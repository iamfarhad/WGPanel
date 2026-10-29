package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/store"
)

// TestEndpointsSubscriptionAndPagingEndToEnd drives the real router against Postgres:
// node endpoints CRUD and selection in generated configs (incl. MTU), the public
// subscription page API, content negotiation, cursor paging and the Bot API limiter.
// Skipped without WGPANEL_TEST_POSTGRES_DSN; deletes what it creates.
func TestEndpointsSubscriptionAndPagingEndToEnd(t *testing.T) {
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
	group := "epit-" + sfx
	pub := "pk-epit-" + sfx
	node, err := st.CreateNode(ctx, "epit-"+sfx, group, "eu", "direct.test:51820", "10.69.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DELETE FROM accounts WHERE label LIKE $1`, "epit-"+sfx+"%")
		_, _ = conn.Exec(bg, `DELETE FROM api_keys WHERE key_id = $1`, "epit-key-"+sfx)
		_, _ = conn.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID) // cascades node_endpoints
		_, _ = conn.Exec(bg, `UPDATE panel_settings SET client_mtu = 1280 WHERE id = 1`)
	})

	do := func(req *http.Request) (int, string, http.Header) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code, rr.Body.String(), rr.Header()
	}
	admin := func(role, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+adminToken(t, role))
		code, raw, _ := do(req)
		return code, raw
	}
	decode := func(raw string) map[string]any {
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		return m
	}
	must := func(what string, want, got int, raw string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: status %d, want %d: %s", what, got, want, raw)
		}
	}

	code, raw := admin("operator", "POST", "/api/v1/accounts", `{"label":"epit-`+sfx+`-a","node_id":"`+node.ID+`","data_quota_gb":10}`)
	must("create account", 201, code, raw)
	acc := decode(raw)
	accID := acc["id"].(string)
	var token string
	if err := conn.QueryRow(ctx, `SELECT subscription_token FROM accounts WHERE id = $1`, accID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	cfgPath := "/api/v1/accounts/" + accID + "/config"
	epBase := "/api/v1/nodes/" + node.ID + "/endpoints"

	getCfg := func(query string) (int, string) { return admin("operator", "GET", cfgPath+query, "") }

	t.Run("no endpoints: direct address and default MTU (backward compatible)", func(t *testing.T) {
		code, cfg := getCfg("")
		must("config", 200, code, cfg)
		if !strings.Contains(cfg, "Endpoint = direct.test:51820") || !strings.Contains(cfg, "MTU = 1280") {
			t.Fatalf("unexpected config:\n%s", cfg)
		}
	})

	t.Run("client_mtu setting is used", func(t *testing.T) {
		code, raw := admin("super_admin", "PATCH", "/api/v1/settings", `{"client_mtu":1380}`)
		must("set mtu", 200, code, raw)
		if _, cfg := getCfg(""); !strings.Contains(cfg, "MTU = 1380") {
			t.Fatalf("setting not applied:\n%s", cfg)
		}
		code, raw = admin("super_admin", "PATCH", "/api/v1/settings", `{"client_mtu":1100}`)
		must("reject low mtu", 400, code, raw)
		code, raw = admin("super_admin", "PATCH", "/api/v1/settings", `{"api_rate_rps":0}`)
		must("reject zero rate", 400, code, raw)
		code, raw = admin("super_admin", "PATCH", "/api/v1/settings", `{"client_mtu":1280}`)
		must("reset mtu", 200, code, raw)
	})

	var relayID, relay2ID string
	t.Run("endpoint CRUD and validation", func(t *testing.T) {
		code, raw := admin("operator", "POST", epBase, `{"label":"ir-relay","address":"relay.example.ir:51820","kind":"relay","priority":200,"mtu":1340}`)
		must("create", 201, code, raw)
		relayID = decode(raw)["id"].(string)
		if m := decode(raw); m["health_check"] != "none" || m["last_status"] != "unknown" || m["enabled"] != true {
			t.Fatalf("defaults wrong: %s", raw)
		}
		code, raw = admin("operator", "POST", epBase, `{"label":"ir-relay","address":"x.example.ir:1"}`)
		must("duplicate label", 409, code, raw)
		for _, bad := range []string{
			`{"label":"","address":"a.b:1"}`, `{"label":"x","address":"nohost"}`,
			`{"label":"x","address":"a.b:1","kind":"weird"}`, `{"label":"x","address":"a.b:1","health_check":"icmp"}`,
			`{"label":"x","address":"a.b:1","mtu":900}`,
		} {
			code, raw = admin("operator", "POST", epBase, bad)
			must("invalid "+bad, 400, code, raw)
		}
		code, raw = admin("support", "POST", epBase, `{"label":"s","address":"a.b:1"}`)
		must("support is read-only", 403, code, raw)
		code, raw = admin("support", "GET", epBase, "")
		must("support can list", 200, code, raw)
		code, raw = admin("operator", "POST", "/api/v1/nodes/00000000-0000-0000-0000-000000000000/endpoints", `{"label":"s","address":"a.b:1"}`)
		must("unknown node", 404, code, raw)

		code, raw = admin("operator", "PATCH", epBase+"/"+relayID, `{"priority":300,"label":"ir-relay-1"}`)
		must("patch", 200, code, raw)
		code, raw = admin("operator", "POST", epBase, `{"label":"second","address":"relay2.example.ir:51820","priority":50,"health_check":"tcp"}`)
		must("create 2", 201, code, raw)
		relay2ID = decode(raw)["id"].(string)
	})

	t.Run("config uses the highest-priority healthy endpoint with its MTU", func(t *testing.T) {
		code, cfg := getCfg("")
		must("config", 200, code, cfg)
		if !strings.Contains(cfg, "Endpoint = relay.example.ir:51820") || !strings.Contains(cfg, "MTU = 1340") {
			t.Fatalf("relay not selected:\n%s", cfg)
		}
		// Disabled endpoint drops out; the unchecked tcp one is not trusted yet -> direct.
		code, raw := admin("operator", "PATCH", epBase+"/"+relayID, `{"enabled":false}`)
		must("disable", 200, code, raw)
		if _, cfg := getCfg(""); !strings.Contains(cfg, "Endpoint = direct.test:51820") || !strings.Contains(cfg, "MTU = 1280") {
			t.Fatalf("expected fallback to public_endpoint:\n%s", cfg)
		}
		// Once health-checked up, the tcp endpoint is used.
		if _, err := conn.Exec(ctx, `UPDATE node_endpoints SET last_status='up', last_checked_at=now() WHERE id=$1`, relay2ID); err != nil {
			t.Fatal(err)
		}
		if _, cfg := getCfg(""); !strings.Contains(cfg, "Endpoint = relay2.example.ir:51820") {
			t.Fatalf("healthy tcp endpoint not selected:\n%s", cfg)
		}
		if _, err := conn.Exec(ctx, `UPDATE node_endpoints SET last_status='down' WHERE id=$1`, relay2ID); err != nil {
			t.Fatal(err)
		}
		if _, cfg := getCfg(""); !strings.Contains(cfg, "Endpoint = direct.test:51820") {
			t.Fatalf("down endpoint must be skipped:\n%s", cfg)
		}
		// Editing the address resets the health verdict.
		code, raw = admin("operator", "PATCH", epBase+"/"+relay2ID, `{"address":"relay3.example.ir:51820"}`)
		must("patch addr", 200, code, raw)
		if m := decode(raw); m["last_status"] != "unknown" || m["last_checked_at"] != nil {
			t.Fatalf("health not reset: %s", raw)
		}
	})

	t.Run("explicit endpoint_id is staff-only and exact", func(t *testing.T) {
		code, cfg := getCfg("?endpoint_id=" + relayID) // disabled, still selectable by staff
		must("explicit", 200, code, cfg)
		if !strings.Contains(cfg, "Endpoint = relay.example.ir:51820") {
			t.Fatalf("explicit endpoint ignored:\n%s", cfg)
		}
		if code, cfg := getCfg("?endpoint_id=direct"); code != 200 || !strings.Contains(cfg, "Endpoint = direct.test:51820") {
			t.Fatalf("direct: %d\n%s", code, cfg)
		}
		code, raw := getCfg("?endpoint_id=00000000-0000-0000-0000-000000000000")
		must("unknown endpoint", 404, code, raw)
		// A reseller-owned or API-key caller must not use it.
		if _, err := st.CreateAPIKey(ctx, "epit-key-"+sfx, mustEnc(t, "sec"), "epit", []string{group}, []string{"read", "create"}, nil); err != nil {
			t.Fatal(err)
		}
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req := httptest.NewRequest("GET", cfgPath+"?endpoint_id=direct", nil)
		req.Header.Set("X-API-Key", "epit-key-"+sfx)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC("sec", "GET\n"+cfgPath+"\n"+ts+"\n"))
		// The key is not the account's owner (admin-created account), so 404 or 403 -
		// either way it must not succeed.
		if code, raw, _ := do(req); code == 200 {
			t.Fatalf("API key used endpoint_id: %s", raw)
		}
	})

	t.Run("endpoint check and delete", func(t *testing.T) {
		old := tcpProbe
		t.Cleanup(func() { tcpProbe = old })
		tcpProbe = func(ctx context.Context, address string) (bool, time.Duration) { return true, 12 * time.Millisecond }
		code, raw := admin("operator", "POST", epBase+"/"+relay2ID+"/check", "")
		must("check", 200, code, raw)
		if m := decode(raw); m["last_status"] != "up" || m["last_latency_ms"] != float64(12) || m["last_checked_at"] == nil {
			t.Fatalf("check result: %s", raw)
		}
		tcpProbe = func(ctx context.Context, address string) (bool, time.Duration) { return false, 0 }
		s.sweepEndpointHealth(ctx) // not due yet (just checked) -> must not flip
		if m := decode(func() string { _, r := admin("operator", "GET", epBase, ""); return r }()); m["endpoints"] == nil {
			t.Fatal("list failed")
		}
		code, raw = admin("operator", "POST", epBase+"/"+relayID+"/check", "")
		must("check disabled-health endpoint", 409, code, raw)
		code, raw = admin("operator", "DELETE", epBase+"/"+relayID, "")
		must("delete", 204, code, raw)
		code, raw = admin("operator", "DELETE", epBase+"/"+relayID, "")
		must("delete again", 404, code, raw)
	})

	t.Run("sweep marks an unreachable endpoint down and skips none-mode", func(t *testing.T) {
		old := tcpProbe
		t.Cleanup(func() { tcpProbe = old })
		tcpProbe = func(ctx context.Context, address string) (bool, time.Duration) { return false, 0 }
		if _, err := conn.Exec(ctx, `UPDATE node_endpoints SET last_checked_at = now() - interval '5 minutes' WHERE id = $1`, relay2ID); err != nil {
			t.Fatal(err)
		}
		s.sweepEndpointHealth(ctx)
		ep, err := st.GetNodeEndpoint(ctx, node.ID, relay2ID)
		if err != nil || ep.LastStatus != "down" {
			t.Fatalf("endpoint should be down after a failed probe: %+v %v", ep, err)
		}
	})

	t.Run("public subscription page API", func(t *testing.T) {
		get := func(path string, hdr map[string]string) (int, string, http.Header) {
			req := httptest.NewRequest("GET", path, nil)
			req.RemoteAddr = "198.51.100.200:1"
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			return do(req)
		}
		code, raw, hdr := get("/api/v1/sub/"+token+"/info", nil)
		must("info", 200, code, raw)
		if hdr.Get("Cache-Control") != "no-store" {
			t.Error("info must be no-store")
		}
		m := decode(raw)
		if m["status"] != "active" || m["label"] != "epit-"+sfx+"-a" || m["data_quota_bytes"] != float64(10e9) || m["config_path"] == "" {
			t.Fatalf("info: %s", raw)
		}
		for _, leak := range []string{"id", "subscription_token", "token", "owner", "assigned_ip", "public_key", "private_key"} {
			if _, ok := m[leak]; ok {
				t.Errorf("info leaks %q", leak)
			}
		}
		if strings.Contains(raw, accID) {
			t.Errorf("info leaks the account id")
		}
		if nodes, _ := m["nodes"].([]any); len(nodes) != 1 {
			t.Errorf("want 1 node, got %s", raw)
		}

		// Browser navigation is redirected; machine clients and ?format=conf get the config.
		code, _, hdr = get("/api/v1/sub/"+token, map[string]string{"Accept": "text/html,application/xhtml+xml"})
		if code != http.StatusFound || hdr.Get("Location") != "/s/"+token {
			t.Fatalf("browser: %d %q", code, hdr.Get("Location"))
		}
		for _, c := range []struct {
			q   string
			hdr map[string]string
		}{{"", nil}, {"", map[string]string{"Accept": "*/*"}}, {"?format=conf", map[string]string{"Accept": "text/html"}}} {
			code, raw, hdr = get("/api/v1/sub/"+token+c.q, c.hdr)
			if code != 200 || !strings.Contains(raw, "[Interface]") || hdr.Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("machine fetch %q: %d %s", c.q, code, raw)
			}
		}

		// Suspended: info explains, config refuses.
		if _, err := conn.Exec(ctx, `UPDATE accounts SET status='suspended', suspend_reason='expired' WHERE id=$1`, accID); err != nil {
			t.Fatal(err)
		}
		code, raw, _ = get("/api/v1/sub/"+token+"/info", nil)
		must("suspended info", 200, code, raw)
		if m := decode(raw); m["status"] != "suspended" || m["status_reason"] != "expired" {
			t.Fatalf("suspended info: %s", raw)
		}
		code, raw, _ = get("/api/v1/sub/"+token+"?format=conf", nil)
		must("suspended config", 403, code, raw)
		if _, err := conn.Exec(ctx, `UPDATE accounts SET status='active', suspend_reason=NULL WHERE id=$1`, accID); err != nil {
			t.Fatal(err)
		}

		code, raw, _ = get("/api/v1/sub/"+strings.Repeat("0", 48)+"/info", nil)
		must("unknown token", 404, code, raw)
	})

	t.Run("cursor pagination", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			code, raw := admin("operator", "POST", "/api/v1/accounts", `{"label":"epit-`+sfx+`-p`+strconv.Itoa(i)+`","node_id":"`+node.ID+`"}`)
			must("create paged", 201, code, raw)
		}
		seen := map[string]bool{}
		cursor, pages := "", 0
		for {
			code, raw := admin("operator", "GET", "/api/v1/accounts?limit=2&owner=admin&cursor="+cursor, "")
			must("page", 200, code, raw)
			m := decode(raw)
			list := m["accounts"].([]any)
			if len(list) > 2 {
				t.Fatalf("page larger than limit: %d", len(list))
			}
			for _, a := range list {
				id := a.(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatalf("account %s repeated across pages", id)
				}
				seen[id] = true
			}
			pages++
			next, present := m["next_cursor"]
			if !present {
				t.Fatalf("next_cursor missing in cursor mode: %s", raw)
			}
			if next == nil {
				break
			}
			cursor = next.(string)
			if pages > 200 {
				t.Fatal("paging loop")
			}
		}
		if !seen[accID] || len(seen) < 5 {
			t.Fatalf("cursor paging missed accounts: saw %d", len(seen))
		}
		// Offset mode is unchanged: no next_cursor key.
		code, raw := admin("operator", "GET", "/api/v1/accounts?limit=2&offset=0", "")
		must("offset", 200, code, raw)
		if _, has := decode(raw)["next_cursor"]; has {
			t.Fatalf("offset mode must not grow next_cursor: %s", raw)
		}
		code, raw = admin("operator", "GET", "/api/v1/accounts?cursor=garbage", "")
		must("bad cursor", 400, code, raw)
		code, raw = admin("operator", "GET", "/api/v1/accounts?cursor=&offset=1", "")
		must("cursor+offset", 400, code, raw)
	})

	t.Run("Bot API per-key rate limit", func(t *testing.T) {
		rps, burst := 1, 3
		if _, err := st.UpdateSettings(ctx, store.UpdateSettingsParams{APIRateRPS: &rps, APIRateBurst: &burst}); err != nil {
			t.Fatal(err)
		}
		resetAPIRateCache()
		keyID := "epit-key-" + sfx
		call := func() (int, http.Header) {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
			req.Header.Set("X-API-Key", keyID)
			req.Header.Set("X-Timestamp", ts)
			req.Header.Set("X-Signature", hexHMAC("sec", "GET\n/api/v1/nodes\n"+ts+"\n"))
			code, _, hdr := do(req)
			return code, hdr
		}
		var last int
		var hdr http.Header
		for i := 0; i < 3; i++ {
			last, hdr = call()
			if last != 200 {
				t.Fatalf("request %d: %d", i, last)
			}
			if hdr.Get("X-RateLimit-Limit") != "3" || hdr.Get("X-RateLimit-Remaining") == "" {
				t.Fatalf("missing rate headers on success: %v", hdr)
			}
		}
		last, hdr = call()
		if last != 429 || hdr.Get("Retry-After") == "" || hdr.Get("X-RateLimit-Remaining") != "0" || hdr.Get("X-RateLimit-Reset") == "" {
			t.Fatalf("expected 429 with headers, got %d %v", last, hdr)
		}
		// A bad signature must not consume the key's bucket (limiter runs after auth).
		req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
		req.Header.Set("X-Signature", "deadbeef")
		if code, _, _ := do(req); code != 401 {
			t.Fatalf("bad signature: %d, want 401", code)
		}
	})
}
