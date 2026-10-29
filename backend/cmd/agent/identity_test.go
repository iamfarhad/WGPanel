package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// writeExistingRegistration fills dir with a loadable identity (self-signed cert
// doubling as its own CA) for node "old-node", as a previous install would have left.
func writeExistingRegistration(t *testing.T, dir string) paths {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "old-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	p := statePaths(dir)
	for path, content := range map[string][]byte{
		p.cert:   certPEM,
		p.key:    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		p.ca:     certPEM,
		p.nodeID: []byte("old-node"),
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestJoinTokenChangedAdoptsTokenForLegacyState(t *testing.T) {
	p := statePaths(t.TempDir())
	cfg := config{JoinToken: "token-a"}

	// No hash file yet (state from before it existed): adopt, don't re-register.
	if joinTokenChanged(cfg, p) {
		t.Fatal("legacy state without a recorded token hash must not count as changed")
	}
	if joinTokenChanged(cfg, p) {
		t.Fatal("same token must not count as changed")
	}
	if !joinTokenChanged(config{JoinToken: "token-b"}, p) {
		t.Fatal("a different token must count as changed")
	}
	if joinTokenChanged(config{}, p) {
		t.Fatal("no configured token must never trigger re-registration")
	}
}

func TestLoadOrRegisterKeepsExistingIdentityWhenNewTokenIsRejected(t *testing.T) {
	dir := t.TempDir()
	p := writeExistingRegistration(t, dir)
	if err := os.WriteFile(p.tokenHash, []byte(hashJoinToken("old-token")), 0600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	panel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
	}))
	defer panel.Close()

	cfg := config{
		PanelAddr: strings.TrimPrefix(panel.URL, "https://"),
		JoinToken: "new-token",
		NodeName:  "n",
		StateDir:  dir,
	}
	id, err := loadOrRegister(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("loadOrRegister: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one registration attempt with the changed token, got %d", calls)
	}
	if id.NodeID != "old-node" || !id.Reused {
		t.Fatalf("expected the existing identity to be kept, got node %q reused=%v", id.NodeID, id.Reused)
	}
}

func TestLoadOrRegisterReplacesIdentityWhenNewTokenIsAccepted(t *testing.T) {
	dir := t.TempDir()
	p := writeExistingRegistration(t, dir)
	if err := os.WriteFile(p.tokenHash, []byte(hashJoinToken("old-token")), 0600); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(p.ca)
	if err != nil {
		t.Fatal(err)
	}
	caPair, err := tls.LoadX509KeyPair(p.cert, p.key)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caPair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	panel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sign whatever key the agent sent with the "CA" from the old state.
		var req struct {
			CSRPEM string `json:"csr_pem"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode register request: %v", err)
			return
		}
		block, _ := pem.Decode([]byte(req.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Errorf("parse CSR: %v", err)
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: "new-node"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caPair.PrivateKey)
		if err != nil {
			t.Errorf("sign: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id":              "new-node",
			"certificate_pem":      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			"ca_certificate_pem":   string(caPEM),
			"wg_subnet":            "10.67.0.0/24",
			"wg_interface_address": "10.67.0.1/24",
			"public_endpoint":      "vpn.example.com:51820",
			"wg_listen_port":       51820,
		})
	}))
	defer panel.Close()

	cfg := config{
		PanelAddr: strings.TrimPrefix(panel.URL, "https://"),
		JoinToken: "new-token",
		NodeName:  "n",
		StateDir:  dir,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := loadOrRegister(cfg, logger)
	if err != nil {
		t.Fatalf("loadOrRegister: %v", err)
	}
	if id.NodeID != "new-node" || id.Reused {
		t.Fatalf("expected a fresh registration as new-node, got node %q reused=%v", id.NodeID, id.Reused)
	}
	if id.Network.WGInterfaceAddress != "10.67.0.1/24" {
		t.Fatalf("expected the node network from the register response, got %+v", id.Network)
	}

	// The next start with the same token reuses the new state, network included.
	again, err := loadOrRegister(cfg, logger)
	if err != nil {
		t.Fatalf("second loadOrRegister: %v", err)
	}
	if again.NodeID != "new-node" || !again.Reused || again.Network.WGSubnet != "10.67.0.0/24" {
		t.Fatalf("expected the persisted new registration to be reused, got %+v", again)
	}
}
