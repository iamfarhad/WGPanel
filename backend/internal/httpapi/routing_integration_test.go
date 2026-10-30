package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/routing"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// TestRoutingPSKIPv6EndToEnd drives the real router against Postgres through the three
// features of migration 0029: split-tunnel routing modes (accounts, templates, bulk, the
// public info page), the per-account PresharedKey with its rollout latch, and per-node
// IPv6 egress. Skipped without WGPANEL_TEST_POSTGRES_DSN; deletes what it creates.
func TestRoutingPSKIPv6EndToEnd(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run integration tests")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	relaxAPIRate(t, st)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Routes()

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	group := "rt-" + sfx
	pub := "pk-rt-" + sfx
	const subnet = "10.77.0.0/24"
	node, err := st.CreateNode(ctx, "rt-"+sfx, group, "eu", "rt.test:51820", subnet, 200, &pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}
	keyID := "rtbot-" + sfx
	if _, err := st.CreateAPIKey(ctx, keyID, mustEnc(t, "secret-rt"), "rtbot", []string{group},
		[]string{"read", "create", "update", "suspend", "delete"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = $1`, keyID)
		_, _ = conn.Exec(bg, `DELETE FROM plan_templates WHERE owner_namespace = $1`, keyID)
		_, _ = conn.Exec(bg, `DELETE FROM api_keys WHERE key_id = $1`, keyID)
		_, _ = conn.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID)
	})

	do := func(req *http.Request) (int, string, map[string]any) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, rr.Body.String(), out
	}
	bot := func(method, path, body string) (int, string, map[string]any) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC("secret-rt", method+"\n"+strings.SplitN(path, "?", 2)[0]+"\n"+ts+"\n"+body))
		return do(req)
	}
	admin := func(role, method, path, body string) (int, string, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken(t, role))
		return do(req)
	}
	must := func(what string, want, code int, raw string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: status %d, want %d: %s", what, code, want, raw)
		}
	}
	createAccount := func(body string) map[string]any {
		t.Helper()
		code, raw, out := bot("POST", "/api/v1/accounts", body)
		must("create "+body, 201, code, raw)
		return out
	}
	config := func(id string) map[string]string {
		t.Helper()
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		path := "/api/v1/accounts/" + id + "/config"
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC("secret-rt", "GET\n"+path+"\n"+ts+"\n"))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("config: %d %s", rr.Code, rr.Body.String())
		}
		kv := map[string]string{}
		for _, line := range strings.Split(rr.Body.String(), "\n") {
			if k, v, ok := strings.Cut(line, " = "); ok {
				kv[k] = v
			}
		}
		return kv
	}
	allowed := func(cfg map[string]string) []string { return strings.Split(cfg["AllowedIPs"], ", ") }

	t.Run("default account keeps the historical config", func(t *testing.T) {
		a := createAccount(`{"label":"plain"}`)
		if a["routing_mode"] != nil || a["routing_precision"] != nil {
			t.Fatalf("default account must report null routing, got %v / %v", a["routing_mode"], a["routing_precision"])
		}
		cfg := config(a["id"].(string))
		if cfg["AllowedIPs"] != "0.0.0.0/0, ::/0" {
			t.Fatalf("AllowedIPs = %q, want the exact historical line", cfg["AllowedIPs"])
		}
		if !strings.HasSuffix(cfg["Address"], "/32") || strings.Contains(cfg["Address"], ",") {
			t.Fatalf("Address = %q", cfg["Address"])
		}
		if _, has := cfg["PresharedKey"]; has {
			t.Fatal("a node whose agent has not reported psk support must get no PresharedKey")
		}
		// Every new account has a PSK stored, encrypted.
		enc, err := st.GetAccountPSK(ctx, a["id"].(string))
		if err != nil || enc == nil {
			t.Fatalf("new account has no stored PSK: %v", err)
		}
		if _, err := wgkeys.Decrypt(itHexKey, *enc); err != nil {
			t.Fatalf("stored PSK is not decryptable: %v", err)
		}
	})

	t.Run("bypass_iran config and the public info page", func(t *testing.T) {
		a := createAccount(`{"label":"bypass","routing_mode":"bypass_iran"}`)
		if a["routing_mode"] != "bypass_iran" {
			t.Fatalf("routing_mode = %v", a["routing_mode"])
		}
		cfg := config(a["id"].(string))
		entries := allowed(cfg)
		if entries[len(entries)-1] != "::/0" || strings.Contains(cfg["AllowedIPs"], "0.0.0.0/0") {
			t.Fatalf("AllowedIPs = %.100s", cfg["AllowedIPs"])
		}
		t.Logf("bypass_iran (compact) AllowedIPs entries: %d", len(entries))
		if len(entries) > routing.CompactBudget+20 {
			t.Fatalf("compact config has %d entries", len(entries))
		}
		// DNS (the node forwarder / public default) stays reachable through the tunnel.
		dnsIP := strings.Split(cfg["DNS"], ",")[0]
		if !rtCovered(entries, strings.TrimSpace(dnsIP)) {
			t.Fatalf("DNS %s is not covered by AllowedIPs - name resolution would leave the tunnel", dnsIP)
		}
		if !rtCovered(entries, "10.77.0.1") {
			t.Fatal("node subnet must stay in the tunnel in bypass mode")
		}
		if rtCovered(entries, "2.145.1.1") || rtCovered(entries, "192.168.1.1") {
			t.Fatal("Iranian / LAN addresses must bypass the tunnel")
		}
		if !rtCovered(entries, "8.8.8.8") {
			t.Fatal("foreign addresses must ride the tunnel")
		}

		// exact precision: many more entries
		b := createAccount(`{"label":"bypass-exact","routing_mode":"bypass_iran","routing_precision":"exact"}`)
		exact := allowed(config(b["id"].(string)))
		t.Logf("bypass_iran (exact) AllowedIPs entries: %d", len(exact))
		if len(exact) < 4000 {
			t.Fatalf("exact config has only %d entries", len(exact))
		}

		// The public subscription info page shows the mode (and never the CIDRs).
		sub := strings.TrimPrefix(a["subscription_path"].(string), "/api/v1/sub/")
		req := httptest.NewRequest("GET", "/api/v1/sub/"+sub+"/info", nil)
		code, raw, out := do(req)
		must("sub info", 200, code, raw)
		if out["routing_mode"] != "bypass_iran" {
			t.Fatalf("info routing_mode = %v", out["routing_mode"])
		}
	})

	t.Run("update: custom, validation, back to default", func(t *testing.T) {
		a := createAccount(`{"label":"upd"}`)
		id := a["id"].(string)
		code, raw, _ := bot("PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"custom"}`)
		must("custom without cidrs", 400, code, raw)
		code, raw, _ = bot("PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"custom","routing_excluded_cidrs":["not-a-cidr"]}`)
		must("custom with junk", 400, code, raw)
		code, raw, _ = bot("PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"nonsense"}`)
		must("unknown mode", 400, code, raw)
		// A rejected PATCH leaves the other fields of the same body untouched.
		code, raw, _ = bot("PATCH", "/api/v1/accounts/"+id, `{"label":"renamed","routing_mode":"nonsense"}`)
		must("mixed body", 400, code, raw)
		code, raw, out := bot("GET", "/api/v1/accounts/"+id, "")
		must("get", 200, code, raw)
		if out["label"] != "upd" {
			t.Fatalf("label changed by a rejected PATCH: %v", out["label"])
		}

		code, raw, out = bot("PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"custom","routing_excluded_cidrs":["203.0.113.0/24","198.51.100.77"],"routing_precision":"exact"}`)
		must("custom", 200, code, raw)
		if out["routing_mode"] != "custom" || len(out["routing_excluded_cidrs"].([]any)) != 2 || out["routing_precision"] != "exact" {
			t.Fatalf("response = %v", out)
		}
		entries := allowed(config(id))
		if rtCovered(entries, "203.0.113.9") || rtCovered(entries, "198.51.100.77") || !rtCovered(entries, "198.51.100.78") {
			t.Fatalf("custom exclusions not applied: %v", entries)
		}

		// Updating only the list keeps the mode.
		code, raw, out = bot("PATCH", "/api/v1/accounts/"+id, `{"routing_excluded_cidrs":["203.0.113.0/24"]}`)
		must("list only", 200, code, raw)
		if out["routing_mode"] != "custom" || len(out["routing_excluded_cidrs"].([]any)) != 1 {
			t.Fatalf("response = %v", out)
		}

		// Leaving custom drops the stale list; "" means back to the default.
		code, raw, out = bot("PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"","routing_precision":""}`)
		must("clear", 200, code, raw)
		if out["routing_mode"] != nil || out["routing_precision"] != nil || len(out["routing_excluded_cidrs"].([]any)) != 0 {
			t.Fatalf("response = %v", out)
		}
		if got := config(id)["AllowedIPs"]; got != "0.0.0.0/0, ::/0" {
			t.Fatalf("AllowedIPs after clear = %q", got)
		}

		// Staff can edit routing of any account too.
		code, raw, _ = admin("super_admin", "PATCH", "/api/v1/accounts/"+id, `{"routing_mode":"bypass_iran"}`)
		must("admin sees everything", 200, code, raw)
	})

	t.Run("templates and bulk create", func(t *testing.T) {
		code, raw, tm := bot("POST", "/api/v1/templates", `{"name":"iran-direct-`+sfx+`","routing_mode":"bypass_iran","routing_precision":"exact"}`)
		must("create template", 201, code, raw)
		if tm["routing_mode"] != "bypass_iran" || tm["routing_precision"] != "exact" {
			t.Fatalf("template = %v", tm)
		}
		code, raw, _ = bot("POST", "/api/v1/templates", `{"name":"bad-`+sfx+`","routing_mode":"custom"}`)
		must("custom template without cidrs", 400, code, raw)

		a := createAccount(`{"label":"from-tmpl","template_id":"` + tm["id"].(string) + `"}`)
		if a["routing_mode"] != "bypass_iran" || a["routing_precision"] != "exact" {
			t.Fatalf("account from template = %v / %v", a["routing_mode"], a["routing_precision"])
		}
		b := createAccount(`{"label":"override","template_id":"` + tm["id"].(string) + `","routing_mode":"full"}`)
		if b["routing_mode"] != "full" {
			t.Fatalf("explicit mode must beat the template, got %v", b["routing_mode"])
		}

		code, raw, out := bot("POST", "/api/v1/accounts/bulk", `{"action":"create","count":2,"label_prefix":"bulk-`+sfx+`","template_id":"`+tm["id"].(string)+`"}`)
		must("bulk create", 200, code, raw)
		for _, r := range out["results"].([]any) {
			acc := r.(map[string]any)["account"].(map[string]any)
			if acc["routing_mode"] != "bypass_iran" {
				t.Fatalf("bulk account routing = %v", acc["routing_mode"])
			}
		}

		// Template PUT replaces routing; GET reflects it; list is not broken.
		code, raw, upd := bot("PUT", "/api/v1/templates/"+tm["id"].(string), `{"name":"iran-direct-`+sfx+`","routing_mode":"custom","routing_excluded_cidrs":["203.0.113.0/24"]}`)
		must("update template", 200, code, raw)
		if upd["routing_mode"] != "custom" || len(upd["routing_excluded_cidrs"].([]any)) != 1 {
			t.Fatalf("template after update = %v", upd)
		}
		code, raw, _ = bot("GET", "/api/v1/templates", "")
		must("list templates", 200, code, raw)
	})

	t.Run("account list carries routing", func(t *testing.T) {
		code, raw, out := bot("GET", "/api/v1/accounts?limit=200", "")
		must("list", 200, code, raw)
		sawBypass := false
		for _, a := range out["accounts"].([]any) {
			m := a.(map[string]any)
			if _, has := m["routing_mode"]; !has {
				t.Fatalf("account in list lacks routing_mode: %v", m)
			}
			if m["routing_mode"] == "bypass_iran" {
				sawBypass = true
			}
		}
		if !sawBypass {
			t.Fatal("list did not report the bypass_iran accounts")
		}
	})

	t.Run("PresharedKey rollout latch", func(t *testing.T) {
		a := createAccount(`{"label":"psk-a"}`)
		b := createAccount(`{"label":"psk-b"}`)
		aID, bID := a["id"].(string), b["id"].(string)
		aPub := a["public_key"].(string)

		desired := func() ([]store.DesiredPeer, []agentHeartbeatPeer) {
			d, err := st.ListDesiredPeersForNode(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			peers := make([]agentHeartbeatPeer, len(d))
			for i, p := range d {
				peers[i] = agentHeartbeatPeer{PublicKey: p.PublicKey, AllowedIPs: []string{p.AssignedIP + "/32"}}
			}
			return d, peers
		}
		n, _ := st.GetNode(ctx, node.ID)

		// Old agent: nothing reported. No config PSK, nothing sent to the node.
		if err := st.RecordNodeRoutingState(ctx, node.ID, false, false, false); err != nil {
			t.Fatal(err)
		}
		if _, has := config(aID)["PresharedKey"]; has {
			t.Fatal("config carries a PSK for a node without psk support")
		}

		// The agent upgrades and reports support. Nothing issued yet, so the heartbeat
		// still sends no PSK: existing clients (which imported PSK-less configs) keep working.
		if err := st.RecordNodeRoutingState(ctx, node.ID, true, false, false); err != nil {
			t.Fatal(err)
		}
		d, peers := desired()
		var resp agentHeartbeatResponse
		s.augmentHeartbeatRouting(ctx, n, routingAgentReport{PSKSupported: true}, d, peers, &resp)
		for _, p := range peers {
			if p.PresharedKey != "" {
				t.Fatalf("node peer requires a PSK that no client holds yet: %s", p.PublicKey)
			}
		}

		// Serving account a's config issues its key: the config carries it, and from
		// now on the node's peer for a requires the very same key. b is untouched.
		cfg := config(aID)
		psk := cfg["PresharedKey"]
		if psk == "" {
			t.Fatal("config lacks PresharedKey although the node supports it")
		}
		enc, _ := st.GetAccountPSK(ctx, aID)
		want, _ := wgkeys.Decrypt(itHexKey, *enc)
		if psk != want {
			t.Fatal("config PSK differs from the stored one")
		}
		d, peers = desired()
		resp = agentHeartbeatResponse{}
		s.augmentHeartbeatRouting(ctx, n, routingAgentReport{PSKSupported: true}, d, peers, &resp)
		for i, p := range peers {
			switch d[i].PublicKey {
			case aPub:
				if p.PresharedKey != want {
					t.Fatalf("issued peer got PSK %q, want the account's", p.PresharedKey)
				}
			default:
				if p.PresharedKey != "" {
					t.Fatalf("peer of an account whose config was never served got a PSK")
				}
			}
		}
		// An agent that does NOT report support on this heartbeat never gets one.
		d, peers = desired()
		s.augmentHeartbeatRouting(ctx, n, routingAgentReport{PSKSupported: false}, d, peers, &resp)
		for _, p := range peers {
			if p.PresharedKey != "" {
				t.Fatal("PSK sent to an agent that did not report support")
			}
		}
		_ = bID
		// Rendering twice yields the same key (no rotation on fetch).
		if config(aID)["PresharedKey"] != psk {
			t.Fatal("PSK changed between fetches")
		}
		if err := st.RecordNodeRoutingState(ctx, node.ID, false, false, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("node IPv6 opt-in", func(t *testing.T) {
		a := createAccount(`{"label":"v6"}`)
		id := a["id"].(string)

		// Staff only; support role may not change it.
		code, raw, _ := admin("support", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":true}`)
		must("support", 403, code, raw)
		code, raw, _ = bot("GET", "/api/v1/node-ipv6", "")
		if code == 200 {
			t.Fatalf("an API key must not read the node IPv6 list: %s", raw)
		}
		code, raw, _ = admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":true,"ula_prefix":"2001:db8::/64"}`)
		must("global prefix", 400, code, raw)
		code, raw, _ = admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":true,"ula_prefix":"fd00::/48"}`)
		must("wrong length", 400, code, raw)

		code, raw, out := admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":true}`)
		must("enable", 200, code, raw)
		prefix, _ := out["ipv6_ula_prefix"].(string)
		if _, err := routing.NormalizeULAPrefix(prefix); err != nil {
			t.Fatalf("a ULA /64 should have been generated, got %q", prefix)
		}
		if out["ipv6_enabled"] != true || out["ipv6_active"] != false {
			t.Fatalf("enabled but not yet reported ready must not be active: %v", out)
		}
		// Enabled but not ready: client config stays IPv4-only.
		cfg := config(id)
		if strings.Contains(cfg["Address"], ":") {
			t.Fatalf("Address %q has IPv6 before the agent reported it working", cfg["Address"])
		}

		n, _ := st.GetNode(ctx, node.ID)
		d, _ := st.ListDesiredPeersForNode(ctx, node.ID)
		mkPeers := func() []agentHeartbeatPeer {
			ps := make([]agentHeartbeatPeer, len(d))
			for i, p := range d {
				ps[i] = agentHeartbeatPeer{PublicKey: p.PublicKey, AllowedIPs: []string{p.AssignedIP + "/32"}}
			}
			return ps
		}
		peers := mkPeers()
		var resp agentHeartbeatResponse
		s.augmentHeartbeatRouting(ctx, n, routingAgentReport{}, d, peers, &resp)
		if resp.IPv6 == nil || !resp.IPv6.Enabled || resp.IPv6.Prefix != prefix || !strings.HasPrefix(resp.IPv6.Gateway, strings.TrimSuffix(prefix, "/64")+"1") {
			t.Fatalf("heartbeat IPv6 instruction = %+v", resp.IPv6)
		}
		for _, p := range peers {
			if len(p.AllowedIPs) != 1 {
				t.Fatalf("peer got a v6 route before the node was ready: %v", p.AllowedIPs)
			}
		}

		// Host has no global v6 -> still nothing, however it is reported.
		if err := st.RecordNodeRoutingState(ctx, node.ID, false, false, false); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(config(id)["Address"], ":") {
			t.Fatal("v6 rendered for a node reporting not ready")
		}

		// The agent reports it working: v6 appears everywhere, consistently.
		if err := st.RecordNodeRoutingState(ctx, node.ID, false, true, true); err != nil {
			t.Fatal(err)
		}
		cfg = config(id)
		parts := strings.Split(cfg["Address"], ", ")
		if len(parts) != 2 || !strings.HasSuffix(parts[0], "/32") || !strings.HasSuffix(parts[1], "/128") {
			t.Fatalf("Address = %q, want v4/32 and v6/128", cfg["Address"])
		}
		v6, err := netip.ParsePrefix(parts[1])
		if err != nil || !netip.MustParsePrefix(prefix).Contains(v6.Addr()) {
			t.Fatalf("client v6 %s outside node prefix %s", parts[1], prefix)
		}
		v4 := strings.TrimSuffix(parts[0], "/32")
		wantV6, _ := routing.PeerV6(prefix, subnet, v4)
		if v6.Addr() != wantV6 {
			t.Fatalf("client v6 = %s, want derived %s", v6.Addr(), wantV6)
		}
		if got := allowed(cfg); got[len(got)-1] != "::/0" {
			t.Fatalf("::/0 must stay in AllowedIPs: %v", got)
		}
		peers = mkPeers()
		s.augmentHeartbeatRouting(ctx, n, routingAgentReport{IPv6Global: true, IPv6Ready: true}, d, peers, &resp)
		found := false
		for i, p := range peers {
			if d[i].PublicKey == a["public_key"].(string) {
				found = len(p.AllowedIPs) == 2 && p.AllowedIPs[1] == wantV6.String()+"/128"
			}
		}
		if !found {
			t.Fatal("the account's node peer lacks its /128")
		}

		// Changing the prefix of an enabled node is refused (clients hold the old address).
		code, raw, _ = admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":true,"ula_prefix":"fd12:3456:789a:1::/64"}`)
		must("prefix change while enabled", 409, code, raw)

		// List endpoint.
		code, raw, out = admin("super_admin", "GET", "/api/v1/node-ipv6", "")
		must("list", 200, code, raw)
		sawActive := false
		for _, nn := range out["nodes"].([]any) {
			m := nn.(map[string]any)
			if m["node_id"] == node.ID && m["ipv6_active"] == true {
				sawActive = true
			}
		}
		if !sawActive {
			t.Fatalf("list does not show the node active: %s", raw)
		}

		// Disabling clears "ready" at once and configs drop the address; the prefix is kept.
		code, raw, out = admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":false}`)
		must("disable", 200, code, raw)
		if out["ipv6_active"] != false || out["ipv6_ready"] != false || out["ipv6_ula_prefix"] != prefix {
			t.Fatalf("after disable: %v", out)
		}
		if strings.Contains(config(id)["Address"], ":") {
			t.Fatal("v6 still rendered after the node was disabled")
		}
		// Now the prefix may change.
		code, raw, out = admin("super_admin", "PUT", "/api/v1/node-ipv6/"+node.ID, `{"enabled":false,"ula_prefix":"fd12:3456:789a:1::/64"}`)
		must("prefix change while disabled", 200, code, raw)
		if out["ipv6_ula_prefix"] != "fd12:3456:789a:1::/64" {
			t.Fatalf("prefix = %v", out["ipv6_ula_prefix"])
		}
	})
}
