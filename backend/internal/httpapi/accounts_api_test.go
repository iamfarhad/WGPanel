package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

// These tests need no database: every case either is refused before the handler
// touches the store, or only has to prove it got past the gate (a handler that needs a
// store panics on the nil one, which doAs reports as "reached").

func adminToken(t *testing.T, role string) string {
	t.Helper()
	tok, err := authcrypto.IssueAccessToken(gateTestSecret, "aid-"+role, "user-"+role, role)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// call sends one request through the full router. reached=true means the request got
// past auth, the reseller gate and validation into a handler that needed the nil store.
func call(s *Server, token, method, path, body string) (code int, errCode string, reached bool) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	defer func() {
		if recover() != nil {
			code, errCode, reached = 0, "", true
		}
	}()
	s.Routes().ServeHTTP(rr, req)
	var env errorEnvelope
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	return rr.Code, env.Error.Code, false
}

func TestListAccountsRejectsInvalidOffset(t *testing.T) {
	s := resellerTestServer(false)
	for _, role := range []string{"super_admin", "operator", "support"} {
		tok := adminToken(t, role)
		for _, bad := range []string{"-1", "abc", "1.5", "", "99999999999999999999", "1e3", "+-2"} {
			code, ecode, reached := call(s, tok, "GET", "/api/v1/accounts?offset="+bad, "")
			if reached || code != http.StatusBadRequest || ecode != "invalid_request" {
				t.Errorf("%s offset=%q: got code=%d error=%q reached=%v, want 400 invalid_request", role, bad, code, ecode, reached)
			}
		}
	}
}

func TestListAccountsAcceptsValidOffsetAndLeavesLimitLenient(t *testing.T) {
	s := resellerTestServer(false)
	tok := adminToken(t, "operator")
	for _, q := range []string{"offset=0", "offset=7", "offset=0&limit=2", "limit=abc", "limit=9999", "offset=3&limit=0"} {
		if code, ecode, reached := call(s, tok, "GET", "/api/v1/accounts?"+q, ""); !reached {
			t.Errorf("?%s: refused before the store (code=%d error=%q); only a bad offset may be refused", q, code, ecode)
		}
	}
	// With external_ref the paging parameters are ignored, not validated.
	if code, ecode, reached := call(s, tok, "GET", "/api/v1/accounts?external_ref=abc&offset=nope&limit=-3", ""); !reached {
		t.Errorf("external_ref lookup validated paging params: code=%d error=%q", code, ecode)
	}
}

func TestUpdateAccountClearFlagsCannotCarryAValue(t *testing.T) {
	s := resellerTestServer(false)
	tok := adminToken(t, "operator")
	refused := []string{
		`{"clear_data_quota":true,"data_quota_gb":5}`,
		`{"clear_data_quota":true,"data_quota_gb":0}`, // a zero quota is still a value, not "null"
		`{"clear_device_limit":true,"device_limit":2}`,
		`{"clear_device_limit":true,"device_limit":0}`,
		`{"clear_data_quota":true,"clear_device_limit":true,"data_quota_gb":1}`,
		`{"label":"x","clear_device_limit":true,"device_limit":1}`,
	}
	for _, body := range refused {
		code, ecode, reached := call(s, tok, "PATCH", "/api/v1/accounts/x", body)
		if reached || code != http.StatusBadRequest || ecode != "invalid_request" {
			t.Errorf("%s: got code=%d error=%q reached=%v, want 400 invalid_request", body, code, ecode, reached)
		}
	}

	notRefused := []string{
		`{"clear_data_quota":true}`,
		`{"clear_device_limit":true}`,
		`{"clear_data_quota":true,"clear_device_limit":true}`,
		`{"clear_data_quota":true,"data_quota_gb":null}`, // null = absent
		`{"clear_device_limit":true,"device_limit":null}`,
		`{"clear_data_quota":false,"data_quota_gb":5}`, // false = no effect
		`{"clear_device_limit":false,"device_limit":2}`,
		`{"clear_data_quota":null,"data_quota_gb":5}`,
		`{"data_quota_gb":5,"device_limit":2}`, // existing behaviour, no flags
		`{"label":"x"}`,
	}
	for _, body := range notRefused {
		if code, ecode, reached := call(s, tok, "PATCH", "/api/v1/accounts/x", body); !reached {
			t.Errorf("%s: refused before the store (code=%d error=%q)", body, code, ecode)
		}
	}

	// Not a boolean -> the whole body is malformed.
	if code, ecode, reached := call(s, tok, "PATCH", "/api/v1/accounts/x", `{"clear_data_quota":"yes"}`); reached || code != http.StatusBadRequest || ecode != "invalid_request" {
		t.Errorf("non-boolean flag: code=%d error=%q reached=%v", code, ecode, reached)
	}
}

func TestResetUsageRouteProtection(t *testing.T) {
	s := resellerTestServer(false)
	path := "/api/v1/accounts/x/reset-usage"

	if code, _, reached := call(s, "", "POST", path, ""); reached || code != http.StatusUnauthorized {
		t.Errorf("no credentials: code=%d reached=%v, want 401", code, reached)
	}
	if code, ecode, reached := call(s, adminToken(t, "support"), "POST", path, ""); reached || code != http.StatusForbidden || ecode != "forbidden" {
		t.Errorf("support (read-only) role: code=%d error=%q reached=%v, want 403 forbidden", code, ecode, reached)
	}
	// Operators and super admins get in, whether the body is absent, {} or junk (the
	// endpoint takes no input).
	for _, role := range []string{"operator", "super_admin"} {
		for _, body := range []string{"", "{}", `{"anything":1}`} {
			if code, ecode, reached := call(s, adminToken(t, role), "POST", path, body); !reached {
				t.Errorf("%s body=%q: refused (code=%d error=%q), want it to reach the handler", role, body, code, ecode)
			}
		}
	}
	// A reseller *session* is deliberately not allowlisted (see server.go): its credit
	// counts quota handed out, so a self-service usage reset would re-sell the same credit.
	if code, _, reached := call(s, adminToken(t, "reseller"), "POST", path, ""); reached || code != http.StatusForbidden {
		t.Errorf("reseller session: code=%d reached=%v, want 403", code, reached)
	}
	if resellerRoutes["POST /api/v1/accounts/{id}/reset-usage"] {
		t.Error("reset-usage must not be on the reseller session allowlist")
	}
	// POST only.
	if code, _, _ := call(s, adminToken(t, "operator"), "GET", path, ""); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Errorf("GET reset-usage: code=%d, want 405/404", code)
	}
}

// The brief for reset-usage is "mirror the renew route's protection exactly". The API
// key side is a middleware chain, not data, so pin it by comparing the two
// registrations with the handler name factored out.
func TestResetUsageMirrorsRenewProtection(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	chain := func(route string) string {
		re := regexp.MustCompile(`mux\.Handle\("` + regexp.QuoteMeta(route) + `", (.+)\)\n`)
		m := re.FindStringSubmatch(string(src))
		if m == nil {
			t.Fatalf("route %q not registered in server.go", route)
		}
		return regexp.MustCompile(`http\.HandlerFunc\(s\.handle\w+\)`).ReplaceAllString(m[1], "H")
	}
	renew, reset := chain("POST /api/v1/accounts/{id}/renew"), chain("POST /api/v1/accounts/{id}/reset-usage")
	if renew != reset {
		t.Fatalf("reset-usage is protected differently from renew:\n renew:  %s\n reset:  %s", renew, reset)
	}
	if !strings.Contains(reset, `requirePermission("update"`) {
		t.Fatalf("reset-usage must require the API key `update` permission: %s", reset)
	}
}

func TestAccountResponseCarriesLastHandshakeAt(t *testing.T) {
	base := store.Account{ID: "a", Label: "l", Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}

	marshal := func(a store.Account) map[string]any {
		t.Helper()
		raw, err := json.Marshal(toAccountResponse(a, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := marshal(base)
	v, present := got["last_handshake_at"]
	if !present || v != nil {
		t.Fatalf("no handshake: last_handshake_at present=%v value=%v, want present and null", present, v)
	}

	at := time.Date(2026, 10, 29, 12, 30, 45, 0, time.UTC)
	base.LastHandshakeAt = &at
	got = marshal(base)
	if got["last_handshake_at"] != "2026-10-29T12:30:45Z" {
		t.Fatalf("last_handshake_at = %v, want 2026-10-29T12:30:45Z", got["last_handshake_at"])
	}
	// The account-level value is its own field, independent of the (empty) peers list.
	if peers, _ := got["peers"].([]any); len(peers) != 0 {
		t.Fatalf("peers = %v", got["peers"])
	}
}
