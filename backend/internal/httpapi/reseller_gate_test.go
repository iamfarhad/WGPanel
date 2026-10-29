package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

const gateTestSecret = "test-secret"

func resellerTestServer(disabled bool) *Server {
	return &Server{
		JWTSecret: gateTestSecret,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResellerLimitsLookup: func(ctx context.Context, id string) (store.ResellerLimits, error) {
			return store.ResellerLimits{AdminID: id, Disabled: disabled}, nil
		},
	}
}

// routePatterns reads every mux pattern registered in server.go, so a newly added
// route can't slip past the allowlist test unclassified.
func routePatterns(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+ [^"]+)"`)
	var out []string
	// Routes live in server.go and in feature files that register their own groups.
	for _, file := range []string{"server.go", "notify_api.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) < 20 {
		t.Fatalf("only found %d routes; regexp stale?", len(out))
	}
	return out
}

// doAs sends a request through the full Routes() tree. Allowed routes reach handlers
// that need a Store we don't have here, so a panic means "passed the gate".
func doAs(s *Server, token, method, path string) (code int, reachedHandler bool) {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	defer func() {
		if recover() != nil {
			code, reachedHandler = 0, true
		}
	}()
	s.Routes().ServeHTTP(rr, req)
	return rr.Code, rr.Code != http.StatusForbidden && rr.Code != http.StatusUnauthorized
}

func TestResellerAllowlist(t *testing.T) {
	s := resellerTestServer(false)
	token, err := authcrypto.IssueAccessToken(gateTestSecret, "rid", "res", "reseller")
	if err != nil {
		t.Fatal(err)
	}
	public := map[string]bool{
		"GET /healthz": true, "POST /api/v1/auth/login": true, "POST /api/v1/auth/refresh": true,
		"POST /api/v1/nodes/join": true, "GET /api/v1/sub/{token}": true, "GET /api/v1/sub/{token}/nodes": true,
	}
	for _, pattern := range routePatterns(t) {
		if public[pattern] || strings.Contains(pattern, "/internal/") {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		path = strings.NewReplacer("{id}", "x", "{delivery_id}", "y").Replace(path)
		_, reached := doAs(s, token, method, path)
		if want := resellerRoutes[pattern]; reached != want {
			t.Errorf("%s: reseller reached handler = %v, allowlist says %v", pattern, reached, want)
		}
	}
	for pattern := range resellerRoutes {
		found := false
		for _, p := range routePatterns(t) {
			found = found || p == pattern
		}
		if !found {
			t.Errorf("allowlist entry %q matches no registered route", pattern)
		}
	}
}

func TestResellerDeniedMutatingRoutesAre403(t *testing.T) {
	s := resellerTestServer(false)
	token, _ := authcrypto.IssueAccessToken(gateTestSecret, "rid", "res", "reseller")
	for _, c := range []struct{ m, p string }{
		{"POST", "/api/v1/nodes"}, {"PATCH", "/api/v1/nodes/x"}, {"POST", "/api/v1/nodes/x/join-token"},
		{"GET", "/api/v1/nodes/x/metrics"}, {"GET", "/api/v1/settings"}, {"PATCH", "/api/v1/settings"},
		{"GET", "/api/v1/admins"}, {"GET", "/api/v1/audit-log"},
		{"POST", "/api/v1/backup"}, {"GET", "/api/v1/resellers"}, {"POST", "/api/v1/resellers/x/suspend-accounts"},
	} {
		if code, _ := doAs(s, token, c.m, c.p); code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", c.m, c.p, code)
		}
	}
}

func TestDisabledResellerIsRejectedImmediately(t *testing.T) {
	s := resellerTestServer(true)
	token, _ := authcrypto.IssueAccessToken(gateTestSecret, "rid", "res", "reseller")
	if code, _ := doAs(s, token, "GET", "/api/v1/accounts"); code != http.StatusUnauthorized {
		t.Fatalf("disabled reseller with a valid token: got %d, want 401", code)
	}
}

func TestStaffRolesUnaffectedByResellerGate(t *testing.T) {
	s := resellerTestServer(false)
	token, _ := authcrypto.IssueAccessToken(gateTestSecret, "aid", "boss", "super_admin")
	if code, reached := doAs(s, token, "GET", "/api/v1/audit-log"); !reached {
		t.Fatalf("super_admin blocked on audit-log: %d", code)
	}
}

func TestCanSeeNodeGroup(t *testing.T) {
	reseller := &CallerIdentity{IsAdmin: true, ResellerID: "r", NodeGroups: []string{"eu"}}
	allGroups := &CallerIdentity{IsAdmin: true, ResellerID: "r"}
	staff := &CallerIdentity{IsAdmin: true}
	key := &CallerIdentity{NodeGroups: []string{"eu"}}
	cases := []struct {
		id   *CallerIdentity
		g    string
		want bool
	}{
		{reseller, "eu", true}, {reseller, "us", false}, {allGroups, "us", true},
		{staff, "us", true}, {key, "eu", true}, {key, "us", false},
	}
	for i, c := range cases {
		if got := canSeeNodeGroup(c.id, c.g); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}
