package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"wgpanel-api/internal/authcrypto"
)

func TestAPIKeyRoutesStaffBelowSuperAdminStillDenied(t *testing.T) {
	s := resellerTestServer(false)
	keyRoutes := []struct{ m, p string }{
		{"GET", "/api/v1/api-keys"}, {"POST", "/api/v1/api-keys"},
		{"POST", "/api/v1/api-keys/x/revoke"}, {"POST", "/api/v1/api-keys/x/rotate"},
	}
	for _, role := range []string{"operator", "support"} {
		token, _ := authcrypto.IssueAccessToken(gateTestSecret, "aid", "u", role)
		for _, c := range keyRoutes {
			if code, _ := doAs(s, token, c.m, c.p); code != http.StatusForbidden {
				t.Errorf("%s %s %s: got %d, want 403", role, c.m, c.p, code)
			}
		}
	}
	token, _ := authcrypto.IssueAccessToken(gateTestSecret, "rid", "res", "reseller")
	for _, c := range keyRoutes {
		if _, reached := doAs(s, token, c.m, c.p); !reached {
			t.Errorf("reseller blocked on %s %s", c.m, c.p)
		}
	}
}

func TestEffectiveKeyGroups(t *testing.T) {
	cases := []struct {
		reseller, key, want []string
	}{
		{[]string{"eu", "us"}, nil, []string{"eu", "us"}},
		{nil, []string{"eu"}, []string{"eu"}},
		{[]string{"eu", "us"}, []string{"us", "ap"}, []string{"us"}},
		{[]string{"eu"}, []string{"us"}, []string{""}},
		{nil, nil, nil},
	}
	for i, c := range cases {
		got := effectiveKeyGroups(c.reseller, c.key)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}
