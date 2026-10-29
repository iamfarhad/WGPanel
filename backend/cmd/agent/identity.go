package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type identity struct {
	NodeID     string
	TLSCert    tls.Certificate
	CACertPool *x509.CertPool
	Network    nodeNetwork // zero value when the registration predates it
	Reused     bool        // loaded from an earlier registration rather than registered just now
}

// loadOrRegister reuses a prior registration persisted under cfg.StateDir if one
// exists, otherwise performs a fresh registration (which requires WGPANEL_JOIN_TOKEN).
//
// A configured join token different from the one the saved state was registered with
// means the operator re-added this node in the panel and re-ran the installer (the
// state volume survives a reinstall) - so register again with it instead of silently
// carrying on as the old node, which looked like success but left the new node
// pending forever (or heartbeating into a 404 if the old node had been deleted). If
// the new token is rejected - a normal token can't re-register a node that is already
// registered - the still-valid old identity is kept and the fallback is logged.
func loadOrRegister(cfg config, logger *slog.Logger) (*identity, error) {
	paths := statePaths(cfg.StateDir)

	if allExist(paths.cert, paths.key, paths.ca, paths.nodeID) {
		if !joinTokenChanged(cfg, paths) {
			logger.Info("using existing registration", "state_dir", cfg.StateDir)
			return loadExistingIdentity(paths)
		}
		logger.Info("join token changed since the last registration - registering again", "panel_addr", cfg.PanelAddr)
		id, err := register(cfg, paths)
		if err == nil {
			return id, nil
		}
		logger.Warn("join_token_rejected_using_existing_registration", "error", err)
		return loadExistingIdentity(paths)
	}

	if cfg.JoinToken == "" {
		return nil, fmt.Errorf("no existing registration in %s and WGPANEL_JOIN_TOKEN is not set", cfg.StateDir)
	}

	logger.Info("registering with control plane", "panel_addr", cfg.PanelAddr)
	return register(cfg, paths)
}

func loadExistingIdentity(p paths) (*identity, error) {
	id, err := loadIdentity(p)
	if err != nil {
		return nil, err
	}
	id.Reused = true
	return id, nil
}

// joinTokenChanged reports whether cfg's join token differs from the one the saved
// state was registered with. State from before the hash was recorded adopts the
// current token as its own: an existing install's .env still carries the (already
// redeemed) token it registered with, and re-registering with that would just fail.
func joinTokenChanged(cfg config, p paths) bool {
	if cfg.JoinToken == "" {
		return false
	}
	want := hashJoinToken(cfg.JoinToken)
	stored, err := os.ReadFile(p.tokenHash)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(p.tokenHash, []byte(want), 0600)
		return false
	}
	if err != nil {
		return false // unreadable - keep the working identity rather than guess
	}
	return strings.TrimSpace(string(stored)) != want
}

func hashJoinToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type paths struct {
	cert, key, ca, nodeID, tokenHash, network string
}

func statePaths(stateDir string) paths {
	return paths{
		cert:      filepath.Join(stateDir, "client-cert.pem"),
		key:       filepath.Join(stateDir, "client-key.pem"),
		ca:        filepath.Join(stateDir, "ca-cert.pem"),
		nodeID:    filepath.Join(stateDir, "node-id.txt"),
		tokenHash: filepath.Join(stateDir, "join-token.sha256"),
		network:   filepath.Join(stateDir, "node-network.json"),
	}
}

func allExist(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// register generates a keypair locally (the private key never leaves this
// process), submits a CSR + the join token, and persists what /agent/register
// returns. The bootstrap call itself skips server certificate verification - the
// join token is the trust anchor for this one exchange, since the agent has no CA
// to verify against yet (docs/STORY-04-node-agent-mtls.md design note).
func register(cfg config, p paths) (*identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cfg.NodeName},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	body := map[string]string{
		"join_token": cfg.JoinToken,
		"csr_pem":    string(csrPEM),
	}
	if cfg.WGPublicKey != "" {
		body["wg_public_key"] = cfg.WGPublicKey
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	bootstrapClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // deliberate - see doc comment above
		Timeout:   15 * time.Second,
	}

	resp, err := bootstrapClient.Post("https://"+cfg.PanelAddr+"/agent/register", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		// An HTML body (or a bare 405) means we reached a web server, not the
		// agent API: WGPANEL_PANEL_ADDR points at the panel's WEB port instead of
		// NODE_AGENT_PORT. Say so - the raw nginx/SPA error page explains nothing.
		if resp.StatusCode == http.StatusMethodNotAllowed ||
			strings.Contains(resp.Header.Get("Content-Type"), "text/html") ||
			bytes.Contains(bytes.ToLower(body), []byte("<html")) {
			return nil, fmt.Errorf("register rejected: %s - this address answers like the panel's web UI, not the node-agent API; WGPANEL_PANEL_ADDR must use the panel's NODE_AGENT_PORT (48443 by default, see the panel server's .env), not the web/HTTPS port", resp.Status)
		}
		return nil, fmt.Errorf("register rejected: %s: %s", resp.Status, string(body))
	}

	var respBody struct {
		NodeID           string `json:"node_id"`
		CertificatePEM   string `json:"certificate_pem"`
		CACertificatePEM string `json:"ca_certificate_pem"`
		nodeNetwork
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	writes := map[string][]byte{
		p.cert:      []byte(respBody.CertificatePEM),
		p.key:       keyPEM,
		p.ca:        []byte(respBody.CACertificatePEM),
		p.nodeID:    []byte(respBody.NodeID),
		p.tokenHash: []byte(hashJoinToken(cfg.JoinToken)),
	}
	for path, content := range writes {
		if err := os.WriteFile(path, content, 0600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	if err := saveNodeNetwork(p, respBody.nodeNetwork); err != nil {
		return nil, err
	}

	return loadIdentity(p)
}

func saveNodeNetwork(p paths, network nodeNetwork) error {
	raw, err := json.Marshal(network)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.network, raw, 0600); err != nil {
		return fmt.Errorf("write %s: %w", p.network, err)
	}
	return nil
}

func loadIdentity(p paths) (*identity, error) {
	tlsCert, err := tls.LoadX509KeyPair(p.cert, p.key)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}

	caBytes, err := os.ReadFile(p.ca)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("parse CA cert")
	}

	nodeIDBytes, err := os.ReadFile(p.nodeID)
	if err != nil {
		return nil, fmt.Errorf("read node id: %w", err)
	}

	id := &identity{NodeID: string(nodeIDBytes), TLSCert: tlsCert, CACertPool: pool}
	// Optional: registrations from before the network check existed have no file,
	// and a corrupt one just skips the check rather than taking the node down.
	if raw, err := os.ReadFile(p.network); err == nil {
		_ = json.Unmarshal(raw, &id.Network)
	}
	return id, nil
}
