package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1, secret "12345678901234567890": T=59s -> 94287082 (8 digits),
// so the 6-digit code is 287082.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPRFC6238Vector(t *testing.T) {
	code, err := totpCodeAt(rfcSecret, 59/30)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("code = %s, want 287082", code)
	}
	step, ok := verifyTOTP(rfcSecret, "287082", time.Unix(59, 0))
	if !ok || step != 1 {
		t.Fatalf("verify = %d,%v", step, ok)
	}
	// One step of clock skew is tolerated, two are not.
	if _, ok := verifyTOTP(rfcSecret, "287082", time.Unix(59+30, 0)); !ok {
		t.Error("expected +-1 step skew to be accepted")
	}
	if _, ok := verifyTOTP(rfcSecret, "287082", time.Unix(59+90, 0)); ok {
		t.Error("expected a code 3 steps old to be rejected")
	}
	if _, ok := verifyTOTP(rfcSecret, "12345", time.Unix(59, 0)); ok {
		t.Error("short code must be rejected")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := totpURI("Jirjirak WireGuard", "alice", "ABC")
	for _, want := range []string{"otpauth://totp/Jirjirak%20WireGuard:alice?", "secret=ABC", "issuer=Jirjirak+WireGuard", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri %q missing %q", uri, want)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil || len(codes) != recoveryCodeCount || len(hashes) != recoveryCodeCount {
		t.Fatalf("codes=%d hashes=%d err=%v", len(codes), len(hashes), err)
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 11 || c[5] != '-' {
			t.Errorf("unexpected code shape %q", c)
		}
		if hashRecoveryCode(c) != hashes[i] {
			t.Error("hash mismatch")
		}
		if seen[c] {
			t.Error("duplicate code")
		}
		seen[c] = true
	}
	// Case, dashes and spaces are forgiven when typing a code.
	if hashRecoveryCode(strings.ToUpper(codes[0])) != hashRecoveryCode(strings.Replace(codes[0], "-", " ", 1)) {
		t.Error("normalisation should ignore case and separators")
	}
}

func TestClientIP(t *testing.T) {
	mk := func(remote, xff string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return clientIP(r)
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:5555", "", "203.0.113.9"},
		// A public peer cannot spoof the header.
		{"203.0.113.9:5555", "198.51.100.1", "203.0.113.9"},
		// Behind the proxy (private peer) the forwarded client is used - last element.
		{"172.18.0.5:4444", "198.51.100.1", "198.51.100.1"},
		{"172.18.0.5:4444", "1.1.1.1, 198.51.100.7", "198.51.100.7"},
		{"127.0.0.1:1", "2001:db8::1", "2001:db8::1"},
		{"172.18.0.5:4444", "garbage", "172.18.0.5"},
		{"[::1]:80", "", "::1"},
	}
	for _, c := range cases {
		if got := mk(c.remote, c.xff); got != c.want {
			t.Errorf("clientIP(%s, %q) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestParseAllowlist(t *testing.T) {
	pfx, norm, err := parseAllowlist([]string{"203.0.113.5", " 10.0.0.7/8 ", "2001:db8::/32", "203.0.113.5", ""})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(norm, ",") != "203.0.113.5/32,10.0.0.0/8,2001:db8::/32" {
		t.Fatalf("normalised = %v", norm)
	}
	for ip, want := range map[string]bool{"203.0.113.5": true, "203.0.113.6": false, "10.9.9.9": true, "2001:db8:1::1": true, "garbage": false} {
		if got := ipInPrefixes(ip, pfx); got != want {
			t.Errorf("ipInPrefixes(%s) = %v, want %v", ip, got, want)
		}
	}
	for _, bad := range []string{"nope", "1.2.3.4/33", "300.1.1.1"} {
		if _, _, err := parseAllowlist([]string{bad}); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
