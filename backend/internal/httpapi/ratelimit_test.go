package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestMemLimiterBurstThenRefill(t *testing.T) {
	m := newMemLimiter()
	now := time.Now()
	m.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if res := m.take("k", 2, 5, 1); !res.Allowed || res.Remaining != 4-i {
			t.Fatalf("take %d: %+v", i, res)
		}
	}
	res := m.take("k", 2, 5, 1)
	if res.Allowed || res.RetryAfter <= 0 || res.RetryAfter > time.Second {
		t.Fatalf("6th take should be refused with a short retry: %+v", res)
	}
	now = now.Add(500 * time.Millisecond) // +1 token at 2/s
	if res := m.take("k", 2, 5, 1); !res.Allowed {
		t.Fatalf("token should have refilled: %+v", res)
	}
	if res := m.take("other", 2, 5, 1); !res.Allowed || res.Remaining != 4 {
		t.Fatalf("buckets must be independent: %+v", res)
	}
	// cost 0 only peeks.
	if res := m.take("peek", 1, 2, 0); !res.Allowed || res.Remaining != 2 {
		t.Fatalf("peek consumed or refused: %+v", res)
	}
}

func TestRateHeadersAnd429(t *testing.T) {
	w := httptest.NewRecorder()
	writeRateLimited(w, RateResult{Burst: 40, Remaining: 0, RetryAfter: 1200 * time.Millisecond, Reset: 2 * time.Second})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", w.Code)
	}
	for h, want := range map[string]string{"Retry-After": "2", "X-RateLimit-Limit": "40", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "2"} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:4000", "", "203.0.113.5"},
		{"203.0.113.5:4000", "1.1.1.1", "203.0.113.5"}, // public peer: header is spoofable, ignored
		{"172.18.0.4:5555", "198.51.100.7", "198.51.100.7"},
		{"172.18.0.4:5555", "9.9.9.9, 198.51.100.7", "198.51.100.7"}, // rightmost = what the proxy saw
		{"172.18.0.4:5555", "garbage", "172.18.0.4"},
		{"[::1]:1", "198.51.100.7", "198.51.100.7"},
	}
	for _, c := range cases {
		if got := clientIP(mk(c.remote, c.xff)); got != c.want {
			t.Errorf("%s xff=%q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestSubRateGatePerIP(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	calls := 0
	h := s.subRateGate(func(w http.ResponseWriter, r *http.Request) { calls++ })
	ip := "198.51.100." + strconv.Itoa(int(time.Now().UnixNano()%200)+1)
	do := func(from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/sub/x", nil)
		r.RemoteAddr = from + ":1234"
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	for i := 0; i < subIPBurst; i++ {
		if w := do(ip); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d limited too early", i)
		}
	}
	w := do(ip)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("burst exhausted: got %d, headers %v", w.Code, w.Header())
	}
	if calls != subIPBurst {
		t.Fatalf("handler ran %d times, want %d", calls, subIPBurst)
	}
	if w := do("192.0.2.77"); w.Code == http.StatusTooManyRequests {
		t.Fatal("another IP must not be affected")
	}
}

func TestSubMissAllowanceBlocksGuessing(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.subRateGate(func(w http.ResponseWriter, r *http.Request) {
		s.noteSubscriptionMiss(r) // what lookupSubscriptionAccount does on an unknown token
		w.WriteHeader(http.StatusNotFound)
	})
	var codes []int
	for i := 0; i < subMissBurst+3; i++ {
		r := httptest.NewRequest("GET", "/api/v1/sub/guess", nil)
		r.RemoteAddr = "192.0.2.201:1"
		w := httptest.NewRecorder()
		h(w, r)
		codes = append(codes, w.Code)
	}
	if codes[0] != 404 || codes[len(codes)-1] != 429 {
		t.Fatalf("guessing must end in 429: %v", codes)
	}
}

// TestRedisTokenBucket runs the Lua script against a real Redis. Set
// WGPANEL_TEST_REDIS_URL (e.g. redis://127.0.0.1:6390/0) to enable.
func TestRedisTokenBucket(t *testing.T) {
	url := os.Getenv("WGPANEL_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set WGPANEL_TEST_REDIS_URL to run")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rc := redis.NewClient(opts)
	t.Cleanup(func() { _ = rc.Close() })
	s := &Server{Redis: rc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	key := "test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx := context.Background()
	t.Cleanup(func() { rc.Del(ctx, "rl:"+key) })

	for i := 0; i < 5; i++ {
		res := s.takeToken(ctx, key, 1, 5, 1)
		if !res.Allowed || res.Remaining != 4-i || res.Burst != 5 {
			t.Fatalf("take %d: %+v", i, res)
		}
	}
	res := s.takeToken(ctx, key, 1, 5, 1)
	if res.Allowed || res.RetryAfter < 500*time.Millisecond || res.RetryAfter > 1100*time.Millisecond {
		t.Fatalf("6th take: %+v", res)
	}
	if peek := s.takeToken(ctx, key, 1, 5, 0); peek.Allowed {
		t.Fatalf("peek on an empty bucket must report not allowed: %+v", peek)
	}
	time.Sleep(1100 * time.Millisecond)
	if res := s.takeToken(ctx, key, 1, 5, 1); !res.Allowed {
		t.Fatalf("token should have refilled: %+v", res)
	}
	if ttl := rc.PTTL(ctx, "rl:"+key).Val(); ttl <= 0 {
		t.Fatalf("bucket key must expire, ttl=%v", ttl)
	}
}
