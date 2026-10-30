package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/store"
)

// TestMarzbanCompatEndToEnd drives the real router against Postgres the way the Mirza
// panel bot drives a Marzban panel (github.com/mahdiMGF2/botmirzapanel, marzban.php):
// token, create, get, modify (renew + add traffic), reset, revoke_sub, disable/enable,
// delete - then contract checks for every other endpoint, the Marzban error shapes and
// the scoping of the issued token. Skipped without WGPANEL_TEST_POSTGRES_DSN.
func TestMarzbanCompatEndToEnd(t *testing.T) {
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
		BootPanelDomain: "panel.example.com",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Routes()

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	group := "mz-" + sfx
	pub := "pk-mz-" + sfx
	node, err := st.CreateNode(ctx, "mz-"+sfx, group, "eu", "mz.test:51820", "10.69.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}
	var namespaces []string
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = ANY($1)`, namespaces)
		_, _ = conn.Exec(bg, `DELETE FROM api_keys WHERE key_id = ANY($1)`, namespaces)
		_, _ = conn.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID)
		_ = st.SetMarzbanCompatEnabled(bg, false)
	})

	type apiKey struct{ id, secret string }
	mkKey := func(name string, perms []string) apiKey {
		t.Helper()
		k := apiKey{id: name + "-" + sfx, secret: "secret-" + name + "-" + sfx}
		if _, err := st.CreateAPIKey(ctx, k.id, mustEnc(t, k.secret), name, []string{group}, perms, nil); err != nil {
			t.Fatal(err)
		}
		namespaces = append(namespaces, k.id)
		return k
	}
	allPerms := []string{"read", "create", "update", "suspend", "delete"}
	bot := mkKey("bot", allPerms)
	other := mkKey("other", allPerms)
	reader := mkKey("reader", []string{"read"})
	noSuspend := mkKey("nosusp", []string{"read", "update"})

	serve := func(req *http.Request) (int, string, map[string]any) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, rr.Body.String(), out
	}
	login := func(user, pass string) (int, string, map[string]any) {
		form := url.Values{"username": {user}, "password": {pass}}
		req := httptest.NewRequest("POST", "/api/admin/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return serve(req)
	}
	tokenOf := func(k apiKey) string {
		t.Helper()
		code, raw, out := login(k.id, k.secret)
		if code != 200 {
			t.Fatalf("login %s: %d %s", k.id, code, raw)
		}
		if out["token_type"] != "bearer" {
			t.Fatalf("token_type = %v", out["token_type"])
		}
		return out["access_token"].(string)
	}
	call := func(token, method, path, body string) (int, string, map[string]any) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return serve(req)
	}
	want := func(what string, wantCode, code int, raw string) {
		t.Helper()
		if code != wantCode {
			t.Fatalf("%s: status %d, want %d: %s", what, code, wantCode, raw)
		}
	}
	detailOf := func(raw string) string {
		var d struct {
			Detail any `json:"detail"`
		}
		_ = json.Unmarshal([]byte(raw), &d)
		if s, ok := d.Detail.(string); ok {
			return s
		}
		return fmt.Sprint(d.Detail)
	}
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	num := func(m map[string]any, k string) int64 {
		t.Helper()
		f, ok := m[k].(float64)
		if !ok {
			t.Fatalf("%s is not a number in %v", k, m)
		}
		return int64(f)
	}

	// ---- the switch: off by default, every route is a plain 404 ----
	code, raw, _ := login(bot.id, bot.secret)
	want("login while disabled", 404, code, raw)
	if detailOf(raw) != "Not Found" {
		t.Fatalf("disabled answer: %s", raw)
	}
	code, raw, _ = call("whatever", "GET", "/api/system", "")
	want("system while disabled", 404, code, raw)

	adminCall := func(role, method, path, body string) (int, string, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken(t, role))
		return serve(req)
	}
	code, raw, out := adminCall("super_admin", "GET", "/api/v1/marzban-compat", "")
	want("get switch", 200, code, raw)
	if out["enabled"] != false {
		t.Fatalf("switch should default to off: %s", raw)
	}
	code, raw, _ = adminCall("operator", "PUT", "/api/v1/marzban-compat", `{"enabled":true}`)
	want("operator may not flip the switch", 403, code, raw)
	code, raw, _ = adminCall("super_admin", "PUT", "/api/v1/marzban-compat", `{"enabled":"yes"}`)
	want("bad switch body", 400, code, raw)
	code, raw, out = adminCall("super_admin", "PUT", "/api/v1/marzban-compat", `{"enabled":true}`)
	want("enable switch", 200, code, raw)
	if out["enabled"] != true {
		t.Fatalf("enable: %s", raw)
	}

	// ---- 1. token ----
	code, raw, _ = login(bot.id, "wrong-secret")
	want("wrong password", 401, code, raw)
	if detailOf(raw) != "Incorrect username or password" {
		t.Fatalf("wrong password detail: %s", raw)
	}
	code, raw, _ = login("no-such-key", "x")
	want("unknown key", 401, code, raw)
	if detailOf(raw) != "Incorrect username or password" {
		t.Fatalf("unknown key must look like a wrong password: %s", raw)
	}
	code, raw, _ = login("", "")
	want("empty credentials", 422, code, raw)
	var v422 struct {
		Detail []struct {
			Loc  []string `json:"loc"`
			Msg  string   `json:"msg"`
			Type string   `json:"type"`
		} `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &v422); err != nil || len(v422.Detail) != 2 || v422.Detail[0].Loc[1] != "username" || v422.Detail[0].Msg != "field required" {
		t.Fatalf("422 shape: %s", raw)
	}
	botTok := tokenOf(bot)

	code, raw, _ = call("", "GET", "/api/admin", "")
	want("no bearer", 401, code, raw)
	code, raw, _ = call("garbage", "GET", "/api/admin", "")
	want("garbage bearer", 401, code, raw)
	code, raw, out = call(botTok, "GET", "/api/admin", "")
	want("current admin", 200, code, raw)
	if out["username"] != bot.id || out["is_sudo"] != false {
		t.Fatalf("admin: %s", raw)
	}
	// An admin panel JWT is not a Marzban token.
	code, raw, _ = call(adminToken(t, "super_admin"), "GET", "/api/system", "")
	want("admin JWT on the facade", 401, code, raw)

	// ---- 2. create (exactly what marzban.php adduser sends; proxies/inbounds ignored) ----
	username := "mirza" + sfx[len(sfx)-8:]
	expire := time.Now().Add(30 * 24 * time.Hour).Unix()
	const gib = int64(1) << 30
	body := fmt.Sprintf(`{"proxies":{"vless":{"flow":""}},"data_limit":%d,"username":%q,"inbounds":{"vless":["VLESS TCP REALITY"]},"expire":%d}`, 5*gib, username, expire)
	code, raw, user := call(botTok, "POST", "/api/user", body)
	want("create", 200, code, raw)
	for _, k := range []string{"username", "status", "expire", "data_limit", "used_traffic", "lifetime_used_traffic", "created_at", "online_at", "links",
		"subscription_url", "proxies", "inbounds", "note", "data_limit_reset_strategy", "on_hold_expire_duration", "on_hold_timeout", "sub_updated_at", "excluded_inbounds"} {
		if _, ok := user[k]; !ok {
			t.Errorf("create response lacks %q: %s", k, raw)
		}
	}
	if user["username"] != username || user["status"] != "active" || num(user, "expire") != expire || num(user, "data_limit") != 5*gib {
		t.Fatalf("create response: %s", raw)
	}
	if user["data_limit_reset_strategy"] != "no_reset" || user["online_at"] != nil {
		t.Fatalf("create defaults: %s", raw)
	}
	subURL := user["subscription_url"].(string)
	if !strings.HasPrefix(subURL, "https://panel.example.com/s/") {
		t.Fatalf("subscription_url = %q, want the panel's /s/ page on its public origin", subURL)
	}
	if !mzBotURLRE.MatchString(subURL) {
		t.Fatalf("subscription_url %q would be rewritten by the Mirza bot", subURL)
	}
	links, _ := user["links"].([]any)
	if len(links) != 1 || !strings.Contains(links[0].(string), "/api/v1/sub/") || !strings.Contains(links[0].(string), "node_id="+node.ID) {
		t.Fatalf("links should be one per-node config URL: %s", raw)
	}
	// The account is a normal account: external_ref = label = username, owned by the key.
	var ref, label, owner string
	if err := conn.QueryRow(ctx, `SELECT external_ref, label, owner_key_namespace FROM accounts WHERE external_ref = $1`, username).Scan(&ref, &label, &owner); err != nil {
		t.Fatal(err)
	}
	if ref != username || label != username || owner != bot.id {
		t.Fatalf("stored account: ref=%q label=%q owner=%q", ref, label, owner)
	}

	code, raw, _ = call(botTok, "POST", "/api/user", body)
	want("duplicate create", 409, code, raw)
	if detailOf(raw) != "User already exists" {
		t.Fatalf("duplicate detail: %s", raw)
	}

	// ---- 3. get ----
	code, raw, user = call(botTok, "GET", "/api/user/"+username, "")
	want("get", 200, code, raw)
	if user["username"] != username || user["subscription_url"] != subURL {
		t.Fatalf("get: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/user/nobody"+sfx[len(sfx)-6:], "")
	want("get unknown", 404, code, raw)
	if detailOf(raw) != "User not found" {
		t.Fatalf("404 detail: %s", raw)
	}

	// ---- 4. modify: renew + add traffic, after the bot's usage reset ----
	sql(`UPDATE accounts SET data_used_bytes = $2 WHERE external_ref = $1`, username, 3*gib)
	code, raw, user = call(botTok, "POST", "/api/user/"+username+"/reset", "")
	want("reset", 200, code, raw)
	if num(user, "used_traffic") != 0 || user["status"] != "active" {
		t.Fatalf("reset: %s", raw)
	}
	newExpire := time.Now().Add(60 * 24 * time.Hour).Unix()
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, fmt.Sprintf(`{"expire":%d,"data_limit":%d}`, newExpire, 10*gib))
	want("modify", 200, code, raw)
	if num(user, "expire") != newExpire || num(user, "data_limit") != 10*gib {
		t.Fatalf("modify: %s", raw)
	}
	// Extra traffic only (the bot's "buy more volume" call).
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, fmt.Sprintf(`{"data_limit":%d}`, 12*gib))
	want("modify data_limit only", 200, code, raw)
	if num(user, "expire") != newExpire || num(user, "data_limit") != 12*gib {
		t.Fatalf("data_limit only must leave expire alone: %s", raw)
	}
	// Renewing an EXPIRED account revives it (the commonest real renewal).
	sql(`UPDATE accounts SET expiry_at = now() - interval '1 day' WHERE external_ref = $1`, username)
	code, raw, user = call(botTok, "GET", "/api/user/"+username, "")
	want("get expired", 200, code, raw)
	if user["status"] != "expired" {
		t.Fatalf("an account past its expiry must read expired: %s", raw)
	}
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, fmt.Sprintf(`{"expire":%d,"data_limit":%d}`, newExpire, 12*gib))
	want("renew expired", 200, code, raw)
	if user["status"] != "active" {
		t.Fatalf("renewing an expired account must reactivate it: %s", raw)
	}
	// A limited account comes back when the cap is raised above its usage.
	sql(`UPDATE accounts SET data_used_bytes = $2 WHERE external_ref = $1`, username, 12*gib)
	code, raw, user = call(botTok, "GET", "/api/user/"+username, "")
	want("get limited", 200, code, raw)
	if user["status"] != "limited" {
		t.Fatalf("an account at its cap must read limited: %s", raw)
	}
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, fmt.Sprintf(`{"data_limit":%d}`, 20*gib))
	want("raise cap", 200, code, raw)
	if user["status"] != "active" {
		t.Fatalf("raising the cap must lift the limit: %s", raw)
	}
	// expire 0 / data_limit 0 = unlimited.
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, `{"expire":0,"data_limit":0}`)
	want("make unlimited", 200, code, raw)
	if num(user, "expire") != 0 || num(user, "data_limit") != 0 || user["status"] != "active" {
		t.Fatalf("unlimited: %s", raw)
	}
	// reset strategy round trip
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, `{"data_limit_reset_strategy":"month","note":"vip","proxies":{}}`)
	want("reset strategy", 200, code, raw)
	if user["data_limit_reset_strategy"] != "month" {
		t.Fatalf("reset strategy: %s", raw)
	}
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, `{"data_limit_reset_strategy":"no_reset"}`)
	want("reset strategy off", 200, code, raw)
	if user["data_limit_reset_strategy"] != "no_reset" {
		t.Fatalf("reset strategy off: %s", raw)
	}
	// validation -> 422 detail list
	for name, bad := range map[string]string{
		"expire":                    `{"expire":-5}`,
		"data_limit":                `{"data_limit":"lots"}`,
		"data_limit_reset_strategy": `{"data_limit_reset_strategy":"hourly"}`,
		"status":                    `{"status":"expired"}`,
	} {
		code, raw, _ = call(botTok, "PUT", "/api/user/"+username, bad)
		want("422 for "+name, 422, code, raw)
		if err := json.Unmarshal([]byte(raw), &v422); err != nil || len(v422.Detail) == 0 || v422.Detail[0].Loc[0] != "body" || v422.Detail[0].Loc[1] != name {
			t.Fatalf("422 for %s: %s", name, raw)
		}
	}
	code, raw, _ = call(botTok, "PUT", "/api/user/"+username, `{not json`)
	want("malformed JSON", 422, code, raw)
	code, raw, _ = call(botTok, "PUT", "/api/user/ghost"+sfx[len(sfx)-6:], `{"expire":0}`)
	want("modify unknown", 404, code, raw)

	// ---- 5/6. reset + revoke_sub ----
	oldSub := user["subscription_url"].(string)
	code, raw, user = call(botTok, "POST", "/api/user/"+username+"/revoke_sub", "")
	want("revoke_sub", 200, code, raw)
	if user["subscription_url"] == oldSub || !strings.HasPrefix(user["subscription_url"].(string), "https://panel.example.com/s/") {
		t.Fatalf("revoke_sub must mint a new subscription URL: %s", raw)
	}
	var oldTokenHits int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE subscription_token = $1`, oldSub[strings.LastIndex(oldSub, "/")+1:]).Scan(&oldTokenHits)
	if oldTokenHits != 0 {
		t.Fatal("the old subscription token still resolves")
	}
	code, raw, _ = call(botTok, "POST", "/api/user/nobody"+sfx[len(sfx)-6:]+"/revoke_sub", "")
	want("revoke unknown", 404, code, raw)

	// ---- 7. disable / enable ----
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, `{"status":"disabled"}`)
	want("disable", 200, code, raw)
	if user["status"] != "disabled" {
		t.Fatalf("disable: %s", raw)
	}
	code, raw, user = call(botTok, "PUT", "/api/user/"+username, `{"status":"active"}`)
	want("enable", 200, code, raw)
	if user["status"] != "active" {
		t.Fatalf("enable: %s", raw)
	}
	// Enabling an account that has used its whole quota is refused, as natively.
	sql(`UPDATE accounts SET data_quota_bytes = 1000, data_used_bytes = 1000, status = 'suspended', suspend_reason = 'manual' WHERE external_ref = $1`, username)
	code, raw, _ = call(botTok, "PUT", "/api/user/"+username, `{"status":"active"}`)
	want("enable over cap", 409, code, raw)
	sql(`UPDATE accounts SET data_quota_bytes = NULL, data_used_bytes = 0, status = 'active', suspend_reason = NULL WHERE external_ref = $1`, username)

	// ---- usage, listing, system, inbounds ----
	code, raw, out = call(botTok, "GET", "/api/user/"+username+"/usage", "")
	want("usage", 200, code, raw)
	if usages, _ := out["usages"].([]any); out["username"] != username || len(usages) != 1 {
		t.Fatalf("usage: %s", raw)
	}
	second := "second" + sfx[len(sfx)-6:]
	code, raw, _ = call(botTok, "POST", "/api/user", fmt.Sprintf(`{"username":%q,"proxies":{},"data_limit":0,"expire":0}`, second))
	want("create second", 200, code, raw)
	code, raw, out = call(botTok, "GET", "/api/users", "")
	want("list", 200, code, raw)
	if num(out, "total") != 2 || len(out["users"].([]any)) != 2 {
		t.Fatalf("list: %s", raw)
	}
	code, raw, out = call(botTok, "GET", "/api/users?limit=1&offset=1&sort=-username", "")
	want("list page", 200, code, raw)
	users := out["users"].([]any)
	if num(out, "total") != 2 || len(users) != 1 || users[0].(map[string]any)["username"] != min(username, second) {
		t.Fatalf("list page (sorted desc, offset 1) should hold the smaller username: %s", raw)
	}
	code, raw, out = call(botTok, "GET", "/api/users?search="+second[:8], "")
	want("search", 200, code, raw)
	if num(out, "total") != 1 {
		t.Fatalf("search: %s", raw)
	}
	code, raw, out = call(botTok, "GET", "/api/users?username="+username+"&username="+second, "")
	want("username filter", 200, code, raw)
	if num(out, "total") != 2 {
		t.Fatalf("username filter: %s", raw)
	}
	code, raw, out = call(botTok, "GET", "/api/users?status=disabled", "")
	want("status filter", 200, code, raw)
	if num(out, "total") != 0 {
		t.Fatalf("status filter: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/users?status=banana", "")
	want("bad status filter", 422, code, raw)
	code, raw, out = call(botTok, "GET", "/api/system", "")
	want("system", 200, code, raw)
	if out["version"] != mzReportedVersion || num(out, "total_user") != 2 || num(out, "users_active") != 2 {
		t.Fatalf("system: %s", raw)
	}
	code, raw, out = call(botTok, "GET", "/api/inbounds", "")
	want("inbounds", 200, code, raw)
	if wg, _ := out["wireguard"].([]any); len(wg) != 1 || wg[0].(map[string]any)["tag"] != "wireguard" {
		t.Fatalf("inbounds: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/user-templates", "")
	want("user-templates", 200, code, raw)
	code, raw, _ = call(botTok, "POST", "/api/user/"+username+"/active-next", "")
	want("active-next", 404, code, raw)

	// ---- on_hold create + modify ----
	hold := "hold" + sfx[len(sfx)-6:]
	code, raw, user = call(botTok, "POST", "/api/user", fmt.Sprintf(`{"username":%q,"proxies":{},"data_limit":%d,"expire":0,"status":"on_hold","on_hold_expire_duration":%d}`, hold, gib, 30*86400-2))
	want("create on_hold", 200, code, raw)
	if user["status"] != "on_hold" || num(user, "on_hold_expire_duration") != 30*86400 || num(user, "expire") != 0 {
		t.Fatalf("on_hold: %s", raw)
	}
	code, raw, user = call(botTok, "PUT", "/api/user/"+hold, `{"expire":0,"data_limit":2147483648}`)
	want("modify on_hold keeps its hold", 200, code, raw)
	if user["status"] != "on_hold" || num(user, "data_limit") != 2*gib {
		t.Fatalf("on_hold modify: %s", raw)
	}
	code, raw, _ = call(botTok, "POST", "/api/user", fmt.Sprintf(`{"username":"hold2%s","proxies":{},"status":"on_hold"}`, sfx[len(sfx)-6:]))
	want("on_hold without a duration", 422, code, raw)

	// ---- create validation ----
	for _, bad := range []string{`{"proxies":{}}`, `{"username":"ab","proxies":{}}`, `{"username":"has space","proxies":{}}`, `{"username":"validname","status":"disabled"}`} {
		code, raw, _ = call(botTok, "POST", "/api/user", bad)
		want("create "+bad, 422, code, raw)
	}

	// ---- scoping: another key sees nothing, a read-only key cannot write ----
	otherTok := tokenOf(other)
	code, raw, _ = call(otherTok, "GET", "/api/user/"+username, "")
	want("another key's user", 404, code, raw)
	code, raw, _ = call(otherTok, "DELETE", "/api/user/"+username, "")
	want("delete another key's user", 404, code, raw)
	code, raw, out = call(otherTok, "GET", "/api/users", "")
	want("another key's list", 200, code, raw)
	if num(out, "total") != 0 {
		t.Fatalf("namespace leak: %s", raw)
	}
	code, raw, _ = call(otherTok, "POST", "/api/user", body)
	want("username taken by another namespace", 409, code, raw)

	readerTok := tokenOf(reader)
	code, raw, _ = call(readerTok, "GET", "/api/users", "")
	want("reader list", 200, code, raw)
	for _, rq := range []struct{ method, path, body string }{
		{"POST", "/api/user", `{"username":"readerx1","proxies":{}}`},
		{"PUT", "/api/user/" + username, `{"expire":0}`},
		{"DELETE", "/api/user/" + username, ""},
		{"POST", "/api/user/" + username + "/reset", ""},
		{"POST", "/api/user/" + username + "/revoke_sub", ""},
		{"DELETE", "/api/users/expired", ""},
	} {
		code, raw, _ = call(readerTok, rq.method, rq.path, rq.body)
		want("reader "+rq.method+" "+rq.path, 403, code, raw)
	}
	nsUser := "nosusp" + sfx[len(sfx)-6:]
	code, raw, _ = call(botTok, "POST", "/api/user", fmt.Sprintf(`{"username":%q,"proxies":{}}`, nsUser))
	want("create for the update-only key", 200, code, raw)
	sql(`UPDATE accounts SET owner_key_namespace = $2 WHERE external_ref = $1`, nsUser, noSuspend.id)
	nsTok := tokenOf(noSuspend)
	code, raw, _ = call(nsTok, "PUT", "/api/user/"+nsUser, `{"status":"disabled"}`)
	want("status flip needs the suspend permission", 403, code, raw)
	code, raw, _ = call(nsTok, "PUT", "/api/user/"+nsUser, `{"expire":0}`)
	want("update-only key may still renew", 200, code, raw)

	// ---- expired cleanup ----
	exp := "exp" + sfx[len(sfx)-8:]
	code, raw, _ = call(botTok, "POST", "/api/user", fmt.Sprintf(`{"username":%q,"proxies":{},"expire":%d}`, exp, time.Now().Add(time.Hour).Unix()))
	want("create soon-expired", 200, code, raw)
	sql(`UPDATE accounts SET expiry_at = now() - interval '2 days' WHERE external_ref = $1`, exp)
	code, raw, _ = call(botTok, "GET", "/api/users/expired", "")
	want("list expired", 200, code, raw)
	var names []string
	_ = json.Unmarshal([]byte(raw), &names)
	if len(names) != 1 || names[0] != exp {
		t.Fatalf("expired list: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/users/expired?expired_before=2000-01-01T00:00:00", "")
	want("expired window", 200, code, raw)
	if strings.Contains(raw, exp) {
		t.Fatalf("expired_before filter ignored: %s", raw)
	}
	code, raw, _ = call(botTok, "DELETE", "/api/users/expired", "")
	want("delete expired", 200, code, raw)
	_ = json.Unmarshal([]byte(raw), &names)
	if len(names) != 1 || names[0] != exp {
		t.Fatalf("deleted expired: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/user/"+exp, "")
	want("cleaned-up user is gone", 404, code, raw)

	// ---- 8. delete ----
	code, raw, out = call(botTok, "DELETE", "/api/user/"+username, "")
	want("delete", 200, code, raw)
	if len(out) != 0 {
		t.Fatalf("delete answers {}: %s", raw)
	}
	code, raw, _ = call(botTok, "GET", "/api/user/"+username, "")
	want("get deleted", 404, code, raw)
	code, raw, _ = call(botTok, "DELETE", "/api/user/"+username, "")
	want("delete twice", 404, code, raw)

	// audit + events trail: the compat calls are attributed to the key and flagged.
	var audits int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor = $1 AND detail->>'via' = 'marzban_compat'`, "api_key:"+bot.id).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 8 {
		t.Fatalf("only %d marzban_compat audit rows for the bot key", audits)
	}

	// ---- token lifecycle: revoke / rotate / disabled switch cut access immediately ----
	sql(`UPDATE api_keys SET revoked_at = now() WHERE key_id = $1`, other.id)
	code, raw, _ = call(otherTok, "GET", "/api/admin", "")
	want("revoked key's token", 401, code, raw)
	code, raw, _ = login(other.id, other.secret)
	want("revoked key's login", 401, code, raw)

	newSecret := "rotated-" + sfx
	sql(`UPDATE api_keys SET previous_secret_encrypted = secret_encrypted, previous_secret_expires_at = now() + interval '1 hour', secret_encrypted = $2 WHERE key_id = $1`,
		bot.id, mustEnc(t, newSecret))
	code, raw, _ = call(botTok, "GET", "/api/admin", "")
	want("token minted before a rotation", 401, code, raw)
	code, raw, _ = login(bot.id, bot.secret)
	want("previous secret inside its grace window", 200, code, raw)
	code, raw, _ = login(bot.id, newSecret)
	want("new secret", 200, code, raw)
	sql(`UPDATE api_keys SET previous_secret_expires_at = now() - interval '1 minute' WHERE key_id = $1`, bot.id)
	code, raw, _ = login(bot.id, bot.secret)
	want("previous secret after its grace window", 401, code, raw)

	freshTok := func() string {
		code, raw, out := login(bot.id, newSecret)
		want("login", 200, code, raw)
		return out["access_token"].(string)
	}()
	code, raw, _ = adminCall("super_admin", "PUT", "/api/v1/marzban-compat", `{"enabled":false}`)
	want("disable switch", 200, code, raw)
	code, raw, _ = call(freshTok, "GET", "/api/admin", "")
	want("token while the switch is off", 404, code, raw)
}

// TestMarzbanCompatRouteTableNoCollision builds the whole router (a duplicate or
// ambiguous pattern panics at registration) - cheap insurance that the facade's paths
// coexist with every other package's routes.
func TestMarzbanCompatRouteTableNoCollision(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_ = s.Routes()
}
