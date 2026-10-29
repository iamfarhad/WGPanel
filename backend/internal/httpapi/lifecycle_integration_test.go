package httpapi

import (
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

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

// TestLifecycleEndToEnd drives templates, on-hold creation and the bulk endpoint through
// the real router: reseller scoping, per-item limits/credit, partial failure, audit rows.
func TestLifecycleEndToEnd(t *testing.T) {
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
	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Routes()

	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	group := "lc-" + sfx
	pub := "lk-" + sfx
	node, err := st.CreateNode(ctx, "lc-"+sfx, group, "eu", "l.test:51820", "10.78.0.0/16", 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `UPDATE nodes SET status = 'pending' WHERE id = $1`, node.ID)
		_, _ = c.Exec(context.Background(), `DELETE FROM plan_templates WHERE name LIKE $1`, "%"+sfx)
	})

	two, credit := 3, int64(5_000_000_000)
	mkRes := func(name string, p store.NewResellerParams) store.Reseller {
		p.Username, p.PasswordHash = name+sfx, "h"
		r, err := st.CreateReseller(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	res := mkRes("lc", store.NewResellerParams{MaxAccounts: &two, DataLimitBytes: &credit, NodeGroups: []string{group}})
	other := mkRes("lcother", store.NewResellerParams{NodeGroups: []string{group}})
	tok := func(role, id string) string {
		v, _ := authcrypto.IssueAccessToken(gateTestSecret, id, "u", role)
		return v
	}
	resTok, otherTok, staff := tok("reseller", res.AdminID), tok("reseller", other.AdminID), tok("operator", "op")
	support := tok("support", "sp")
	call := func(token, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	// --- templates: staff global + shared, reseller private ---
	code, shared := call(staff, "POST", "/api/v1/templates",
		`{"name":"shared-`+sfx+`","available_to_resellers":true,"data_quota_gb":1,"duration_days":30,"reset_strategy":"month","on_hold":true,"hold_deadline_days":14,"node_group":"`+group+`"}`)
	if code != 201 || shared["owner"] != nil || shared["data_quota_bytes"].(float64) != 1e9 {
		t.Fatalf("staff template: %d %v", code, shared)
	}
	sharedID := shared["id"].(string)
	code, priv := call(staff, "POST", "/api/v1/templates", `{"name":"private-`+sfx+`"}`)
	if code != 201 {
		t.Fatalf("private template: %d %v", code, priv)
	}
	if code, out := call(staff, "POST", "/api/v1/templates", `{"name":"x-`+sfx+`","on_hold":true}`); code != 400 {
		t.Fatalf("on-hold template without duration: %d %v", code, out)
	}
	if code, _ := call(support, "POST", "/api/v1/templates", `{"name":"s-`+sfx+`"}`); code != 403 {
		t.Fatalf("support write: %d", code)
	}
	code, mine := call(resTok, "POST", "/api/v1/templates", `{"name":"mine-`+sfx+`","data_quota_gb":2,"duration_days":7}`)
	if code != 201 || mine["owner"] != "reseller:"+res.AdminID {
		t.Fatalf("reseller template: %d %v", code, mine)
	}
	mineID := mine["id"].(string)
	_, lst := call(resTok, "GET", "/api/v1/templates", "")
	names := map[string]bool{}
	for _, x := range lst["templates"].([]any) {
		names[x.(map[string]any)["name"].(string)] = true
	}
	if !names["shared-"+sfx] || !names["mine-"+sfx] || names["private-"+sfx] {
		t.Fatalf("reseller template list: %v", names)
	}
	if code, _ := call(otherTok, "GET", "/api/v1/templates/"+mineID, ""); code != 404 {
		t.Fatalf("foreign template get: %d", code)
	}
	if code, _ := call(resTok, "PUT", "/api/v1/templates/"+sharedID, `{"name":"hack-`+sfx+`"}`); code != 404 {
		t.Fatalf("reseller editing a staff template: %d", code)
	}
	if code, _ := call(resTok, "GET", "/api/v1/templates/not-a-uuid", ""); code != 404 {
		t.Fatalf("bad id: %d", code)
	}

	// --- create via template (on hold) with an explicit override ---
	code, acct := call(resTok, "POST", "/api/v1/accounts", `{"label":"tmpl-1","template_id":"`+sharedID+`","data_quota_gb":0.5}`)
	if code != 201 || acct["on_hold"] != true || acct["expiry_at"] != nil || acct["hold_duration_days"].(float64) != 30 ||
		acct["reset_strategy"] != "month" || acct["next_reset_at"] == nil || acct["hold_deadline_at"] == nil || acct["data_quota_bytes"].(float64) != 5e8 {
		t.Fatalf("create from template: %d %v", code, acct)
	}
	onHoldID := acct["id"].(string)
	if code, out := call(resTok, "POST", "/api/v1/accounts", `{"label":"x","template_id":"`+priv["id"].(string)+`"}`); code != 404 || ecode(out) != "template_not_found" {
		t.Fatalf("invisible template: %d %v", code, out)
	}

	// renew an on-hold account lengthens the hold, not the expiry
	code, ren := call(resTok, "POST", "/api/v1/accounts/"+onHoldID+"/renew", `{"extend_days":5}`)
	if code != 200 || ren["on_hold"] != true || ren["hold_duration_days"].(float64) != 35 || ren["expiry_at"] != nil {
		t.Fatalf("renew on hold: %d %v", code, ren)
	}

	// --- bulk create: reseller limit is 3 accounts / 5 GB credit, 1 used (0.5 GB) ---
	code, b := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"create","count":4,"label_prefix":"batch","template_id":"`+mineID+`"}`)
	if code != 200 || b["succeeded"].(float64) != 2 || b["failed"].(float64) != 2 || b["total"].(float64) != 4 {
		t.Fatalf("bulk create over the account limit: %d %v", code, b)
	}
	results := b["results"].([]any)
	if r0 := results[0].(map[string]any); r0["ok"] != true || r0["label"] != "batch-01" || r0["account"].(map[string]any)["subscription_path"] == "" {
		t.Fatalf("first item: %v", r0)
	}
	if r3 := results[3].(map[string]any); r3["ok"] != false || r3["error"].(map[string]any)["code"] != "reseller_account_limit_reached" {
		t.Fatalf("last item: %v", r3)
	}
	var created []string
	for _, r := range results {
		if m := r.(map[string]any); m["ok"] == true {
			created = append(created, m["id"].(string))
		}
	}
	// per-item audit rows
	for _, id := range created {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE target = $1 AND action = 'account.created'`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("audit rows for %s = %d (%v)", id, n, err)
		}
	}
	// credit is per item too: 0.5 + 2 + 2 = 4.5 GB allocated; one more 2 GB would exceed
	if code, out := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"renew","ids":["`+created[0]+`"],"add_quota_gb":2}`); code != 200 ||
		out["failed"].(float64) != 1 || out["results"].([]any)[0].(map[string]any)["error"].(map[string]any)["code"] != "reseller_data_credit_exceeded" {
		t.Fatalf("renew over credit: %d %v", code, out)
	}

	// --- bulk over ids: partial failure incl. a foreign id and a duplicate ---
	foreignNS := store.ResellerNamespace(other.AdminID)
	foreign, err := st.CreateAccount(ctx, store.CreateAccountParams{
		Label: "f", PublicKey: "fpk-" + sfx, PrivateKeyEncrypted: "x", SubscriptionToken: "ft-" + sfx, OwnerKeyNamespace: &foreignNS,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"action":"suspend","ids":["` + created[0] + `","` + foreign.ID + `","` + created[0] + `","nope"]}`
	code, sus := call(resTok, "POST", "/api/v1/accounts/bulk", body)
	if code != 200 || sus["succeeded"].(float64) != 1 || sus["failed"].(float64) != 3 {
		t.Fatalf("bulk suspend: %d %v", code, sus)
	}
	codes := []string{}
	for _, r := range sus["results"].([]any) {
		if e, ok := r.(map[string]any)["error"].(map[string]any); ok {
			codes = append(codes, e["code"].(string))
		}
	}
	if strings.Join(codes, ",") != "account_not_found,duplicate_id,invalid_request" {
		t.Fatalf("error codes: %v", codes)
	}
	if a, _ := st.GetAccount(ctx, foreign.ID, nil); a.Status != "active" {
		t.Fatal("foreign account was suspended by another reseller's bulk")
	}
	if code, out := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"enable","ids":["`+created[0]+`"]}`); code != 200 || out["succeeded"].(float64) != 1 {
		t.Fatalf("bulk enable: %d %v", code, out)
	}
	// credit-limited reseller may not reset usage (per-item refusal, same as the single route's policy)
	if code, out := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"reset_usage","ids":["`+created[0]+`"]}`); code != 200 ||
		out["results"].([]any)[0].(map[string]any)["error"].(map[string]any)["code"] != "reseller_reset_not_allowed" {
		t.Fatalf("bulk reset by credit-limited reseller: %d %v", code, out)
	}
	if code, out := call(resTok, "POST", "/api/v1/accounts/bulk", `{"action":"delete","ids":["`+created[0]+`","`+created[1]+`"]}`); code != 200 || out["succeeded"].(float64) != 2 {
		t.Fatalf("bulk delete: %d %v", code, out)
	}

	// --- validation and permissions ---
	for name, body := range map[string]string{
		"unknown action": `{"action":"explode","ids":["x"]}`,
		"no ids":         `{"action":"suspend","ids":[]}`,
		"count zero":     `{"action":"create","count":0,"label_prefix":"a"}`,
		"count too big":  `{"action":"create","count":201,"label_prefix":"a"}`,
		"no prefix":      `{"action":"create","count":2}`,
		"external_ref":   `{"action":"create","count":2,"label_prefix":"a","external_ref":"r"}`,
		"renew nothing":  `{"action":"renew","ids":["` + foreign.ID + `"]}`,
	} {
		if code, out := call(staff, "POST", "/api/v1/accounts/bulk", body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, _ := call(support, "POST", "/api/v1/accounts/bulk", `{"action":"suspend","ids":["`+foreign.ID+`"]}`); code != 403 {
		t.Fatalf("support bulk: %d", code)
	}

	// staff can bulk-create across namespaces with plain fields and no template
	code, sb := call(staff, "POST", "/api/v1/accounts/bulk", `{"action":"create","count":3,"label_prefix":"stf","data_quota_gb":1,"on_hold":true,"hold_duration_days":10,"node_id":"`+node.ID+`"}`)
	if code != 200 || sb["succeeded"].(float64) != 3 {
		t.Fatalf("staff bulk create: %d %v", code, sb)
	}
	var ids []any
	for _, r := range sb["results"].([]any) {
		ids = append(ids, r.(map[string]any)["id"])
	}
	idsJSON, _ := json.Marshal(ids)
	if code, out := call(staff, "POST", "/api/v1/accounts/bulk", `{"action":"delete","ids":`+string(idsJSON)+`}`); code != 200 || out["succeeded"].(float64) != 3 {
		t.Fatalf("staff bulk delete: %d %v", code, out)
	}
	_, _ = st.SoftDeleteAccount(ctx, foreign.ID, nil)
	_, _ = st.SoftDeleteAccount(ctx, onHoldID, nil)
}
