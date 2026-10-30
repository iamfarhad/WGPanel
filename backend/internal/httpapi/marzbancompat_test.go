package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wgpanel-api/internal/store"
)

func TestMzValidUsername(t *testing.T) {
	ok := []string{"abc", "user_1", "mine-user", "a_b_c", "abc@x.com", "abc.def", "ABC123", "_abc", "abc_", "ab__cd", strings.Repeat("a", 32), "user_name_2"}
	bad := []string{"", "ab", "my-user", "a.b", "a@b", "has space", "نام", strings.Repeat("a", 33), "abc/def", strings.Repeat("a", 30) + "@xyz.com"}
	for _, u := range ok {
		if !mzValidUsername(u) {
			t.Errorf("%q should be a valid Marzban username", u)
		}
	}
	for _, u := range bad {
		if mzValidUsername(u) {
			t.Errorf("%q should be rejected", u)
		}
	}
}

func TestMzStatus(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	reason := func(r string) *string { return &r }
	quota, hold := int64(100), 30
	cases := []struct {
		name string
		a    store.Account
		want string
	}{
		{"active", store.Account{Status: "active"}, "active"},
		{"manual suspend", store.Account{Status: "suspended", SuspendReason: reason("manual")}, "disabled"},
		{"abuse flag", store.Account{Status: "suspended", SuspendReason: reason("abuse_flag")}, "disabled"},
		{"device limit", store.Account{Status: "suspended", SuspendReason: reason("device_limit")}, "disabled"},
		{"expired suspend", store.Account{Status: "suspended", SuspendReason: reason("expired")}, "expired"},
		{"quota suspend", store.Account{Status: "suspended", SuspendReason: reason("quota_exceeded")}, "limited"},
		{"past expiry not yet reconciled", store.Account{Status: "active", ExpiryAt: &past}, "expired"},
		{"future expiry", store.Account{Status: "active", ExpiryAt: &future}, "active"},
		{"at cap not yet reconciled", store.Account{Status: "active", DataQuotaBytes: &quota, DataUsedBytes: 100}, "limited"},
		{"under cap", store.Account{Status: "active", DataQuotaBytes: &quota, DataUsedBytes: 99}, "active"},
		{"on hold", store.Account{Status: "active", HoldDurationDays: &hold}, "on_hold"},
		{"on hold but disabled", store.Account{Status: "suspended", SuspendReason: reason("manual"), HoldDurationDays: &hold}, "disabled"},
	}
	for _, c := range cases {
		if got := mzStatus(c.a, now); got != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMzSubscriptionURLMatchesBotRegexp(t *testing.T) {
	cases := []struct{ origin, token, want string }{
		{"https://panel.example.com", "tok", "https://panel.example.com/s/tok"},
		{"https://sub.example.com:8443", "tok", "https://sub.example.com:8443/s/tok"},
		// The bot prefixes its panel URL to anything its regexp rejects, so an IP or
		// localhost origin must come back relative instead of as a broken absolute URL.
		{"https://203.0.113.5", "tok", "/s/tok"},
		{"http://localhost:8090", "tok", "/s/tok"},
		{"", "tok", "/s/tok"},
	}
	for _, c := range cases {
		if got := mzSubscriptionURL(c.origin, c.token); got != c.want {
			t.Errorf("mzSubscriptionURL(%q) = %q, want %q", c.origin, got, c.want)
		}
	}
	// The per-node link keeps its query string out of the bot-regexp check.
	if got := mzAbsoluteOrRelative("https://panel.example.com", "/api/v1/sub/t?node_id=n"); got != "https://panel.example.com/api/v1/sub/t?node_id=n" {
		t.Errorf("node link = %q", got)
	}
}

func TestMzValidate(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }

	p, errs := mzValidate(mzUserBody{Expire: f(1900000000), DataLimit: f(5 << 30), OnHoldExpireDuration: f(30*86400 - 3), DataLimitResetStrategy: s("month"), Status: s("on_hold")}, "active", "on_hold")
	if len(errs) != 0 {
		t.Fatalf("valid body rejected: %+v", errs)
	}
	if *p.holdDays != 30 {
		t.Errorf("a duration 3s short of 30 days must round to 30 days, got %d", *p.holdDays)
	}
	if *p.resetStrategy != "month" || *p.dataLimit != 5<<30 || *p.expire != 1900000000 {
		t.Errorf("bad conversion: %+v", p)
	}
	if p, _ := mzValidate(mzUserBody{OnHoldExpireDuration: f(60)}); *p.holdDays != 1 {
		t.Errorf("sub-day duration should round up to 1 day, got %d", *p.holdDays)
	}
	if p, _ := mzValidate(mzUserBody{DataLimitResetStrategy: s("no_reset")}); *p.resetStrategy != "none" {
		t.Errorf("no_reset must map to none, got %q", *p.resetStrategy)
	}

	for name, b := range map[string]mzUserBody{
		"expire":                    {Expire: f(-1)},
		"data_limit":                {DataLimit: f(1.5)},
		"data_limit_reset_strategy": {DataLimitResetStrategy: s("hourly")},
		"status":                    {Status: s("limited")},
		"on_hold_timeout":           {OnHoldTimeout: s("tomorrow")},
	} {
		_, errs := mzValidate(b, "active", "on_hold")
		if len(errs) != 1 || errs[0].Loc[1] != name || errs[0].Loc[0] != "body" || errs[0].Type == "" {
			t.Errorf("%s: want one 422 entry located at body.%s, got %+v", name, name, errs)
		}
	}
}

func TestMzParseSortAndOrder(t *testing.T) {
	keys, msg := mzParseSort("-used_traffic,username")
	if msg != "" || len(keys) != 2 || !keys[0].desc || keys[1].desc || keys[0].field != "used_traffic" {
		t.Fatalf("parse: %+v %q", keys, msg)
	}
	if _, msg := mzParseSort("password"); msg == "" {
		t.Fatal("an unknown sort field must be refused")
	}
	ref := func(s string) *string { return &s }
	list := []store.Account{
		{ExternalRef: ref("b"), DataUsedBytes: 5},
		{ExternalRef: ref("a"), DataUsedBytes: 5},
		{ExternalRef: ref("c"), DataUsedBytes: 9},
	}
	mzSortAccounts(list, keys)
	if got := *list[0].ExternalRef + *list[1].ExternalRef + *list[2].ExternalRef; got != "cab" {
		t.Fatalf("order %q, want cab (used_traffic desc, then username asc)", got)
	}
}

func TestMzParseTime(t *testing.T) {
	for _, v := range []string{"2026-11-02T09:51:46", "2026-11-02T09:51:46Z", "2026-11-02T13:21:46+03:30", "2026-11-02 09:51:46", "1793613106"} {
		if _, ok := mzParseTime(v); !ok {
			t.Errorf("%q should parse", v)
		}
	}
	if _, ok := mzParseTime("soon"); ok {
		t.Error("garbage parsed")
	}
}

// TestMzTokenIsNotAnAdminToken: both are HS256 JWTs under the same JWTSecret family; a
// compat token must be useless against the admin API and the other way round.
func TestMzTokenIsolation(t *testing.T) {
	s := &Server{JWTSecret: "unit-secret"}
	key := store.APIKey{KeyID: "k1", SecretEncrypted: "enc-1"}
	tok, err := s.mzIssueToken(key)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.mzParseToken(tok)
	if err != nil || claims.Subject != "k1" || claims.KeyFP != mzKeyFingerprint(key) {
		t.Fatalf("round trip: %+v %v", claims, err)
	}
	if mzKeyFingerprint(store.APIKey{SecretEncrypted: "enc-2"}) == claims.KeyFP {
		t.Fatal("a rotated secret must change the fingerprint")
	}
	// Not valid as an admin token.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("compat token reached an admin handler") })).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("admin API answered %d to a compat token", rr.Code)
	}
	// And an admin-signed token is not a compat token.
	if _, err := s.mzParseToken("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.x"); err == nil {
		t.Fatal("garbage accepted as a compat token")
	}
}
