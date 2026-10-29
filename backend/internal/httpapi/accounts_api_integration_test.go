package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

// TestAccountAPIAdditionsEndToEnd drives the real router, with HMAC-signed API keys
// like the sales bot's, against Postgres: lookup by external_ref, offset paging,
// reset-usage, the clear_* PATCH flags and last_handshake_at on every account response.
// Skipped without WGPANEL_TEST_POSTGRES_DSN. It deletes what it creates, so the other
// integration tests sharing the database still see the node set they expect.
func TestAccountAPIAdditionsEndToEnd(t *testing.T) {
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
	conn, err := pgx.Connect(ctx, dsn) // direct SQL for states the API cannot reach
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
	group := "aapi-" + sfx
	pub := "pk-aapi-" + sfx
	node, err := st.CreateNode(ctx, "aapi-"+sfx, group, "eu", "a.test:51820", "10.68.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}

	var namespaces, adminIDs []string
	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []struct{ sql, what string }{
			{`DELETE FROM accounts WHERE owner_key_namespace = ANY($1)`, "accounts"},
			{`DELETE FROM api_keys WHERE key_id = ANY($1)`, "api keys"},
		} {
			if _, err := conn.Exec(bg, q.sql, namespaces); err != nil {
				t.Logf("cleanup %s: %v", q.what, err)
			}
		}
		if _, err := conn.Exec(bg, `DELETE FROM admins WHERE id = ANY($1::uuid[])`, adminIDs); err != nil {
			t.Logf("cleanup admins: %v", err)
		}
		if _, err := conn.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID); err != nil {
			t.Logf("cleanup node: %v", err)
		}
	})

	type apiKey struct{ id, secret string }
	mkKey := func(name string, perms []string, resellerID *string) apiKey {
		t.Helper()
		k := apiKey{id: name + "-" + sfx, secret: "secret-" + name}
		if _, err := st.CreateAPIKey(ctx, k.id, mustEnc(t, k.secret), name, []string{group}, perms, resellerID); err != nil {
			t.Fatal(err)
		}
		namespaces = append(namespaces, k.id)
		return k
	}
	allPerms := []string{"read", "create", "update", "suspend", "delete"}
	bot := mkKey("bot", allPerms, nil)
	reader := mkKey("reader", []string{"read"}, nil)
	other := mkKey("other", allPerms, nil)

	// do sends one request and returns the status, the raw body and the decoded object.
	do := func(req *http.Request) (int, string, map[string]any) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, rr.Body.String(), out
	}
	signed := func(k apiKey, method, path, body string) (int, string, map[string]any) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("X-API-Key", k.id)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC(k.secret, method+"\n"+strings.SplitN(path, "?", 2)[0]+"\n"+ts+"\n"+body))
		return do(req)
	}
	bearer := func(role, method, path string) (int, string, map[string]any) {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken(t, role))
		return do(req)
	}
	must := func(what string, wantCode int, code int, raw string) {
		t.Helper()
		if code != wantCode {
			t.Fatalf("%s: status %d, want %d: %s", what, code, wantCode, raw)
		}
	}
	accountsOf := func(out map[string]any) []map[string]any {
		t.Helper()
		list, ok := out["accounts"].([]any)
		if !ok {
			t.Fatalf("response has no accounts array: %v", out)
		}
		var res []map[string]any
		for _, a := range list {
			res = append(res, a.(map[string]any))
		}
		return res
	}
	idsOf := func(out map[string]any) []string {
		var ids []string
		for _, a := range accountsOf(out) {
			ids = append(ids, a["id"].(string))
		}
		return ids
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	create := func(k apiKey, body string) map[string]any {
		t.Helper()
		code, raw, out := signed(k, "POST", "/api/v1/accounts", body)
		must("create "+body, 201, code, raw)
		return out
	}

	ref := "bot-ref-" + sfx
	a := create(bot, `{"label":"a","external_ref":"`+ref+`","data_quota_gb":5}`)
	b := create(bot, `{"label":"b","data_quota_gb":5}`)
	c := create(bot, `{"label":"c","data_quota_gb":5}`)
	aID, bID, cID := a["id"].(string), b["id"].(string), c["id"].(string)

	t.Run("find by external_ref", func(t *testing.T) {
		code, raw, out := signed(bot, "GET", "/api/v1/accounts?external_ref="+ref, "")
		must("find", 200, code, raw)
		if ids := idsOf(out); len(ids) != 1 || ids[0] != aID {
			t.Fatalf("find: %v, want [%s]", ids, aID)
		}
		// Not a prefix/substring match.
		code, raw, out = signed(bot, "GET", "/api/v1/accounts?external_ref="+ref[:len(ref)-1], "")
		must("find prefix", 200, code, raw)
		if len(accountsOf(out)) != 0 {
			t.Fatalf("a prefix matched: %s", raw)
		}
		// Another namespace sees nothing - an empty array, not null and not 404.
		code, raw, _ = signed(other, "GET", "/api/v1/accounts?external_ref="+ref, "")
		must("find as another key", 200, code, raw)
		if !strings.Contains(strings.ReplaceAll(raw, " ", ""), `"accounts":[]`) {
			t.Fatalf("foreign namespace: %s, want an empty accounts array", raw)
		}
		code, raw, _ = signed(bot, "GET", "/api/v1/accounts?external_ref=nope-"+sfx, "")
		must("find missing", 200, code, raw)
		if !strings.Contains(strings.ReplaceAll(raw, " ", ""), `"accounts":[]`) {
			t.Fatalf("missing ref: %s, want an empty accounts array", raw)
		}
		// limit/offset are ignored (not validated) when external_ref is given.
		code, raw, out = signed(bot, "GET", "/api/v1/accounts?external_ref="+ref+"&limit=abc&offset=-5", "")
		must("find with junk paging", 200, code, raw)
		if len(accountsOf(out)) != 1 {
			t.Fatalf("junk paging: %s", raw)
		}
		// An empty external_ref is "not given": the plain list comes back.
		code, raw, out = signed(bot, "GET", "/api/v1/accounts?external_ref=", "")
		must("empty external_ref", 200, code, raw)
		if len(accountsOf(out)) != 3 {
			t.Fatalf("empty external_ref should list: %s", raw)
		}
		if code, _, _ := signed(reader, "GET", "/api/v1/accounts?external_ref="+ref, ""); code != 200 {
			t.Fatalf("a read-only key must be able to look up by ref: %d", code)
		}
	})

	t.Run("a deleted account is still found and its ref stays taken", func(t *testing.T) {
		code, raw, out := signed(bot, "DELETE", "/api/v1/accounts/"+aID, "")
		must("delete", 200, code, raw)
		if out["status"] != "deleted" {
			t.Fatalf("delete: %v", out)
		}
		code, raw, out = signed(bot, "GET", "/api/v1/accounts?external_ref="+ref, "")
		must("find deleted", 200, code, raw)
		found := accountsOf(out)
		if len(found) != 1 || found[0]["id"] != aID || found[0]["status"] != "deleted" {
			t.Fatalf("find after delete: %s", raw)
		}
		code, raw, out = signed(bot, "GET", "/api/v1/accounts", "")
		must("list", 200, code, raw)
		for _, id := range idsOf(out) {
			if id == aID {
				t.Fatalf("the plain list shows a deleted account: %s", raw)
			}
		}
		code, raw, out = signed(bot, "POST", "/api/v1/accounts", `{"label":"again","external_ref":"`+ref+`"}`)
		must("re-create with the same ref", 409, code, raw)
		if ecode(out) != "external_ref_taken" {
			t.Fatalf("re-create: %s", raw)
		}
	})

	t.Run("offset paging", func(t *testing.T) {
		// A was deleted above, so the key owns exactly b then c (creation order).
		want := []string{bID, cID}
		code, raw, out := signed(bot, "GET", "/api/v1/accounts", "")
		must("full list", 200, code, raw)
		if got := idsOf(out); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("full list order %v, want %v", got, want)
		}
		for offset, wantIDs := range map[int][]string{0: {bID}, 1: {cID}, 2: nil, 50: nil} {
			code, raw, out = signed(bot, "GET", fmt.Sprintf("/api/v1/accounts?limit=1&offset=%d", offset), "")
			must("page", 200, code, raw)
			if got := idsOf(out); fmt.Sprint(got) != fmt.Sprint(wantIDs) {
				t.Fatalf("limit=1&offset=%d: %v, want %v", offset, got, wantIDs)
			}
		}
		// Omitting offset is offset=0; the response shape is unchanged.
		code, raw, out = signed(bot, "GET", "/api/v1/accounts?limit=1", "")
		must("limit only", 200, code, raw)
		if got := idsOf(out); len(got) != 1 || got[0] != bID {
			t.Fatalf("limit=1: %v", got)
		}
		for _, bad := range []string{"-1", "abc", "1.5", "", "99999999999999999999"} {
			code, raw, out = signed(bot, "GET", "/api/v1/accounts?offset="+bad, "")
			if code != 400 || ecode(out) != "invalid_request" {
				t.Fatalf("offset=%q: %d %s, want 400 invalid_request", bad, code, raw)
			}
		}
	})

	t.Run("reset usage", func(t *testing.T) {
		path := "/api/v1/accounts/" + bID + "/reset-usage"
		sql(`UPDATE accounts SET data_used_bytes = 7000, status = 'suspended', suspend_reason = 'quota_exceeded' WHERE id = $1`, bID)

		code, raw, out := signed(bot, "POST", path, "") // empty body
		must("reset", 200, code, raw)
		if out["id"] != bID || out["data_used_bytes"] != float64(0) || out["status"] != "active" || out["suspend_reason"] != nil {
			t.Fatalf("reset response: %s", raw)
		}
		if out["data_quota_bytes"] != float64(5_000_000_000) {
			t.Fatalf("quota must be unchanged: %v", out["data_quota_bytes"])
		}
		if _, ok := out["peers"].([]any); !ok || len(out["peers"].([]any)) == 0 {
			t.Fatalf("response should be the full account object: %s", raw)
		}

		// {} is fine too, and a manual suspension survives (usage still zeroed).
		sql(`UPDATE accounts SET data_used_bytes = 9000, status = 'suspended', suspend_reason = 'manual' WHERE id = $1`, cID)
		code, raw, out = signed(bot, "POST", "/api/v1/accounts/"+cID+"/reset-usage", "{}")
		must("reset manual-suspended", 200, code, raw)
		if out["data_used_bytes"] != float64(0) || out["status"] != "suspended" || out["suspend_reason"] != "manual" {
			t.Fatalf("manual suspension must not be lifted: %s", raw)
		}

		if code, raw, out := signed(reader, "POST", path, ""); code != 403 || ecode(out) != "forbidden" {
			t.Fatalf("key without update: %d %s, want 403", code, raw)
		}
		if code, raw, out := signed(other, "POST", path, ""); code != 404 || ecode(out) != "account_not_found" {
			t.Fatalf("foreign namespace: %d %s, want 404 account_not_found", code, raw)
		}
		if code, raw, out := signed(bot, "POST", "/api/v1/accounts/"+aID+"/reset-usage", ""); code != 404 || ecode(out) != "account_not_found" {
			t.Fatalf("deleted account: %d %s, want 404 account_not_found", code, raw)
		}
		if code, raw, out := signed(bot, "POST", "/api/v1/accounts/00000000-0000-0000-0000-000000000000/reset-usage", ""); code != 404 || ecode(out) != "account_not_found" {
			t.Fatalf("unknown id: %d %s, want 404", code, raw)
		}
		req := httptest.NewRequest("POST", path, nil)
		if code, _, _ := do(req); code != 401 {
			t.Fatalf("unauthenticated: %d, want 401", code)
		}
		if code, _, out := bearer("support", "POST", path); code != 403 || ecode(out) != "forbidden" {
			t.Fatalf("support session: %d, want 403", code)
		}

		// Audit trail.
		code, raw, out = bearer("super_admin", "GET", "/api/v1/audit-log?limit=200")
		must("audit log", 200, code, raw)
		var seen bool
		for _, e := range out["entries"].([]any) {
			e := e.(map[string]any)
			if e["action"] == "account.usage_reset" && e["target"] == bID {
				seen = e["actor"] == "api_key:"+bot.id
			}
		}
		if !seen {
			t.Fatalf("no account.usage_reset audit row for %s by api_key:%s: %s", bID, bot.id, raw)
		}
	})

	t.Run("clear flags in PATCH", func(t *testing.T) {
		path := "/api/v1/accounts/" + bID
		code, raw, out := signed(bot, "PATCH", path, `{"device_limit":2}`)
		must("set device limit", 200, code, raw)
		if out["device_limit"] != float64(2) {
			t.Fatalf("%s", raw)
		}

		// false / absent: nothing changes.
		code, raw, out = signed(bot, "PATCH", path, `{"clear_data_quota":false,"clear_device_limit":false,"label":"b2"}`)
		must("false flags", 200, code, raw)
		if out["data_quota_bytes"] != float64(5_000_000_000) || out["device_limit"] != float64(2) || out["label"] != "b2" {
			t.Fatalf("false flags changed something: %s", raw)
		}

		for _, body := range []string{
			`{"clear_data_quota":true,"data_quota_gb":3}`,
			`{"clear_device_limit":true,"device_limit":4}`,
		} {
			code, raw, out = signed(bot, "PATCH", path, body)
			if code != 400 || ecode(out) != "invalid_request" {
				t.Fatalf("%s: %d %s, want 400 invalid_request", body, code, raw)
			}
		}
		code, raw, out = signed(bot, "GET", path, "")
		must("get after refused patches", 200, code, raw)
		if out["data_quota_bytes"] != float64(5_000_000_000) || out["device_limit"] != float64(2) {
			t.Fatalf("a refused patch must change nothing: %s", raw)
		}

		// Clear the device limit only.
		code, raw, out = signed(bot, "PATCH", path, `{"clear_device_limit":true}`)
		must("clear device limit", 200, code, raw)
		if out["device_limit"] != nil || out["data_quota_bytes"] != float64(5_000_000_000) {
			t.Fatalf("clear device limit: %s", raw)
		}

		// Clearing the quota lifts a quota_exceeded suspension; null in the response.
		sql(`UPDATE accounts SET data_used_bytes = 9000000000, status = 'suspended', suspend_reason = 'quota_exceeded' WHERE id = $1`, bID)
		code, raw, out = signed(bot, "PATCH", path, `{"clear_data_quota":true}`)
		must("clear data quota", 200, code, raw)
		if out["data_quota_bytes"] != nil || out["status"] != "active" || out["suspend_reason"] != nil {
			t.Fatalf("clear data quota: %s", raw)
		}
		if out["data_used_bytes"] != float64(9_000_000_000) {
			t.Fatalf("clearing the quota must not reset usage: %s", raw)
		}
		// Both at once, on an already-unlimited account, is fine and idempotent.
		code, raw, out = signed(bot, "PATCH", path, `{"clear_data_quota":true,"clear_device_limit":true}`)
		must("clear both", 200, code, raw)
		if out["data_quota_bytes"] != nil || out["device_limit"] != nil {
			t.Fatalf("clear both: %s", raw)
		}
	})

	t.Run("reseller credit refuses an unlimited account", func(t *testing.T) {
		limit := int64(100_000_000_000)
		withCredit, err := st.CreateReseller(ctx, store.NewResellerParams{
			Username: "aapi-credit-" + sfx, PasswordHash: "h", DataLimitBytes: &limit, NodeGroups: []string{group},
		})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, withCredit.AdminID)
		namespaces = append(namespaces, store.ResellerNamespace(withCredit.AdminID))
		noCredit, err := st.CreateReseller(ctx, store.NewResellerParams{
			Username: "aapi-free-" + sfx, PasswordHash: "h", NodeGroups: []string{group},
		})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, noCredit.AdminID)
		namespaces = append(namespaces, store.ResellerNamespace(noCredit.AdminID))

		creditBot := mkKey("creditbot", allPerms, &withCredit.AdminID)
		freeBot := mkKey("freebot", allPerms, &noCredit.AdminID)

		x := create(creditBot, `{"label":"x","data_quota_gb":2}`)
		path := "/api/v1/accounts/" + x["id"].(string)
		code, raw, out := signed(creditBot, "PATCH", path, `{"clear_data_quota":true}`)
		if code != 422 || ecode(out) != "reseller_unlimited_not_allowed" {
			t.Fatalf("clear under a credit: %d %s, want 422 reseller_unlimited_not_allowed", code, raw)
		}
		code, raw, out = signed(creditBot, "GET", path, "")
		must("get", 200, code, raw)
		if out["data_quota_bytes"] != float64(2_000_000_000) {
			t.Fatalf("a refused clear must leave the quota: %s", raw)
		}
		if code, raw, _ := signed(creditBot, "PATCH", path, `{"clear_device_limit":true}`); code != 200 {
			t.Fatalf("clear device limit under a credit: %d %s", code, raw)
		}

		y := create(freeBot, `{"label":"y","data_quota_gb":2}`)
		code, raw, out = signed(freeBot, "PATCH", "/api/v1/accounts/"+y["id"].(string), `{"clear_data_quota":true}`)
		must("clear without a credit", 200, code, raw)
		if out["data_quota_bytes"] != nil {
			t.Fatalf("%s", raw)
		}

		// A credit-limited reseller's key may not reset usage (it would re-sell quota it was
		// already charged for); one without a credit may. The reseller's own session cannot
		// at all (not on the allowlist).
		if code, raw, out := signed(creditBot, "POST", path+"/reset-usage", ""); code != 403 || ecode(out) != "reseller_reset_not_allowed" {
			t.Fatalf("credit-limited reseller key reset: %d %s, want 403 reseller_reset_not_allowed", code, raw)
		}
		if code, raw, out := signed(freeBot, "POST", "/api/v1/accounts/"+y["id"].(string)+"/reset-usage", ""); code != 200 || out["data_used_bytes"] != float64(0) {
			t.Fatalf("reseller key without a credit reset: %d %s", code, raw)
		}
		req := httptest.NewRequest("POST", path+"/reset-usage", nil)
		resellerTok, err := authcrypto.IssueAccessToken(gateTestSecret, withCredit.AdminID, "u", "reseller")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+resellerTok)
		if code, _, _ := do(req); code != 403 {
			t.Fatalf("reseller session reset: %d, want 403", code)
		}
	})

	t.Run("data cap: PATCH suspends and lifts, enable is refused while over the cap", func(t *testing.T) {
		d := create(bot, `{"label":"cap","data_quota_gb":1}`)
		id := d["id"].(string)
		path := "/api/v1/accounts/" + id

		// Lower the quota to exactly what the account already used: suspended on the spot.
		sql(`UPDATE accounts SET data_used_bytes = 500000000 WHERE id = $1`, id)
		code, raw, out := signed(bot, "PATCH", path, `{"data_quota_gb":0.5}`)
		must("lower quota to usage", 200, code, raw)
		if out["status"] != "suspended" || out["suspend_reason"] != "quota_exceeded" {
			t.Fatalf("lowering the quota to the usage: %s", raw)
		}

		// Enabling it would be undone by the next sample, so it is refused, not faked.
		code, raw, out = signed(bot, "POST", path+"/enable", "")
		if code != 409 || ecode(out) != "quota_exhausted" {
			t.Fatalf("enable over the cap: %d %s, want 409 quota_exhausted", code, raw)
		}
		code, raw, out = signed(bot, "GET", path, "")
		must("get", 200, code, raw)
		if out["status"] != "suspended" || out["suspend_reason"] != "quota_exceeded" {
			t.Fatalf("a refused enable must change nothing: %s", raw)
		}

		// Raising the cap above the usage reactivates it by itself.
		code, raw, out = signed(bot, "PATCH", path, `{"data_quota_gb":1}`)
		must("raise quota", 200, code, raw)
		if out["status"] != "active" || out["suspend_reason"] != nil {
			t.Fatalf("raising the quota above the usage: %s", raw)
		}

		// A manual suspension over the cap: enable refused until the cap moves, then fine.
		sql(`UPDATE accounts SET status = 'suspended', suspend_reason = 'manual', data_used_bytes = 2000000000 WHERE id = $1`, id)
		if code, raw, out = signed(bot, "POST", path+"/enable", ""); code != 409 || ecode(out) != "quota_exhausted" {
			t.Fatalf("enable a manual suspension over the cap: %d %s", code, raw)
		}
		code, raw, out = signed(bot, "PATCH", path, `{"data_quota_gb":5}`)
		must("raise quota again", 200, code, raw)
		if out["status"] != "suspended" || out["suspend_reason"] != "manual" {
			t.Fatalf("a raise must not lift a manual suspension: %s", raw)
		}
		code, raw, out = signed(bot, "POST", path+"/enable", "")
		must("enable after the raise", 200, code, raw)
		if out["status"] != "active" {
			t.Fatalf("%s", raw)
		}

		// Read-time: an active account found over its cap reads as suspended.
		sql(`UPDATE accounts SET data_used_bytes = 9000000000 WHERE id = $1`, id)
		code, raw, out = signed(bot, "GET", path, "")
		must("get over the cap", 200, code, raw)
		if out["status"] != "suspended" || out["suspend_reason"] != "quota_exceeded" {
			t.Fatalf("an account over its cap must read as suspended: %s", raw)
		}
		// ...and an unlimited one never does.
		code, raw, out = signed(bot, "PATCH", path, `{"clear_data_quota":true}`)
		must("clear quota", 200, code, raw)
		if out["status"] != "active" || out["data_quota_bytes"] != nil {
			t.Fatalf("clear lifts: %s", raw)
		}
		code, raw, out = signed(bot, "GET", path, "")
		must("get unlimited", 200, code, raw)
		if out["status"] != "active" {
			t.Fatalf("unlimited account suspended: %s", raw)
		}
	})

	t.Run("last_handshake_at on every account response", func(t *testing.T) {
		// A fresh account that has never connected: null, but the key is always present.
		fresh := create(bot, `{"label":"hs"}`)
		if v, ok := fresh["last_handshake_at"]; !ok || v != nil {
			t.Fatalf("create: last_handshake_at present=%v value=%v, want present and null", ok, v)
		}
		id := fresh["id"].(string)
		path := "/api/v1/accounts/" + id

		at := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
		sql(`UPDATE account_peers SET last_handshake_at = $2 WHERE account_id = $1`, id, at)

		checkAt := func(what string, out map[string]any) {
			t.Helper()
			v, ok := out["last_handshake_at"].(string)
			if !ok {
				t.Fatalf("%s: last_handshake_at = %v, want an RFC3339 string", what, out["last_handshake_at"])
			}
			got, err := time.Parse(time.RFC3339, v)
			if err != nil || !got.Equal(at) {
				t.Fatalf("%s: last_handshake_at = %q (%v), want %s", what, v, err, at.Format(time.RFC3339))
			}
			// The account-level value is the max of the peers' own.
			peers := out["peers"].([]any)
			if len(peers) == 0 || peers[0].(map[string]any)["last_handshake_at"] != v {
				t.Fatalf("%s: peers %v disagree with %q", what, peers, v)
			}
		}

		for _, step := range []struct{ name, method, suffix, body string }{
			{"get", "GET", "", ""},
			{"patch", "PATCH", "", `{"label":"hs2"}`},
			{"renew", "POST", "/renew", `{"extend_days":1}`},
			{"suspend", "POST", "/suspend", `{}`},
			{"enable", "POST", "/enable", ""},
			{"rotate", "POST", "/subscription/rotate", ""},
			{"reset-usage", "POST", "/reset-usage", ""},
		} {
			code, raw, out := signed(bot, step.method, path+step.suffix, step.body)
			must(step.name, 200, code, raw)
			checkAt(step.name, out)
		}

		code, raw, out := signed(bot, "GET", "/api/v1/accounts", "")
		must("list", 200, code, raw)
		var inList bool
		for _, acct := range accountsOf(out) {
			if acct["id"] == id {
				inList = true
				checkAt("list", acct)
			} else if v, ok := acct["last_handshake_at"]; !ok {
				t.Fatalf("list: account %v lacks last_handshake_at (%v)", acct["id"], v)
			}
		}
		if !inList {
			t.Fatalf("account missing from the list: %s", raw)
		}

		code, raw, out = signed(bot, "DELETE", path, "")
		must("delete", 200, code, raw)
		checkAt("delete", out)
	})
}
