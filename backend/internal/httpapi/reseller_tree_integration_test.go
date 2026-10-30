package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
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
)

// pngBytes is the smallest thing http.DetectContentType calls a PNG; the logo path only
// sniffs the signature, it does not decode.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 32)...)

// TestResellerTreeEndToEnd drives the real router against Postgres for sub-resellers:
// carving through the API, subtree visibility for sessions and reseller-owned API keys,
// cascading disable, branding on the public subscription page, audit entries and backup
// coverage. Skipped without WGPANEL_TEST_POSTGRES_DSN.
func TestResellerTreeEndToEnd(t *testing.T) {
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
	pub := "tk-" + sfx
	node, err := st.CreateNode(ctx, "tree-"+sfx, "tree-"+sfx, "eu", "t.test:51820", "10.92.0.0/16", 500, &pub)
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
		_, _ = c.Exec(context.Background(), `DELETE FROM brand_profiles WHERE admin_id IS NULL`)
	})
	if _, err := conn.Exec(ctx, `DELETE FROM brand_profiles WHERE admin_id IS NULL`); err != nil {
		t.Fatal(err)
	}

	do := func(req *http.Request) (int, map[string]any) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	session := func(role, id string) string {
		tok, _ := authcrypto.IssueAccessToken(gateTestSecret, id, "u-"+id[:4], role)
		return tok
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
	staff := session("super_admin", "00000000-0000-0000-0000-00000000aaaa")

	mkRes := func(name string, p store.NewResellerParams) store.Reseller {
		p.Username, p.PasswordHash = name+sfx, "h"
		r, err := st.CreateReseller(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	ten, limit := 10, int64(100)<<30
	agent := mkRes("agent", store.NewResellerParams{MaxAccounts: &ten, DataLimitBytes: &limit})
	agentTok := session("reseller", agent.AdminID)

	// --- creating children through the API -------------------------------------------------
	code, out := bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub1-`+sfx+`","password":"password123","max_accounts":4,"data_limit_gb":40}`)
	if code != 201 || out["parent_id"] != agent.AdminID || out["depth"].(float64) != 2 {
		t.Fatalf("create child: %d %v", code, out)
	}
	sub1 := out["id"].(string)
	code, out = bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub2-`+sfx+`","password":"password123","max_accounts":4,"data_limit_gb":40}`)
	if code != 201 {
		t.Fatalf("create sibling: %d %v", code, out)
	}
	sub2 := out["id"].(string)
	if code, out = bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub3-`+sfx+`","password":"password123","max_accounts":3,"data_limit_gb":1}`); code != 409 || ecode(out) != "child_account_allocation_exceeded" {
		t.Fatalf("over-allocation: %d %v", code, out)
	}
	if code, out = bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub3-`+sfx+`","password":"password123","max_accounts":1,"data_limit_gb":30}`); code != 409 || ecode(out) != "child_credit_allocation_exceeded" {
		t.Fatalf("credit over-allocation: %d %v", code, out)
	}
	if code, out = bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub3-`+sfx+`","password":"password123"}`); code != 422 || ecode(out) != "child_limit_required" {
		t.Fatalf("unlimited child: %d %v", code, out)
	}
	// A client-supplied parent_id is ignored: the caller is always the parent.
	code, out = bearer(agentTok, "POST", "/api/v1/reseller/children",
		`{"username":"sub4-`+sfx+`","password":"password123","max_accounts":1,"data_limit_gb":1,"parent_id":"`+sub1+`"}`)
	if code != 201 || out["parent_id"] != agent.AdminID {
		t.Fatalf("parent_id override: %d %v", code, out)
	}
	if code, _ := bearer(staff, "POST", "/api/v1/reseller/children", `{}`); code != 403 {
		t.Fatalf("staff on the reseller-only route: %d", code)
	}

	sub1Tok, sub2Tok := session("reseller", sub1), session("reseller", sub2)
	// Grandchild, then the depth cap.
	code, out = bearer(sub1Tok, "POST", "/api/v1/reseller/children",
		`{"username":"gc-`+sfx+`","password":"password123","max_accounts":2,"data_limit_gb":10}`)
	if code != 201 || out["depth"].(float64) != 3 {
		t.Fatalf("grandchild: %d %v", code, out)
	}
	grand := out["id"].(string)
	grandTok := session("reseller", grand)
	if code, out = bearer(grandTok, "POST", "/api/v1/reseller/children",
		`{"username":"ggc-`+sfx+`","password":"password123","max_accounts":1,"data_limit_gb":1}`); code != 422 || ecode(out) != "reseller_too_deep" {
		t.Fatalf("depth cap: %d %v", code, out)
	}

	// Listing is the caller's own subtree only.
	_, list := bearer(agentTok, "GET", "/api/v1/reseller/children", "")
	if n := len(list["resellers"].([]any)); n != 4 { // sub1, sub2, sub4, grand
		t.Fatalf("agent sees %d descendants, want 4: %v", n, list)
	}
	_, list = bearer(sub2Tok, "GET", "/api/v1/reseller/children", "")
	if n := len(list["resellers"].([]any)); n != 0 {
		t.Fatalf("a leaf sees %d descendants", n)
	}
	// A sibling or a child cannot edit/delete another branch's reseller: 404, not 403.
	for name, tok := range map[string]string{"sibling": sub2Tok, "grandchild": grandTok} {
		if code, _ := bearer(tok, "PATCH", "/api/v1/reseller/children/"+sub1, `{"notes":"x"}`); code != 404 {
			t.Fatalf("%s patching sub1: %d, want 404", name, code)
		}
		if code, _ := bearer(tok, "DELETE", "/api/v1/reseller/children/"+sub1, ""); code != 404 {
			t.Fatalf("%s deleting sub1: %d, want 404", name, code)
		}
	}
	if code, _ := bearer(sub1Tok, "PATCH", "/api/v1/reseller/children/"+agent.AdminID, `{"notes":"x"}`); code != 404 {
		t.Fatalf("child patching its parent: %d, want 404", code)
	}
	// A parent may not reset a child's password, and carving applies on PATCH.
	if code, _ := bearer(agentTok, "PATCH", "/api/v1/reseller/children/"+sub1, `{"password":"newpassword1"}`); code != 403 {
		t.Fatalf("parent password reset: %d, want 403", code)
	}
	if code, out := bearer(agentTok, "PATCH", "/api/v1/reseller/children/"+sub1, `{"max_accounts":7}`); code != 409 || ecode(out) != "child_account_allocation_exceeded" {
		t.Fatalf("patch beyond the parent's room: %d %v", code, out)
	}
	if code, out := bearer(agentTok, "PATCH", "/api/v1/reseller/children/"+sub1, `{"notes":"hello"}`); code != 200 || out["notes"] != "hello" {
		t.Fatalf("plain patch: %d %v", code, out)
	}
	// Deleting a parent with children needs an explicit cascade.
	if code, out := bearer(staff, "DELETE", "/api/v1/resellers/"+sub1, ""); code != 409 || ecode(out) != "reseller_has_children" {
		t.Fatalf("delete with children: %d %v", code, out)
	}

	// --- account visibility ----------------------------------------------------------------
	quota := int64(1) << 30
	mkAcct := func(r string, label string) store.Account {
		ns := store.ResellerNamespace(r)
		a, err := st.CreateAccount(ctx, store.CreateAccountParams{
			Label: label, NodeIDOrAuto: node.ID, PublicKey: "pk-" + label + sfx, PrivateKeyEncrypted: "x", DataQuotaBytes: &quota,
			SubscriptionToken: "tok-" + label + sfx, OwnerKeyNamespace: &ns,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	aAgent, aSub1, aSub2, aGrand := mkAcct(agent.AdminID, "agent"), mkAcct(sub1, "sub1"), mkAcct(sub2, "sub2"), mkAcct(grand, "grand")
	// Reseller credit limits require quotas; the store call above ran without a credit
	// check only because limits are unlimited at the agent: accounts already exist, fine.

	status := func(tok, id string) int { c, _ := bearer(tok, "GET", "/api/v1/accounts/"+id, ""); return c }
	for _, c := range []struct {
		viewer string
		tok    string
		acct   store.Account
		want   int
	}{
		{"agent", agentTok, aSub1, 200}, {"agent", agentTok, aGrand, 200}, {"agent", agentTok, aSub2, 200},
		{"sub1", sub1Tok, aGrand, 200}, {"sub1", sub1Tok, aAgent, 404}, {"sub1", sub1Tok, aSub2, 404},
		{"sub2", sub2Tok, aSub1, 404}, {"sub2", sub2Tok, aAgent, 404}, {"sub2", sub2Tok, aGrand, 404},
		{"grand", grandTok, aSub1, 404}, {"grand", grandTok, aAgent, 404},
	} {
		if got := status(c.tok, c.acct.ID); got != c.want {
			t.Errorf("%s GET %s: %d, want %d", c.viewer, c.acct.Label, got, c.want)
		}
	}
	_, accs := bearer(agentTok, "GET", "/api/v1/accounts", "")
	if n := len(accs["accounts"].([]any)); n != 4 {
		t.Fatalf("agent lists %d accounts, want its own + all descendants (4)", n)
	}
	_, accs = bearer(sub2Tok, "GET", "/api/v1/accounts", "")
	if n := len(accs["accounts"].([]any)); n != 1 {
		t.Fatalf("leaf lists %d accounts, want 1", n)
	}
	// Writes on a descendant's account: suspend/enable yes, everything else 404.
	if code, _ := bearer(agentTok, "POST", "/api/v1/accounts/"+aGrand.ID+"/suspend", ""); code != 200 {
		t.Fatalf("parent suspend of a grandchild's account: %d", code)
	}
	if code, _ := bearer(agentTok, "POST", "/api/v1/accounts/"+aGrand.ID+"/enable", ""); code != 200 {
		t.Fatalf("parent enable: %d", code)
	}
	for _, w := range []struct{ m, p, body string }{
		{"PATCH", "/api/v1/accounts/" + aSub1.ID, `{"label":"x"}`},
		{"DELETE", "/api/v1/accounts/" + aSub1.ID, ""},
		{"POST", "/api/v1/accounts/" + aSub1.ID + "/renew", `{"add_quota_gb":1}`},
		{"GET", "/api/v1/accounts/" + aSub1.ID + "/config", ""},
	} {
		if code, _ := bearer(agentTok, w.m, w.p, w.body); code != 404 {
			t.Errorf("parent %s %s: %d, want 404", w.m, w.p, code)
		}
	}
	if code, _ := bearer(sub1Tok, "POST", "/api/v1/accounts/"+aAgent.ID+"/suspend", ""); code != 404 {
		t.Fatalf("child suspending its parent's account: %d, want 404", code)
	}

	// --- reseller-owned API keys across the subtree ----------------------------------------
	mkKey := func(owner string, perms ...string) (string, string) {
		keyID := "key-" + owner[:8] + "-" + sfx
		if _, err := st.CreateAPIKey(ctx, keyID, mustEnc(t, "secret-"+keyID), "k", []string{}, perms, &owner); err != nil {
			t.Fatal(err)
		}
		return keyID, "secret-" + keyID
	}
	pk, ps := mkKey(agent.AdminID, "read", "update", "suspend", "delete")
	ck, cs := mkKey(sub2, "read", "update", "suspend", "delete")
	if code, _ := signed(pk, ps, "GET", "/api/v1/accounts/"+aGrand.ID, ""); code != 200 {
		t.Fatalf("parent's key reading a grandchild's account: %d", code)
	}
	if code, _ := signed(pk, ps, "POST", "/api/v1/accounts/"+aSub1.ID+"/suspend", ""); code != 200 {
		t.Fatalf("parent's key suspending a child's account: %d", code)
	}
	if code, _ := signed(pk, ps, "POST", "/api/v1/accounts/"+aSub1.ID+"/enable", ""); code != 200 {
		t.Fatalf("parent's key enabling it: %d", code)
	}
	if code, _ := signed(pk, ps, "DELETE", "/api/v1/accounts/"+aSub1.ID, ""); code != 404 {
		t.Fatalf("parent's key deleting a child's account: %d, want 404", code)
	}
	if code, _ := signed(pk, ps, "PATCH", "/api/v1/accounts/"+aSub1.ID, `{"label":"x"}`); code != 404 {
		t.Fatalf("parent's key editing a child's account: %d, want 404", code)
	}
	_, keyList := signed(pk, ps, "GET", "/api/v1/accounts", "")
	if n := len(keyList["accounts"].([]any)); n != 4 {
		t.Fatalf("parent's key lists %d accounts, want 4", n)
	}
	for name, id := range map[string]string{"parent": aAgent.ID, "sibling": aSub1.ID, "grandchild": aGrand.ID} {
		if code, _ := signed(ck, cs, "GET", "/api/v1/accounts/"+id, ""); code != 404 {
			t.Fatalf("leaf's key reading the %s account: %d, want 404", name, code)
		}
		if code, _ := signed(ck, cs, "POST", "/api/v1/accounts/"+id+"/suspend", ""); code != 404 {
			t.Fatalf("leaf's key suspending the %s account: %d, want 404", name, code)
		}
	}
	if _, keyList = signed(ck, cs, "GET", "/api/v1/accounts", ""); len(keyList["accounts"].([]any)) != 1 {
		t.Fatalf("leaf's key lists %v", keyList)
	}

	// --- reports roll up under the parent -------------------------------------------------
	_, rep := bearer(agentTok, "GET", "/api/v1/reports?range=24h", "")
	if tot := rep["totals"].(map[string]any); tot["accounts_total"].(float64) != 4 {
		t.Fatalf("agent report totals: %v", tot)
	}
	rows, _ := rep["resellers"].([]any)
	if len(rows) != 5 { // agent + 4 descendants
		t.Fatalf("agent report breakdown has %d rows: %v", len(rows), rep["resellers"])
	}
	first := rows[0].(map[string]any)
	if first["id"] != agent.AdminID || first["subtree_accounts"].(float64) != 4 {
		t.Fatalf("breakdown should start with the agent and roll its subtree up: %v", first)
	}
	_, rep = bearer(sub2Tok, "GET", "/api/v1/reports?range=24h", "")
	if rep["resellers"] != nil || rep["totals"].(map[string]any)["accounts_total"].(float64) != 1 {
		t.Fatalf("leaf report: %v", rep)
	}
	_, rep = bearer(staff, "GET", "/api/v1/reports?range=24h", "")
	var sawGrand bool
	for _, r := range rep["resellers"].([]any) {
		if m := r.(map[string]any); m["id"] == grand && m["depth"].(float64) == 3 && m["parent_id"] == sub1 {
			sawGrand = true
		}
	}
	if !sawGrand {
		t.Fatal("super admin report should list the whole tree with depth and parent")
	}

	// --- branding -------------------------------------------------------------------------
	info := func(token string) map[string]any {
		req := httptest.NewRequest("GET", "/api/v1/sub/"+token+"/info", nil)
		code, out := do(req)
		if code != 200 {
			t.Fatalf("info %s: %d %v", token, code, out)
		}
		return out
	}
	brandName := func(token string) any {
		b, _ := info(token)["brand"].(map[string]any)
		if b == nil {
			return nil
		}
		return b["name"]
	}
	// Nothing configured anywhere: unbranded page.
	if b := info(aGrand.SubscriptionToken)["brand"]; b != nil {
		t.Fatalf("expected no brand, got %v", b)
	}
	// Global default (super admin), shown for admin-owned accounts and as the fallback.
	if code, out := bearer(staff, "PUT", "/api/v1/branding",
		`{"brand_name":"Owner Co","support_link":"@owner_support","announcement":"Global notice","accent_color":"#112233"}`); code != 200 {
		t.Fatalf("global branding: %d %v", code, out)
	}
	adminAcct, err := st.CreateAccount(ctx, store.CreateAccountParams{
		Label: "adm", NodeIDOrAuto: node.ID, PublicKey: "pk-adm" + sfx, PrivateKeyEncrypted: "x", SubscriptionToken: "tok-adm" + sfx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if brandName(adminAcct.SubscriptionToken) != "Owner Co" || brandName(aGrand.SubscriptionToken) != "Owner Co" {
		t.Fatal("global brand should apply to admin-owned accounts and as a fallback")
	}
	if code, _ := bearer(agentTok, "PUT", "/api/v1/branding", `{"brand_name":"x"}`); code != 403 {
		t.Fatalf("reseller editing the global brand: %d, want 403", code)
	}
	if code, _ := bearer(agentTok, "GET", "/api/v1/branding", ""); code != 403 {
		t.Fatalf("reseller reading the global brand: %d, want 403", code)
	}
	// Reseller branding: sub1 brands itself with an uploaded logo; its grandchild inherits.
	logo := base64.StdEncoding.EncodeToString(pngBytes)
	code, out = bearer(sub1Tok, "PUT", "/api/v1/reseller/branding",
		`{"brand_name":"Sub One","support_link":"t.me/sub_one_help","announcement":"Hi from sub one","accent_color":"#aabbcc","logo_base64":"data:image/png;base64,`+logo+`"}`)
	if code != 200 || out["has_logo_upload"] != true {
		t.Fatalf("reseller branding: %d %v", code, out)
	}
	if brandName(aSub1.SubscriptionToken) != "Sub One" || brandName(aGrand.SubscriptionToken) != "Sub One" {
		t.Fatal("grandchild should inherit its nearest branded ancestor")
	}
	if brandName(aSub2.SubscriptionToken) != "Owner Co" || brandName(aAgent.SubscriptionToken) != "Owner Co" {
		t.Fatal("siblings and parents must not pick up another branch's brand")
	}
	b := info(aGrand.SubscriptionToken)["brand"].(map[string]any)
	if b["support_url"] != "https://t.me/sub_one_help" || b["accent_color"] != "#aabbcc" || b["announcement"] != "Hi from sub one" {
		t.Fatalf("brand payload: %v", b)
	}
	raw, _ := json.Marshal(info(aGrand.SubscriptionToken))
	for _, leak := range []string{"Owner Co", "agent" + sfx, "sub2-" + sfx, "reseller:", agent.AdminID, "@owner_support"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("branded info leaks %q: %s", leak, raw)
		}
	}
	// The unbranded-reseller page never falls back to the owner's support contact either.
	if c := info(aSub1.SubscriptionToken)["support_contact"]; c != "t.me/sub_one_help" && c != "https://t.me/sub_one_help" {
		t.Fatalf("branded support_contact = %v", c)
	}
	// Logo bytes: served under the account's own token with hardening headers.
	req := httptest.NewRequest("GET", b["logo_url"].(string), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!bytes.Equal(rr.Body.Bytes(), pngBytes) {
		t.Fatalf("logo: %d %v", rr.Code, rr.Header())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/sub/"+aSub2.SubscriptionToken+"/logo", nil))
	if rr.Code != 404 {
		t.Fatalf("logo for an account whose brand has none: %d", rr.Code)
	}
	// Validation: SVG, oversized logos, javascript: links, bad colors.
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if code, _ := bearer(sub1Tok, "PUT", "/api/v1/reseller/branding", `{"brand_name":"x","logo_base64":"`+svg+`"}`); code != 400 {
		t.Fatalf("svg logo: %d, want 400", code)
	}
	huge := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, store.MaxLogoBytes+1)...))
	if code, _ := bearer(sub1Tok, "PUT", "/api/v1/reseller/branding", `{"brand_name":"x","logo_base64":"`+huge+`"}`); code != 400 {
		t.Fatalf("oversized logo: %d, want 400", code)
	}
	for _, bad := range []string{
		`{"support_link":"javascript:alert(1)"}`, `{"accent_color":"blue"}`, `{"logo_url":"http://x.example/l.png"}`,
		`{"brand_name":"` + strings.Repeat("x", 41) + `"}`, `{"announcement":"` + strings.Repeat("x", 281) + `"}`,
	} {
		if code, _ := bearer(sub1Tok, "PUT", "/api/v1/reseller/branding", bad); code != 400 {
			t.Fatalf("branding %s: %d, want 400", bad, code)
		}
	}
	// A reseller's save keeps the logo unless asked to remove it; removing falls back.
	if code, out := bearer(sub1Tok, "PUT", "/api/v1/reseller/branding", `{"brand_name":"Sub One v2"}`); code != 200 || out["has_logo_upload"] != true {
		t.Fatalf("logo should survive a text-only save: %d %v", code, out)
	}
	if code, out := bearer(sub1Tok, "PUT", "/api/v1/reseller/branding", `{"remove_logo":true}`); code != 200 || out["inherited_from"] != "global" {
		t.Fatalf("blank profile: %d %v", code, out)
	}
	if brandName(aGrand.SubscriptionToken) != "Owner Co" {
		t.Fatal("a blank profile should hand the page back to the global brand")
	}
	// Staff can edit a reseller's profile; the page follows.
	if code, _ := bearer(staff, "PUT", "/api/v1/resellers/"+agent.AdminID+"/branding", `{"brand_name":"Agent Brand"}`); code != 200 {
		t.Fatalf("staff editing a reseller's brand: %d", code)
	}
	if brandName(aGrand.SubscriptionToken) != "Agent Brand" || brandName(aSub2.SubscriptionToken) != "Agent Brand" {
		t.Fatal("the whole agent subtree should inherit the agent's brand")
	}

	// --- cascade: a disabled parent shuts its subtree's sessions and keys ------------------
	if code, _ := bearer(agentTok, "PATCH", "/api/v1/reseller/children/"+sub2, `{"disabled":true}`); code != 200 {
		t.Fatalf("parent disabling a child: %d", code)
	}
	if code, out := bearer(sub2Tok, "GET", "/api/v1/reseller/me", ""); code != 401 || ecode(out) != "account_disabled" {
		t.Fatalf("disabled child session: %d %v", code, out)
	}
	if code, out := signed(ck, cs, "GET", "/api/v1/accounts", ""); code != 401 || ecode(out) != "reseller_disabled" {
		t.Fatalf("disabled child key: %d %v", code, out)
	}
	if code, _ := bearer(agentTok, "PATCH", "/api/v1/reseller/children/"+sub2, `{"disabled":false}`); code != 200 {
		t.Fatalf("parent re-enabling: %d", code)
	}
	if code, _ := bearer(staff, "PATCH", "/api/v1/resellers/"+agent.AdminID, `{"disabled":true}`); code != 200 {
		t.Fatalf("staff disabling the agent: %d", code)
	}
	if code, out := bearer(grandTok, "GET", "/api/v1/reseller/me", ""); code != 401 || ecode(out) != "account_disabled" {
		t.Fatalf("grandchild under a disabled agent: %d %v", code, out)
	}
	if code, out := signed(ck, cs, "GET", "/api/v1/accounts", ""); code != 401 {
		t.Fatalf("child key under a disabled agent: %d %v", code, out)
	}
	_, me := bearer(staff, "GET", "/api/v1/resellers/"+grand, "")
	if me["effective_disabled"] != true || me["disabled"] != false {
		t.Fatalf("grandchild state: %v", me)
	}
	if code, _ := bearer(staff, "PATCH", "/api/v1/resellers/"+agent.AdminID, `{"disabled":false}`); code != 200 {
		t.Fatal("re-enable agent")
	}
	if code, out := bearer(grandTok, "GET", "/api/v1/reseller/me", ""); code != 200 || out["depth"].(float64) != 3 {
		t.Fatalf("grandchild after re-enable: %d %v", code, out)
	}
	// Plan expiry cascades too.
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if err := st.UpdateResellerLimits(ctx, agent.AdminID, store.ResellerLimitsPatch{ExpiresAt: store.Opt[time.Time]{Set: true, Value: ptrTime(t, past)}}); err != nil {
		t.Fatal(err)
	}
	if code, out := bearer(grandTok, "GET", "/api/v1/reseller/me", ""); code != 401 || ecode(out) != "reseller_expired" {
		t.Fatalf("grandchild under an expired agent: %d %v", code, out)
	}
	if err := st.UpdateResellerLimits(ctx, agent.AdminID, store.ResellerLimitsPatch{ExpiresAt: store.Opt[time.Time]{Set: true}}); err != nil {
		t.Fatal(err)
	}

	// --- audit trail and backup coverage ---------------------------------------------------
	_, audit := bearer(staff, "GET", "/api/v1/audit-log?limit=200", "")
	seen := map[string]bool{}
	for _, e := range audit["entries"].([]any) {
		seen[e.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"reseller.child_created", "reseller.child_updated", "branding.updated"} {
		if !seen[want] {
			t.Errorf("audit log has no %s entry", want)
		}
	}
	dump, err := st.DumpTables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"reseller_limits", "brand_profiles"} {
		if _, ok := dump[table]; !ok || !strings.Contains(string(dump[table]), sfx[:6]) && table == "reseller_limits" {
			t.Errorf("backup is missing %s", table)
		}
	}

	// --- subtree delete --------------------------------------------------------------------
	// Everything under sub1 owns accounts, so even a confirmed cascade is refused.
	if code, out := bearer(staff, "DELETE", "/api/v1/resellers/"+sub1+"?cascade=true", ""); code != 409 || ecode(out) != "reseller_has_accounts" {
		t.Fatalf("cascade with accounts: %d %v", code, out)
	}
	for _, a := range []store.Account{aSub1, aGrand} {
		if _, err := st.SoftDeleteAccount(ctx, a.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if code, _ := bearer(staff, "DELETE", "/api/v1/resellers/"+sub1+"?cascade=true", ""); code != 204 {
		t.Fatal("cascade delete should succeed once the subtree has no accounts")
	}
	if _, err := st.GetResellerLimits(ctx, grand); err == nil {
		t.Fatal("grandchild survived its ancestor's cascade delete")
	}
}

func ptrTime(t *testing.T, rfc string) *time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}
