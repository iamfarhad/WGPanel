package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// TestAbuseControlsEndToEnd drives the abuse features through the real router and the
// background entry points: notes/tags (create, patch, list filter, bulk), the settings
// endpoints and their RBAC, heartbeat torrent-hit ingestion -> auto-suspend -> events,
// the auto-cleanup sweep (event + audit row) and the sharing sweep with the badge detail.
func TestAbuseControlsEndToEnd(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run integration tests")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(256, nil)
	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Events: bus,
	}
	h := s.Routes()

	orig, err := st.GetAbuseSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	group := "ab-" + sfx
	pub := "ak-" + sfx
	node, err := st.CreateNode(ctx, "ab-"+sfx, group, "eu", "a.test:51820", "10.79.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID)

	res, err := st.CreateReseller(ctx, store.NewResellerParams{Username: "abres" + sfx, PasswordHash: "h", NodeGroups: []string{group}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateReseller(ctx, store.NewResellerParams{Username: "abother" + sfx, PasswordHash: "h", NodeGroups: []string{group}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		bg := context.Background()
		_, _ = c.Exec(bg, `UPDATE nodes SET status = 'pending' WHERE id = $1`, node.ID)
		_, _ = c.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = ANY($1)`, []string{store.ResellerNamespace(res.AdminID), store.ResellerNamespace(other.AdminID)})
		_, _ = c.Exec(bg, `DELETE FROM accounts WHERE label LIKE $1`, "%"+sfx)
		_, _ = c.Exec(bg, `DELETE FROM node_abuse_overrides WHERE node_id = $1`, node.ID)
		_, _ = c.Exec(bg, `DELETE FROM event_state WHERE key LIKE 'abuse:%'`)
		_, _ = st.UpdateAbuseSettings(bg, store.UpdateAbuseSettingsParams{
			AutoCleanupEnabled: &orig.AutoCleanupEnabled, AutoCleanupDays: &orig.AutoCleanupDays,
			DeviceLimitMode: &orig.DeviceLimitMode, DeviceWindowMinutes: &orig.DeviceWindowMinutes,
			SharingDetectionEnabled: &orig.SharingDetectionEnabled, SharingMinNetworks: &orig.SharingMinNetworks,
			SharingWindowMinutes: &orig.SharingWindowMinutes, TorrentBlockEnabled: &orig.TorrentBlockEnabled,
			TorrentSuspendEnabled: &orig.TorrentSuspendEnabled, TorrentSuspendHits: &orig.TorrentSuspendHits,
			TorrentSuspendWindowMinutes: &orig.TorrentSuspendWindowMinutes,
		})
	})

	tok := func(role, id string) string {
		v, _ := authcrypto.IssueAccessToken(gateTestSecret, id, "u", role)
		return v
	}
	resTok, otherTok := tok("reseller", res.AdminID), tok("reseller", other.AdminID)
	staff, super, support := tok("operator", "op"), tok("super_admin", "sa"), tok("support", "sp")
	call := func(token, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	drain := func() []events.Event {
		var out []events.Event
		for {
			select {
			case ev := <-bus.C():
				out = append(out, ev)
			default:
				return out
			}
		}
	}
	tagsOf := func(m map[string]any) string {
		var parts []string
		for _, v := range m["tags"].([]any) {
			parts = append(parts, v.(string))
		}
		return strings.Join(parts, ",")
	}
	listIDs := func(token, query string) map[string]bool {
		t.Helper()
		code, out := call(token, "GET", "/api/v1/accounts?limit=200"+query, "")
		if code != 200 {
			t.Fatalf("list %s: %d %v", query, code, out)
		}
		ids := map[string]bool{}
		for _, a := range out["accounts"].([]any) {
			ids[a.(map[string]any)["id"].(string)] = true
		}
		return ids
	}

	// --- notes and tags ---
	code, a := call(resTok, "POST", "/api/v1/accounts", `{"label":"tag-a-`+sfx+`","note":"paid by card, friend of Ali","tags":["VIP"," Trial User ","vip"]}`)
	if code != 201 || tagsOf(a) != "trial-user,vip" || a["note"] != "paid by card, friend of Ali" {
		t.Fatalf("create with meta: %d %v", code, a)
	}
	aID := a["id"].(string)
	code, b := call(resTok, "POST", "/api/v1/accounts", `{"label":"tag-b-`+sfx+`","tags":["vip"]}`)
	if code != 201 || b["note"] != "" {
		t.Fatalf("create b: %d %v", code, b)
	}
	bID := b["id"].(string)
	if code, out := call(resTok, "POST", "/api/v1/accounts", `{"label":"bad-`+sfx+`","tags":["no good!"]}`); code != 400 {
		t.Fatalf("bad tag accepted: %d %v", code, out)
	}
	if got := listIDs(resTok, "&tag=VIP"); !got[aID] || !got[bID] {
		t.Fatalf("tag filter: %v", got)
	}
	if got := listIDs(resTok, "&tag=vip&tag=trial-user"); !got[aID] || got[bID] {
		t.Fatalf("repeatable tag filter is AND: %v", got)
	}
	if got := listIDs(resTok, "&q=friend+of+ali"); !got[aID] || got[bID] {
		t.Fatalf("search must cover the note: %v", got)
	}
	if got := listIDs(otherTok, "&tag=vip"); got[aID] {
		t.Fatalf("tag filter leaked across resellers: %v", got)
	}
	if code, _ := call(resTok, "GET", "/api/v1/accounts?tag=bad%21", ""); code != 400 {
		t.Fatalf("bad ?tag= accepted: %d", code)
	}
	code, out := call(resTok, "GET", "/api/v1/accounts?cursor=&tag=trial-user", "")
	if code != 200 || len(out["accounts"].([]any)) != 1 {
		t.Fatalf("cursor mode with tag: %d %v", code, out)
	}

	code, p := call(resTok, "PATCH", "/api/v1/accounts/"+aID, `{"tags":["Gold"],"note":""}`)
	if code != 200 || tagsOf(p) != "gold" || p["note"] != "" {
		t.Fatalf("patch meta: %d %v", code, p)
	}
	code, p = call(resTok, "PATCH", "/api/v1/accounts/"+aID, `{"label":"tag-a2-`+sfx+`"}`)
	if code != 200 || tagsOf(p) != "gold" {
		t.Fatalf("patch without meta must keep tags: %d %v", code, p)
	}
	if code, _ := call(otherTok, "PATCH", "/api/v1/accounts/"+aID, `{"tags":["x"]}`); code != 404 {
		t.Fatalf("foreign patch: %d", code)
	}

	code, bulk := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"add_tags","ids":["`+aID+`","`+bID+`"],"tags":["Promo","gold"]}`)
	if code != 200 || bulk["succeeded"].(float64) != 2 {
		t.Fatalf("bulk add_tags: %d %v", code, bulk)
	}
	items := bulk["results"].([]any)
	if tagsOf(items[0].(map[string]any)["account"].(map[string]any)) != "gold,promo" ||
		tagsOf(items[1].(map[string]any)["account"].(map[string]any)) != "gold,promo,vip" {
		t.Fatalf("bulk add result: %v", items)
	}
	code, bulk = call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"remove_tags","ids":["`+bID+`"],"tags":["gold","VIP","nonexistent"]}`)
	if code != 200 || tagsOf(bulk["results"].([]any)[0].(map[string]any)["account"].(map[string]any)) != "promo" {
		t.Fatalf("bulk remove_tags: %d %v", code, bulk)
	}
	code, bulk = call(otherTok, "POST", "/api/v1/accounts/bulk", `{"action":"add_tags","ids":["`+aID+`"],"tags":["steal"]}`)
	if code != 200 || bulk["failed"].(float64) != 1 {
		t.Fatalf("foreign bulk tag should fail per item: %d %v", code, bulk)
	}
	if code, _ := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"add_tags","ids":["`+aID+`"],"tags":[]}`); code != 400 {
		t.Fatalf("bulk add_tags without tags: %d", code)
	}
	code, bulk = call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"create","count":2,"label_prefix":"bt-`+sfx+`","tags":["Batch"],"note":"bulk note"}`)
	if code != 200 || bulk["succeeded"].(float64) != 2 {
		t.Fatalf("bulk create with tags: %d %v", code, bulk)
	}
	if acc := bulk["results"].([]any)[0].(map[string]any)["account"].(map[string]any); tagsOf(acc) != "batch" || acc["note"] != "bulk note" {
		t.Fatalf("bulk create meta: %v", acc)
	}

	// --- settings and RBAC ---
	if code, _ := call(resTok, "GET", "/api/v1/abuse-settings", ""); code != 403 {
		t.Fatalf("reseller read settings: %d", code)
	}
	if code, out := call(support, "GET", "/api/v1/abuse-settings", ""); code != 200 || out["auto_cleanup_enabled"] != false || out["device_limit_mode"] != "endpoint" {
		t.Fatalf("defaults: %d %v", code, out)
	}
	if code, _ := call(staff, "PATCH", "/api/v1/abuse-settings", `{"auto_cleanup_enabled":true}`); code != 403 {
		t.Fatalf("operator may not edit: %d", code)
	}
	for _, bad := range []string{`{"auto_cleanup_days":0}`, `{"device_limit_mode":"mac"}`, `{"sharing_min_networks":1}`, `{"torrent_suspend_hits":0}`, `{"node_overrides":{"nope":true}}`} {
		if code, _ := call(super, "PATCH", "/api/v1/abuse-settings", bad); code != 400 {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}
	code, cfg := call(super, "PATCH", "/api/v1/abuse-settings",
		`{"torrent_suspend_enabled":true,"torrent_suspend_hits":5,"torrent_suspend_window_minutes":30,"node_overrides":{"`+node.ID+`":true}}`)
	if code != 200 || cfg["torrent_suspend_hits"].(float64) != 5 {
		t.Fatalf("update settings: %d %v", code, cfg)
	}
	var nodeRow map[string]any
	for _, n := range cfg["nodes"].([]any) {
		if n.(map[string]any)["id"] == node.ID {
			nodeRow = n.(map[string]any)
		}
	}
	if nodeRow == nil || nodeRow["torrent_block"] != true || nodeRow["torrent_block_effective"] != true {
		t.Fatalf("node override: %v", nodeRow)
	}
	var auditN int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'settings.abuse_updated' AND created_at > now() - interval '1 minute'`).Scan(&auditN); err != nil || auditN < 1 {
		t.Fatalf("settings change not audited: %d %v", auditN, err)
	}

	// --- heartbeat torrent hits -> events -> auto-suspend ---
	peers, err := st.ListAccountPeersWithNode(ctx, bID, nil)
	if err != nil || len(peers) != 1 {
		t.Fatalf("peers: %v %v", peers, err)
	}
	ip := peers[0].AssignedIP
	drain()
	s.ingestTorrentHits(ctx, node.ID, []agentTorrentHit{{IP: ip, Hits: 3}, {IP: "not-an-ip", Hits: 9}, {IP: ip, Hits: -4}})
	evs := drain()
	if len(evs) != 1 || evs[0].Type != events.AccountTorrentDetected || evs[0].Namespace != store.ResellerNamespace(res.AdminID) || evs[0].Data["suspended"] != false {
		t.Fatalf("first report: %+v", evs)
	}
	_, got := call(resTok, "GET", "/api/v1/accounts/"+bID, "")
	if got["status"] != "active" || got["torrent_hits_24h"].(float64) != 3 {
		t.Fatalf("after 3 hits: %v", got)
	}
	s.ingestTorrentHits(ctx, node.ID, []agentTorrentHit{{IP: ip, Hits: 2}})
	evs = drain()
	types := map[string]bool{}
	for _, e := range evs {
		types[e.Type] = true
	}
	if !types[events.AccountTorrentDetected] || !types[events.AccountSuspended] {
		t.Fatalf("crossing the threshold should announce detection and suspension: %+v", evs)
	}
	_, got = call(resTok, "GET", "/api/v1/accounts/"+bID, "")
	if got["status"] != "suspended" || got["suspend_reason"] != "torrent_abuse" {
		t.Fatalf("not suspended: %v", got)
	}
	code, dev := call(resTok, "GET", "/api/v1/accounts/"+bID+"/devices", "")
	tor, _ := dev["torrent"].(map[string]any)
	if code != 200 || tor == nil || tor["hits_24h"].(float64) != 5 || len(tor["recent"].([]any)) == 0 || tor["blocking_enabled"] == nil {
		t.Fatalf("devices torrent detail: %d %v", code, dev)
	}

	// --- auto cleanup sweep ---
	code, c1 := call(staff, "POST", "/api/v1/accounts", `{"label":"clean-exp-`+sfx+`","node_id":"`+node.ID+`"}`)
	code2, c2 := call(staff, "POST", "/api/v1/accounts", `{"label":"clean-manual-`+sfx+`","node_id":"`+node.ID+`"}`)
	if code != 201 || code2 != 201 {
		t.Fatalf("create cleanup accounts: %d %d", code, code2)
	}
	c1ID, c2ID := c1["id"].(string), c2["id"].(string)
	exec(`UPDATE accounts SET status='suspended', suspend_reason='expired' WHERE id = $1`, c1ID)
	exec(`UPDATE accounts SET status='suspended', suspend_reason='manual' WHERE id = $1`, c2ID)
	exec(`UPDATE accounts SET suspended_at = now() - interval '45 days' WHERE id = ANY($1)`, []string{c1ID, c2ID})
	drain()

	s.runAbuseSweep(ctx) // feature off by default: nothing happens
	if _, g := call(staff, "GET", "/api/v1/accounts/"+c1ID, ""); g["status"] != "suspended" {
		t.Fatalf("auto-cleanup ran while disabled: %v", g)
	}
	if code, _ := call(super, "PATCH", "/api/v1/abuse-settings", `{"auto_cleanup_enabled":true,"auto_cleanup_days":30}`); code != 200 {
		t.Fatal("enable cleanup")
	}
	s.runAbuseSweep(ctx)
	if _, g := call(staff, "GET", "/api/v1/accounts/"+c1ID, ""); g["status"] != "deleted" {
		t.Fatalf("expired account not cleaned up: %v", g)
	}
	if _, g := call(staff, "GET", "/api/v1/accounts/"+c2ID, ""); g["status"] != "suspended" {
		t.Fatalf("manual suspension must survive cleanup: %v", g)
	}
	var del *events.Event
	for _, e := range drain() {
		if e.Type == events.AccountDeleted && e.Data["account_id"] == c1ID {
			e := e
			del = &e
		}
	}
	if del == nil || del.Data["reason"] != "auto_cleanup" {
		t.Fatalf("account.deleted with reason auto_cleanup not emitted: %+v", del)
	}
	var detail string
	if err := conn.QueryRow(ctx, `SELECT actor || ' ' || COALESCE(detail->>'reason','') FROM audit_log WHERE target = $1 AND action = 'account.deleted'`, c1ID).Scan(&detail); err != nil || detail != "system auto_cleanup" {
		t.Fatalf("audit row: %q %v", detail, err)
	}
	call(super, "PATCH", "/api/v1/abuse-settings", `{"auto_cleanup_enabled":false}`)

	// --- sharing sweep and badge detail ---
	code, sh := call(staff, "POST", "/api/v1/accounts", `{"label":"share-`+sfx+`","node_id":"`+node.ID+`"}`)
	if code != 201 {
		t.Fatalf("create sharing account: %d", code)
	}
	shID := sh["id"].(string)
	for _, ep := range []string{"203.0.113.5:1", "198.51.100.8:1", "192.0.2.77:1", "192.0.2.78:1"} {
		exec(`INSERT INTO account_devices (account_id, source_endpoint, node_id) VALUES ($1, $2, $3)`, shID, ep, node.ID)
	}
	drain()
	s.runAbuseSweep(ctx) // detection off by default
	if _, g := call(staff, "GET", "/api/v1/accounts/"+shID, ""); g["possibly_shared"] != false {
		t.Fatalf("flagged while detection is off: %v", g)
	}
	call(super, "PATCH", "/api/v1/abuse-settings", `{"sharing_detection_enabled":true,"sharing_min_networks":3,"sharing_window_minutes":10}`)
	s.runAbuseSweep(ctx)
	_, g := call(staff, "GET", "/api/v1/accounts/"+shID, "")
	if g["possibly_shared"] != true {
		t.Fatalf("3 networks should flag: %v", g)
	}
	var shared *events.Event
	for _, e := range drain() {
		if e.Type == events.AccountSharingSuspected && e.Data["account_id"] == shID {
			e := e
			shared = &e
		}
	}
	if shared == nil || fmt.Sprint(shared.Data["networks"]) != "3" {
		t.Fatalf("account.sharing_suspected not emitted: %+v", shared)
	}
	_, dev = call(staff, "GET", "/api/v1/accounts/"+shID+"/devices", "")
	sharing, _ := dev["sharing"].(map[string]any)
	if sharing == nil || sharing["suspected"] != true || len(sharing["networks"].([]any)) != 3 {
		t.Fatalf("badge detail: %v", dev["sharing"])
	}
	first := sharing["networks"].([]any)[0].(map[string]any)
	if first["network"] == nil || len(first["ips"].([]any)) == 0 || len(first["nodes"].([]any)) == 0 || first["last_seen_at"] == nil {
		t.Fatalf("network detail incomplete: %v", first)
	}
	// Switching detection off removes the badge.
	call(super, "PATCH", "/api/v1/abuse-settings", `{"sharing_detection_enabled":false}`)
	s.runAbuseSweep(ctx)
	if _, g := call(staff, "GET", "/api/v1/accounts/"+shID, ""); g["possibly_shared"] != false {
		t.Fatalf("badge should clear when detection is switched off: %v", g)
	}

	// --- agent heartbeat response carries the torrent flag (see handleAgentHeartbeat) ---
	if on, err := st.EffectiveTorrentBlock(ctx, node.ID); err != nil || !on {
		t.Fatalf("node override should be effective: %v %v", on, err)
	}
}
