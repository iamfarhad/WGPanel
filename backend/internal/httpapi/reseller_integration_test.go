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

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

const itHexKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// TestResellerEndToEnd drives the real router against Postgres: reseller sessions,
// reseller-owned API keys (HMAC), quota enforcement and the disabled/expired cut-off.
// Skipped without WGPANEL_TEST_POSTGRES_DSN (same switch as the store tests).
func TestResellerEndToEnd(t *testing.T) {
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
	relaxAPIRate(t, st)
	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Routes()

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	pub := "ek-" + sfx
	node, err := st.CreateNode(ctx, "e2e-"+sfx, "eu-"+sfx, "eu", "e.test:51820", "10.77.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn) // fresh nodes are 'pending'; account creation needs online
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}
	// Take the node out of rotation when done: store.TestStoreIntegration asserts how many
	// online nodes exist, and this one (500 slots, left online) made it fail whenever
	// the httpapi package ran first.
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `UPDATE nodes SET status = 'pending' WHERE id = $1`, node.ID)
	})

	one, limit := 1, int64(10_000_000_000)
	mkRes := func(name string, p store.NewResellerParams) store.Reseller {
		p.Username, p.PasswordHash = name+sfx, "h"
		r, err := st.CreateReseller(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	res := mkRes("e2e", store.NewResellerParams{MaxAccounts: &one, DataLimitBytes: &limit, NodeGroups: []string{"eu-" + sfx}})
	other := mkRes("e2eother", store.NewResellerParams{})

	session := func(role, id string) string {
		tok, _ := authcrypto.IssueAccessToken(gateTestSecret, id, "u", role)
		return tok
	}
	do := func(req *http.Request) (int, map[string]any) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	bearer := func(tok, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		return do(req)
	}
	signed := func(keyID, secret, method, path, body string) (int, map[string]any) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC(secret, method+"\n"+strings.SplitN(path, "?", 2)[0]+"\n"+ts+"\n"+body))
		return do(req)
	}
	resTok := session("reseller", res.AdminID)

	// Key management by the reseller: group outside its own is refused, own groups pass.
	code, out := bearer(resTok, "POST", "/api/v1/api-keys", `{"label":"bad","node_groups":["us-zzz"],"permissions":["read"]}`)
	if code != 422 || ecode(out) != "reseller_node_group_not_allowed" {
		t.Fatalf("foreign group key: %d %v", code, out)
	}
	code, out = bearer(resTok, "POST", "/api/v1/api-keys", `{"label":"bot","permissions":["read","create","update","suspend","delete"]}`)
	if code != 201 || out["reseller_id"] != res.AdminID {
		t.Fatalf("create key: %d %v", code, out)
	}
	keyID, secret, keyRowID := out["key_id"].(string), out["secret"].(string), out["id"].(string)

	// A second reseller's key must be invisible (404) to the first, and not listed.
	otherKey, err := st.CreateAPIKey(ctx, "other-"+sfx, mustEnc(t, "sec"), "o", []string{}, []string{"read"}, &other.AdminID)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := bearer(resTok, "POST", "/api/v1/api-keys/"+otherKey.ID+"/revoke", ""); code != 404 {
		t.Fatalf("revoking another reseller's key: %d, want 404", code)
	}
	if code, _ := bearer(resTok, "POST", "/api/v1/api-keys/"+otherKey.ID+"/rotate", ""); code != 404 {
		t.Fatalf("rotating another reseller's key: %d, want 404", code)
	}
	_, list := bearer(resTok, "GET", "/api/v1/api-keys", "")
	for _, k := range list["api_keys"].([]any) {
		if k.(map[string]any)["reseller_id"] != res.AdminID {
			t.Fatalf("reseller saw a foreign key: %v", k)
		}
	}
	// Staff below super_admin are still denied; super_admin sees every key.
	if code, _ := bearer(session("operator", "x"), "GET", "/api/v1/api-keys", ""); code != 403 {
		t.Fatalf("operator on api-keys: %d, want 403", code)
	}
	_, all := bearer(session("super_admin", "x"), "GET", "/api/v1/api-keys", "")
	if len(all["api_keys"].([]any)) < 2 {
		t.Fatalf("super_admin should see all keys: %v", all)
	}

	// Quota enforcement through the key: unlimited refused, credit, then max_accounts.
	if code, out := signed(keyID, secret, "POST", "/api/v1/accounts", `{"label":"a"}`); code != 422 || ecode(out) != "reseller_unlimited_not_allowed" {
		t.Fatalf("unlimited via key: %d %v", code, out)
	}
	code, out = signed(keyID, secret, "POST", "/api/v1/accounts", `{"label":"a","data_quota_gb":5}`)
	if code != 201 || out["owner"] != "reseller:"+res.AdminID {
		t.Fatalf("create via key: %d %v", code, out)
	}
	acctID := out["id"].(string)
	if code, out := signed(keyID, secret, "POST", "/api/v1/accounts", `{"label":"b","data_quota_gb":1}`); code != 409 || ecode(out) != "reseller_account_limit_reached" {
		t.Fatalf("second via key: %d %v", code, out)
	}
	// The key and the reseller session share one namespace; a foreign admin-created
	// account is invisible to both.
	if code, _ := bearer(resTok, "GET", "/api/v1/accounts/"+acctID, ""); code != 200 {
		t.Fatalf("session cannot see key-created account: %d", code)
	}
	otherNS := store.ResellerNamespace(other.AdminID)
	foreign, err := st.CreateAccount(ctx, store.CreateAccountParams{
		Label: "f", PublicKey: "fpk-" + sfx, PrivateKeyEncrypted: "x", SubscriptionToken: "ft-" + sfx, OwnerKeyNamespace: &otherNS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := signed(keyID, secret, "GET", "/api/v1/accounts/"+foreign.ID, ""); code != 404 {
		t.Fatalf("key reading foreign account: %d, want 404", code)
	}

	// Reports: a reseller (session or key) is pinned to its own namespace whatever
	// reseller_id it asks for, and never receives the reseller leaderboard.
	for name, call := range map[string]func() (int, map[string]any){
		"session": func() (int, map[string]any) {
			return bearer(resTok, "GET", "/api/v1/reports?range=24h&reseller_id="+other.AdminID, "")
		},
		"key": func() (int, map[string]any) {
			return signed(keyID, secret, "GET", "/api/v1/reports?range=24h&reseller_id="+other.AdminID, "")
		},
	} {
		code, rep := call()
		scope, _ := rep["scope"].(map[string]any)
		if code != 200 || scope["reseller_id"] != res.AdminID || rep["resellers"] != nil {
			t.Fatalf("%s report: %d scope=%v resellers=%v", name, code, scope, rep["resellers"])
		}
		if tot := rep["totals"].(map[string]any); tot["accounts_total"].(float64) != 1 {
			t.Fatalf("%s report counted foreign accounts: %v", name, tot)
		}
	}
	if code, _ := bearer(resTok, "GET", "/api/v1/reports?range=3y", ""); code != 400 {
		t.Fatalf("bad range: %d", code)
	}
	if code, rep := bearer(session("super_admin", "x"), "GET", "/api/v1/reports", ""); code != 200 || rep["resellers"] == nil {
		t.Fatalf("super_admin report: %d resellers=%v", code, rep["resellers"])
	}

	// Disabled, then expired, reseller: both its key and its session are cut off at once.
	off := true
	if err := st.UpdateResellerLimits(ctx, res.AdminID, store.ResellerLimitsPatch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if code, out := signed(keyID, secret, "GET", "/api/v1/accounts", ""); code != 401 || ecode(out) != "reseller_disabled" {
		t.Fatalf("disabled key: %d %v", code, out)
	}
	if code, out := bearer(resTok, "GET", "/api/v1/accounts", ""); code != 401 || ecode(out) != "account_disabled" {
		t.Fatalf("disabled session: %d %v", code, out)
	}
	on := false
	past := time.Now().Add(-time.Minute)
	if err := st.UpdateResellerLimits(ctx, res.AdminID, store.ResellerLimitsPatch{
		Disabled: &on, ExpiresAt: store.Opt[time.Time]{Set: true, Value: &past},
	}); err != nil {
		t.Fatal(err)
	}
	if code, out := signed(keyID, secret, "GET", "/api/v1/accounts", ""); code != 401 || ecode(out) != "reseller_expired" {
		t.Fatalf("expired key: %d %v", code, out)
	}
	if code, out := bearer(resTok, "GET", "/api/v1/accounts", ""); code != 401 || ecode(out) != "reseller_expired" {
		t.Fatalf("expired session: %d %v", code, out)
	}
	// Revoke by the super admin still works on a reseller-owned key.
	if code, _ := bearer(session("super_admin", "x"), "POST", "/api/v1/api-keys/"+keyRowID+"/revoke", ""); code != 200 {
		t.Fatalf("super_admin revoke: %d", code)
	}
}

// ecode extracts the machine code from the {"error":{"code":...}} envelope.
func ecode(m map[string]any) any {
	e, _ := m["error"].(map[string]any)
	return e["code"]
}

func mustEnc(t *testing.T, v string) string {
	t.Helper()
	e, err := wgkeys.Encrypt(itHexKey, v)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
