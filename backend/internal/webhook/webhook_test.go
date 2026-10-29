package webhook

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"a":1}`)
	h := Sign("sekret", now, body)
	if h[:13] != "t=1700000000," {
		t.Fatalf("header = %s", h)
	}
	if !Verify("sekret", h, body, now.Add(time.Minute), 5*time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if Verify("other", h, body, now, 5*time.Minute) {
		t.Fatal("wrong secret accepted")
	}
	if Verify("sekret", h, []byte(`{"a":2}`), now, 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
	if Verify("sekret", h, body, now.Add(time.Hour), 5*time.Minute) {
		t.Fatal("stale timestamp accepted")
	}
	if Verify("sekret", "garbage", body, now, time.Minute) {
		t.Fatal("garbage accepted")
	}
}

func TestNextBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		d, ok := NextBackoff(i + 1)
		if !ok || d != w {
			t.Fatalf("attempt %d: %v %v", i+1, d, ok)
		}
	}
	if _, ok := NextBackoff(len(want) + 1); ok {
		t.Fatal("should give up after the last backoff")
	}
	if _, ok := NextBackoff(0); ok {
		t.Fatal("0 attempts is not a failure yet")
	}
	if MaxAttempts != 6 {
		t.Fatalf("MaxAttempts = %d", MaxAttempts)
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "100.64.0.1", "::1", "fe80::1", "fc00::1", "fd00:ec2::254", "::ffff:127.0.0.1", "::ffff:10.0.0.1"}
	for _, s := range blocked {
		if !IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "172.32.0.1", "2606:4700::1111"} {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	public := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }
	private := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil }
	fail := func(string) ([]net.IP, error) { return nil, errors.New("nxdomain") }

	cases := []struct {
		url     string
		allow   bool
		resolve func(string) ([]net.IP, error)
		wantErr bool
	}{
		{"https://example.com/hook", false, public, false},
		{"ftp://example.com", false, public, true},
		{"https://user:pw@example.com", false, public, true},
		{"https://", false, public, true},
		{"http://127.0.0.1:8080/x", false, public, true},
		{"http://[::1]/x", false, public, true},
		{"http://169.254.169.254/latest/meta-data", false, public, true},
		{"http://localhost/x", false, public, true},
		{"http://metadata.google.internal/x", false, public, true},
		{"https://rebind.example.com", false, private, true}, // resolves to a private address too
		{"https://nope.example.com", false, fail, true},
		{"http://127.0.0.1:8080/x", true, public, false}, // staff override
	}
	for _, c := range cases {
		err := ValidateURL(c.url, c.allow, c.resolve)
		if (err != nil) != c.wantErr {
			t.Errorf("%s allow=%v: err=%v wantErr=%v", c.url, c.allow, err, c.wantErr)
		}
	}
}

func TestClientBlocksLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()

	c := NewClient(2*time.Second, func() bool { return false })
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("loopback target should be refused by the dialer")
	}
	c = NewClient(2*time.Second, func() bool { return true })
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("override should allow: %v", err)
	}
	resp.Body.Close()
}

func TestClientBlocksRedirectToInternal(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer internal.Close()
	// The public-looking origin redirects to the internal one. With the guard on, even the
	// first hop (loopback in tests) is refused; with it on only for the second hop we
	// simulate by toggling allow after the first connection.
	calls := 0
	allow := func() bool { calls++; return calls == 1 }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer origin.Close()
	c := NewClient(2*time.Second, allow)
	if resp, err := c.Get(origin.URL); err == nil {
		resp.Body.Close()
		t.Fatal("redirect to a blocked address must fail")
	}
}
