package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// TestObfuscationEndToEnd drives the real router against Postgres: profile CRUD and
// validation, assignment to a node and a node group, the "only hand out AWG configs for
// nodes that really apply them" rule across the agent heartbeat contract, rotation (event,
// audit, config revert until re-applied), the subscription info block and backup
// round-trip. Skipped without WGPANEL_TEST_POSTGRES_DSN; deletes what it creates.
func TestObfuscationEndToEnd(t *testing.T) {
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

	bus := events.NewBus(64, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Events: bus,
	}
	h := s.Routes()

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	group := "obfit-" + sfx
	pub := "pk-obfit-" + sfx
	node, err := st.CreateNode(ctx, "obfit-"+sfx, group, "eu", "direct.test:51820", "10.68.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	node2, err := st.CreateNode(ctx, "obfit2-"+sfx, group, "eu", "direct2.test:51820", "10.67.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{node.ID, node2.ID} {
		if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DELETE FROM accounts WHERE label LIKE $1`, "obfit-"+sfx+"%")
		_, _ = conn.Exec(bg, `DELETE FROM nodes WHERE id IN ($1, $2)`, node.ID, node2.ID)
		_, _ = conn.Exec(bg, `DELETE FROM obfuscation_profiles WHERE name LIKE $1`, "obfit-"+sfx+"%")
		_, _ = conn.Exec(bg, `DELETE FROM audit_log WHERE action LIKE 'obfuscation.%' OR action = 'node.obfuscation_assigned'`)
	})

	admin := func(role, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+adminToken(t, role))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code, rr.Body.String()
	}
	decode := func(raw string) map[string]any {
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		return m
	}
	expect := func(what string, want, got int, raw string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: status %d, want %d: %s", what, got, want, raw)
		}
	}

	t.Run("presets", func(t *testing.T) {
		code, raw := admin("support", "GET", "/api/v1/obfuscation/presets", "")
		expect("presets", 200, code, raw)
		var out struct {
			Presets []struct {
				ID                    string `json:"id"`
				Compat                string `json:"compat"`
				StockClientCompatible bool   `json:"stock_client_compatible"`
			} `json:"presets"`
		}
		_ = json.Unmarshal([]byte(raw), &out)
		if len(out.Presets) != 4 || out.Presets[0].ID != "compatible" || !out.Presets[0].StockClientCompatible || out.Presets[1].StockClientCompatible {
			t.Fatalf("presets: %s", raw)
		}
	})

	var profID string
	pName := "obfit-" + sfx + "-a"
	t.Run("create and validate", func(t *testing.T) {
		code, raw := admin("operator", "POST", "/api/v1/obfuscation/profiles", `{"name":"`+pName+`","preset":"awg2-quic"}`)
		expect("create from preset", 201, code, raw)
		m := decode(raw)
		profID = m["id"].(string)
		params := m["params"].(map[string]any)
		if m["compat"] != "awg2" || m["revision"].(float64) != 1 || params["i1"] == "" || params["h1"] == "" || m["stock_client_compatible"] != false {
			t.Fatalf("created profile: %s", raw)
		}
		code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles", `{"name":"`+pName+`","preset":"awg1"}`)
		expect("duplicate name", 409, code, raw)
		code, raw = admin("support", "POST", "/api/v1/obfuscation/profiles", `{"name":"obfit-x","preset":"awg1"}`)
		expect("support is read-only", 403, code, raw)
		for _, bad := range []string{
			`{"name":"","preset":"awg1"}`,
			`{"name":"b1"}`,
			`{"name":"b2","preset":"nope"}`,
			`{"name":"b3","compat":"awg1","params":{"jc":5,"jmin":300,"jmax":100}}`,
			`{"name":"b4","compat":"awg1","params":{"s1":30,"s2":86}}`,
			`{"name":"b5","compat":"awg1","params":{"h1":"3","h2":"7","h3":"8","h4":"9"}}`,
			`{"name":"b6","compat":"awg1","params":{"h1":"7","h2":"7","h3":"8","h4":"9"}}`,
			`{"name":"b7","compat":"awg1","params":{"s3":4}}`,
			`{"name":"b8","compat":"awg2","params":{"i1":"<r 0>"}}`,
			`{"name":"b9","compat":"awg2","params":{"i1":"hello"}}`,
			`{"name":"b10","compat":"awg3","params":{}}`,
		} {
			code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles", bad)
			if code != 400 {
				t.Fatalf("invalid %s: status %d: %s", bad, code, raw)
			}
		}
		code, raw = admin("support", "GET", "/api/v1/obfuscation/profiles/"+profID, "")
		expect("get", 200, code, raw)
		code, raw = admin("support", "GET", "/api/v1/obfuscation/profiles/00000000-0000-0000-0000-000000000000", "")
		expect("unknown id", 404, code, raw)
		code, raw = admin("support", "GET", "/api/v1/obfuscation/profiles/not-a-uuid", "")
		expect("malformed id", 404, code, raw)
	})

	t.Run("update bumps the revision only for parameter changes", func(t *testing.T) {
		code, raw := admin("operator", "PATCH", "/api/v1/obfuscation/profiles/"+profID, `{"name":"`+pName+`-renamed"}`)
		expect("rename", 200, code, raw)
		if rev := decode(raw)["revision"].(float64); rev != 1 {
			t.Fatalf("rename bumped the revision to %v", rev)
		}
		pName += "-renamed"
		code, raw = admin("operator", "PATCH", "/api/v1/obfuscation/profiles/"+profID, `{"compat":"awg1"}`)
		expect("compat awg1 with awg2 params is invalid", 400, code, raw)
		code, raw = admin("operator", "PATCH", "/api/v1/obfuscation/profiles/"+profID,
			`{"params":{"jc":6,"jmin":50,"jmax":300,"s1":20,"s2":30,"s3":10,"s4":8,"h1":"100-200","h2":"300-400","h3":"500-600","h4":"700-800","i1":"<b 0xc0><r 40>"}}`)
		expect("replace params", 200, code, raw)
		if rev := decode(raw)["revision"].(float64); rev != 2 {
			t.Fatalf("param change revision %v, want 2", rev)
		}
	})

	cfgOf := func(accID, nodeID string) string {
		code, cfg := admin("operator", "GET", "/api/v1/accounts/"+accID+"/config?node_id="+nodeID, "")
		expect("config", 200, code, cfg)
		return cfg
	}
	code, raw := admin("operator", "POST", "/api/v1/accounts", `{"label":"obfit-`+sfx+`-acc","node_id":"`+node.ID+`","data_quota_gb":10}`)
	expect("create account", 201, code, raw)
	accID := decode(raw)["id"].(string)
	var subToken string
	if err := conn.QueryRow(ctx, `SELECT subscription_token FROM accounts WHERE id = $1`, accID).Scan(&subToken); err != nil {
		t.Fatal(err)
	}

	t.Run("assignment alone does not make configs AWG", func(t *testing.T) {
		code, raw := admin("operator", "PUT", "/api/v1/nodes/"+node.ID+"/obfuscation", `{"profile_id":"`+profID+`"}`)
		expect("assign", 200, code, raw)
		o := decode(raw)
		if o["mode"] != "wireguard" || o["pending"] != true || o["applied"] != false {
			t.Fatalf("node state after assign: %s", raw)
		}
		if cfg := cfgOf(accID, node.ID); strings.Contains(cfg, "Jc = ") || strings.Contains(cfg, "H1 = ") {
			t.Fatalf("pending node must still get a plain config:\n%s", cfg)
		}
		code, raw = admin("operator", "PUT", "/api/v1/nodes/"+node.ID+"/obfuscation", `{"profile_id":"00000000-0000-0000-0000-000000000000"}`)
		expect("unknown profile", 404, code, raw)
		code, raw = admin("operator", "PUT", "/api/v1/nodes/00000000-0000-0000-0000-000000000000/obfuscation", `{"profile_id":null}`)
		expect("unknown node", 404, code, raw)
		code, raw = admin("support", "PUT", "/api/v1/nodes/"+node.ID+"/obfuscation", `{"profile_id":null}`)
		expect("support is read-only", 403, code, raw)
	})

	t.Run("heartbeat contract", func(t *testing.T) {
		d := s.agentObfuscationFor(ctx, node.ID)
		if d == nil || d.Profile == nil || d.Profile.ID != profID || d.Profile.Revision != 2 || d.Profile.Params.Jc != 6 {
			t.Fatalf("directive: %+v", d)
		}
		if d := s.agentObfuscationFor(ctx, node2.ID); d == nil || d.Profile != nil {
			t.Fatalf("an unassigned node must be told plain WireGuard (profile null): %+v", d)
		}
		// Reporting a stale revision is not "applied".
		s.recordAgentObfuscation(ctx, node.ID, &agentObfuscationReport{Mode: "awg", ProfileID: profID, Revision: 1, Supported: true})
		if cfg := cfgOf(accID, node.ID); strings.Contains(cfg, "Jc = ") {
			t.Fatalf("stale revision must not yield an AWG config:\n%s", cfg)
		}
		// Reporting the current one is.
		s.recordAgentObfuscation(ctx, node.ID, &agentObfuscationReport{Mode: "awg", ProfileID: profID, Revision: 2, Supported: true})
		cfg := cfgOf(accID, node.ID)
		for _, want := range []string{"\nJc = 6\n", "\nS4 = 8\n", "\nH1 = 100-200\n", "\nI1 = <b 0xc0><r 40>\n"} {
			if !strings.Contains(cfg, want) {
				t.Fatalf("config misses %q:\n%s", want, cfg)
			}
		}
		// The AWG lines sit inside [Interface], before [Peer].
		if i, j := strings.Index(cfg, "Jc = 6"), strings.Index(cfg, "[Peer]"); i < 0 || j < 0 || i > j || strings.Index(cfg, "MTU = ") > i {
			t.Fatalf("AWG lines misplaced:\n%s", cfg)
		}
		// Reporting plain WireGuard (agent fell back) drops them again.
		s.recordAgentObfuscation(ctx, node.ID, &agentObfuscationReport{Mode: "wireguard", Supported: true, Error: "boom"})
		if cfg := cfgOf(accID, node.ID); strings.Contains(cfg, "Jc = ") {
			t.Fatalf("fallen-back node must not get AWG config:\n%s", cfg)
		}
		s.recordAgentObfuscation(ctx, node.ID, &agentObfuscationReport{Mode: "awg", ProfileID: profID, Revision: 2, Supported: true})
	})

	t.Run("nodes API exposes the mode", func(t *testing.T) {
		code, raw := admin("operator", "GET", "/api/v1/nodes", "")
		expect("list nodes", 200, code, raw)
		var out struct {
			Nodes []struct {
				ID          string `json:"id"`
				Obfuscation *struct {
					Mode        string `json:"mode"`
					Applied     bool   `json:"applied"`
					ProfileName string `json:"profile_name"`
				} `json:"obfuscation"`
			} `json:"nodes"`
		}
		_ = json.Unmarshal([]byte(raw), &out)
		found := false
		for _, n := range out.Nodes {
			if n.ID == node.ID {
				found = true
				if n.Obfuscation == nil || n.Obfuscation.Mode != "awg" || !n.Obfuscation.Applied || n.Obfuscation.ProfileName != pName {
					t.Fatalf("node obfuscation block: %s", raw)
				}
			}
		}
		if !found {
			t.Fatal("node missing from list")
		}
		code, raw = admin("operator", "GET", "/api/v1/nodes/"+node.ID, "")
		expect("get node", 200, code, raw)
		if o, _ := decode(raw)["obfuscation"].(map[string]any); o == nil || o["mode"] != "awg" {
			t.Fatalf("get node: %s", raw)
		}
	})

	t.Run("subscription info and config", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/sub/"+subToken+"/info", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		expect("info", 200, rr.Code, rr.Body.String())
		var info struct {
			Obfuscation struct {
				AWG    bool   `json:"awg"`
				Compat string `json:"compat"`
				Apps   []struct {
					OS, Name, URL string
				} `json:"apps"`
			} `json:"obfuscation"`
			Nodes []struct {
				AWG bool `json:"awg"`
			} `json:"nodes"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &info)
		if !info.Obfuscation.AWG || info.Obfuscation.Compat != "awg2" || len(info.Obfuscation.Apps) < 4 || len(info.Nodes) == 0 || !info.Nodes[0].AWG {
			t.Fatalf("info: %s", rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "h1") || strings.Contains(rr.Body.String(), "100-200") {
			t.Fatalf("the public page must not receive the parameters: %s", rr.Body.String())
		}

		req = httptest.NewRequest("GET", "/api/v1/sub/"+subToken+"?format=conf", nil)
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		expect("sub config", 200, rr.Code, rr.Body.String())
		if !strings.Contains(rr.Body.String(), "\nJc = 6\n") {
			t.Fatalf("subscription config has no AWG lines:\n%s", rr.Body.String())
		}
	})

	t.Run("rotate", func(t *testing.T) {
		code, raw := admin("support", "POST", "/api/v1/obfuscation/profiles/"+profID+"/rotate", "")
		expect("support cannot rotate", 403, code, raw)
		code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles/"+profID+"/rotate", `{"cps":"bogus"}`)
		expect("bad cps", 400, code, raw)
		before := decode(func() string { _, r := admin("operator", "GET", "/api/v1/obfuscation/profiles/"+profID, ""); return r }())

		code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles/"+profID+"/rotate", `{"cps":"dns"}`)
		expect("rotate", 200, code, raw)
		after := decode(raw)
		if after["revision"].(float64) != 3 || after["compat"] != "awg2" {
			t.Fatalf("rotated: %s", raw)
		}
		bp, ap := before["params"].(map[string]any), after["params"].(map[string]any)
		if bp["h1"] == ap["h1"] && bp["h2"] == ap["h2"] {
			t.Fatalf("rotation kept the headers: %v -> %v", bp, ap)
		}
		if i1, _ := ap["i1"].(string); !strings.HasPrefix(i1, "<r 2><b 0x0100") {
			t.Fatalf("cps=dns must give a DNS-like I1, got %q", i1)
		}

		// The event and the audit entry were emitted.
		gotEvent := false
	drain:
		for {
			select {
			case ev := <-bus.C():
				if ev.Type != events.ObfuscationProfileRotated {
					continue // e.g. account.created from earlier in this test
				}
				if ev.Data["profile_id"] != profID || ev.Data["revision"] != 3 {
					t.Fatalf("event: %+v", ev)
				}
				gotEvent = true
			default:
				break drain
			}
		}
		if !gotEvent {
			t.Fatal("no obfuscation.profile_rotated event emitted")
		}
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'obfuscation.profile_rotated' AND target = $1`, pName).Scan(&n); err != nil || n != 1 {
			t.Fatalf("audit entries: %d, %v", n, err)
		}

		// Until the agent reports the new revision, configs fall back to plain.
		if cfg := cfgOf(accID, node.ID); strings.Contains(cfg, "Jc = ") {
			t.Fatalf("rotated profile not applied yet - config must be plain:\n%s", cfg)
		}
		s.recordAgentObfuscation(ctx, node.ID, &agentObfuscationReport{Mode: "awg", ProfileID: profID, Revision: 3, Supported: true})
		if cfg := cfgOf(accID, node.ID); !strings.Contains(cfg, "Jc = ") || !strings.Contains(cfg, "I1 = <r 2>") {
			t.Fatalf("applied rotation missing from config:\n%s", cfg)
		}
	})

	t.Run("group assignment and delete rules", func(t *testing.T) {
		code, raw := admin("operator", "POST", "/api/v1/obfuscation/assign-group", `{"node_group":"`+group+`","profile_id":"`+profID+`"}`)
		expect("assign group", 200, code, raw)
		if decode(raw)["nodes_updated"].(float64) != 2 {
			t.Fatalf("group assign: %s", raw)
		}
		code, raw = admin("operator", "POST", "/api/v1/obfuscation/assign-group", `{"node_group":"no-such-group-`+sfx+`","profile_id":"`+profID+`"}`)
		expect("empty group", 404, code, raw)
		code, raw = admin("operator", "DELETE", "/api/v1/obfuscation/profiles/"+profID, "")
		expect("in use", 409, code, raw)

		code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles", `{"name":"obfit-`+sfx+`-b","preset":"compatible"}`)
		expect("create compatible", 201, code, raw)
		b := decode(raw)
		if b["stock_client_compatible"] != true || b["compat"] != "awg1" {
			t.Fatalf("compatible preset: %s", raw)
		}
		// Rotating a junk-only profile keeps it junk-only (stock-compatible).
		code, raw = admin("operator", "POST", "/api/v1/obfuscation/profiles/"+b["id"].(string)+"/rotate", "")
		expect("rotate compatible", 200, code, raw)
		if decode(raw)["stock_client_compatible"] != true {
			t.Fatalf("rotated compatible profile lost its shape: %s", raw)
		}

		code, raw = admin("operator", "POST", "/api/v1/obfuscation/assign-group", `{"node_group":"`+group+`","profile_id":null}`)
		expect("clear group", 200, code, raw)
		code, raw = admin("operator", "DELETE", "/api/v1/obfuscation/profiles/"+profID, "")
		expect("delete", 204, code, raw)
		code, raw = admin("operator", "DELETE", "/api/v1/obfuscation/profiles/"+profID, "")
		expect("delete twice", 404, code, raw)
	})

	t.Run("profiles survive a backup round trip", func(t *testing.T) {
		code, raw := admin("operator", "POST", "/api/v1/obfuscation/profiles", `{"name":"obfit-`+sfx+`-c","preset":"awg1"}`)
		expect("create", 201, code, raw)
		id := decode(raw)["id"].(string)
		code, raw = admin("operator", "PUT", "/api/v1/nodes/"+node2.ID+"/obfuscation", `{"profile_id":"`+id+`"}`)
		expect("assign", 200, code, raw)

		dump, err := st.DumpTables(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(dump["obfuscation_profiles"]), "obfit-"+sfx+"-c") {
			t.Fatalf("profile missing from the backup dump: %s", dump["obfuscation_profiles"])
		}
		if _, err := st.RestoreTables(ctx, dump); err != nil {
			t.Fatalf("restore: %v", err)
		}
		code, raw = admin("operator", "GET", "/api/v1/nodes/"+node2.ID+"/obfuscation", "")
		expect("node after restore", 200, code, raw)
		if decode(raw)["profile_id"] != id {
			t.Fatalf("assignment lost in the restore: %s", raw)
		}
	})
}

func TestInjectAWGLines(t *testing.T) {
	cfg := "[Interface]\nPrivateKey = K\nMTU = 1280\n\n[Peer]\nPublicKey = P\n"
	if got := injectAWGLines(cfg, ""); got != cfg {
		t.Fatalf("empty block must not change the config: %q", got)
	}
	want := "[Interface]\nPrivateKey = K\nMTU = 1280\nJc = 5\nS1 = 9\n\n[Peer]\nPublicKey = P\n"
	if got := injectAWGLines(cfg, "Jc = 5\nS1 = 9\n"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := injectAWGLines("no peer section", "Jc = 5\n"); got != "no peer section" {
		t.Fatalf("a config without [Peer] is left alone: %q", got)
	}
}

func TestObfuscationEventRendering(t *testing.T) {
	ev := events.Event{Type: events.ObfuscationProfileRotated, Data: map[string]any{"profile_name": "main", "revision": 4}}
	if r := renderEvent(ev, "en"); !strings.Contains(r.Body, `"main"`) || !strings.Contains(r.Body, "revision 4") {
		t.Fatalf("en: %+v", r)
	}
	if r := renderEvent(ev, "fa"); !strings.Contains(r.Body, "main") || r.Title == "" || r.Title == ev.Type {
		t.Fatalf("fa: %+v", r)
	}
	if m, ok := events.Lookup(events.ObfuscationProfileRotated); !ok || m.Severity != events.SeverityWarning || !m.InPanel {
		t.Fatalf("catalog entry: %+v", m)
	}
}
