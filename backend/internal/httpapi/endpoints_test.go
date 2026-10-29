package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wgpanel-api/internal/store"
)

func ep(label string, prio int, enabled bool, health, status string, checked time.Time, age time.Duration) store.NodeEndpoint {
	e := store.NodeEndpoint{
		ID: label, Label: label, Priority: prio, Enabled: enabled, HealthCheck: health, LastStatus: status,
		CreatedAt: checked.Add(-age),
	}
	if status != "unknown" {
		c := checked
		e.LastCheckedAt = &c
	}
	return e
}

func TestPickEndpoint(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		eps  []store.NodeEndpoint
		want string // "" = nil (fall back to public_endpoint)
	}{
		{"no endpoints", nil, ""},
		{"highest priority wins", []store.NodeEndpoint{
			ep("low", 10, true, "none", "unknown", now, time.Hour),
			ep("high", 200, true, "none", "unknown", now, time.Hour),
		}, "high"},
		{"disabled skipped", []store.NodeEndpoint{
			ep("off", 500, false, "none", "unknown", now, time.Hour),
			ep("on", 1, true, "none", "unknown", now, time.Hour),
		}, "on"},
		{"down skipped, next healthy used", []store.NodeEndpoint{
			ep("dead", 300, true, "tcp", "down", now, time.Hour),
			ep("alive", 100, true, "tcp", "up", now, time.Hour),
		}, "alive"},
		{"all down falls back", []store.NodeEndpoint{
			ep("dead", 300, true, "tcp", "down", now, time.Hour),
		}, ""},
		{"unchecked tcp endpoint is not used yet", []store.NodeEndpoint{
			ep("new", 300, true, "tcp", "unknown", now, time.Hour),
		}, ""},
		{"stale up verdict is not trusted", []store.NodeEndpoint{
			ep("stale", 300, true, "tcp", "up", now.Add(-time.Hour), time.Hour),
		}, ""},
		{"tie goes to the older endpoint", []store.NodeEndpoint{
			ep("newer", 100, true, "none", "unknown", now, time.Minute),
			ep("older", 100, true, "none", "unknown", now, time.Hour),
		}, "older"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickEndpoint(c.eps, now)
			switch {
			case c.want == "" && got != nil:
				t.Fatalf("got %q, want fallback", got.Label)
			case c.want != "" && (got == nil || got.Label != c.want):
				t.Fatalf("got %v, want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeEndpointAddress(t *testing.T) {
	for _, ok := range []string{"relay.example.ir:51820", "1.2.3.4:443", "[2001:db8::1]:51820"} {
		if _, err := normalizeEndpointAddress(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "relay.example.ir", "https://relay.example.ir:1", "host:0", "host:70000", "a b:1"} {
		if _, err := normalizeEndpointAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidMTU(t *testing.T) {
	for v, want := range map[int]bool{1199: false, 1200: true, 1280: true, 1420: true, 1421: false, 0: false} {
		if validMTU(v) != want {
			t.Errorf("validMTU(%d) != %v", v, want)
		}
	}
}

func TestWantsHTML(t *testing.T) {
	mk := func(accept, query string) *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/sub/tok"+query, nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		return r
	}
	browser := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	cases := []struct {
		accept, query string
		want          bool
	}{
		{browser, "", true},
		{"", "", false},
		{"*/*", "", false},
		{"application/json", "", false},
		{browser, "?format=conf", false}, // explicit override: the page's download link
		{browser, "?node_id=x", true},
	}
	for _, c := range cases {
		if got := wantsHTML(mk(c.accept, c.query)); got != c.want {
			t.Errorf("accept=%q query=%q: got %v, want %v", c.accept, c.query, got, c.want)
		}
	}
}

func TestSubscriptionBrowserRedirect(t *testing.T) {
	s := &Server{} // the redirect happens before any store access
	r := httptest.NewRequest("GET", "/api/v1/sub/abc123", nil)
	r.SetPathValue("token", "abc123")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.handleSubscriptionConfig(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/s/abc123" {
		t.Fatalf("got %d %q, want 302 /s/abc123", w.Code, w.Header().Get("Location"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("redirect must not be cached")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	id := "0b5c1d0e-1111-4222-8333-444455556666"
	gotAt, gotID, err := decodeAccountCursor(encodeAccountCursor(at, id))
	if err != nil || gotID != id || !gotAt.Equal(at) {
		t.Fatalf("round trip: %v %v %v", gotAt, gotID, err)
	}
	if at, _, err := decodeAccountCursor(""); err != nil || at != nil {
		t.Fatalf("empty cursor must start from the beginning")
	}
	for _, bad := range []string{"!!!", "bm9waXBl", encodeAccountCursor(at, "not-a-uuid")} {
		if _, _, err := decodeAccountCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}
