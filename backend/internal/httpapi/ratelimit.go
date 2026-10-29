package httpapi

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rate limiting: a token bucket per Bot API key (rate/burst from panel settings) and
// per client IP on the public subscription endpoints. State lives in Redis so every
// API replica shares one bucket; when Redis is unreachable (or absent, as in unit
// tests) a per-process in-memory bucket takes over rather than failing requests.

const (
	defaultAPIRateRPS   = 20
	defaultAPIRateBurst = 40

	// Public subscription endpoints: generous for a person loading the page (info +
	// config + nodes in a burst) yet far too slow to enumerate 192-bit tokens.
	subIPRate  = 2.0
	subIPBurst = 30
	// Unknown-token lookups are charged separately and refilled very slowly, so token
	// guessing from one address dies after a handful of tries.
	subMissRate  = 0.1
	subMissBurst = 10
)

// RateResult is the outcome of one bucket take.
type RateResult struct {
	Allowed    bool
	Burst      int           // bucket capacity (X-RateLimit-Limit)
	Remaining  int           // whole tokens left after this take
	RetryAfter time.Duration // when to retry; 0 if allowed
	Reset      time.Duration // until the bucket is full again
}

// tokenBucketScript: KEYS[1]=bucket; ARGV = rate (tokens/s), burst, cost.
// cost 0 only inspects (allowed = at least one token). Returns
// {allowed, remaining, retry_ms, reset_ms}. Uses Redis TIME so replicas' clocks
// don't matter.
var tokenBucketScript = redis.NewScript(`
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local d = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(d[1])
local ts = tonumber(d[2])
if tokens == nil then tokens = burst; ts = now end
tokens = math.min(burst, tokens + math.max(0, now - ts) * rate / 1000)
local allowed = 0
if tokens >= 1 then allowed = 1 end
if cost > 0 and tokens >= cost then
  tokens = tokens - cost
  allowed = 1
elseif cost > 0 then
  allowed = 0
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', now)
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate * 1000) + 1000)
local retry = 0
if allowed == 0 then retry = math.ceil((math.max(cost, 1) - tokens) * 1000 / rate) end
local reset = math.ceil((burst - tokens) * 1000 / rate)
return {allowed, math.floor(tokens), retry, reset}
`)

type memBucket struct {
	tokens float64
	last   time.Time
}

type memLimiter struct {
	mu      sync.Mutex
	buckets map[string]*memBucket
	swept   time.Time
	now     func() time.Time
}

func newMemLimiter() *memLimiter {
	return &memLimiter{buckets: map[string]*memBucket{}, now: time.Now}
}

// fallbackLimiter is shared by every Server in the process (state is only a fallback).
var fallbackLimiter = newMemLimiter()

func (m *memLimiter) take(key string, rate float64, burst int, cost int) RateResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if now.Sub(m.swept) > time.Minute { // drop buckets idle long enough to be full again
		for k, b := range m.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(m.buckets, k)
			}
		}
		m.swept = now
	}
	b, ok := m.buckets[key]
	if !ok {
		b = &memBucket{tokens: float64(burst), last: now}
		m.buckets[key] = b
	}
	b.tokens = math.Min(float64(burst), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	allowed := b.tokens >= 1
	if cost > 0 {
		allowed = b.tokens >= float64(cost)
		if allowed {
			b.tokens -= float64(cost)
		}
	}
	res := RateResult{Allowed: allowed, Burst: burst, Remaining: int(math.Floor(b.tokens))}
	if !allowed {
		need := math.Max(float64(cost), 1) - b.tokens
		res.RetryAfter = time.Duration(math.Ceil(need/rate*1000)) * time.Millisecond
	}
	res.Reset = time.Duration(math.Ceil((float64(burst)-b.tokens)/rate*1000)) * time.Millisecond
	return res
}

// takeToken charges cost tokens from bucket key (cost 0 = peek).
func (s *Server) takeToken(ctx context.Context, key string, rate float64, burst, cost int) RateResult {
	if s.Redis != nil {
		cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		vals, err := tokenBucketScript.Run(cctx, s.Redis, []string{"rl:" + key}, rate, burst, cost).Int64Slice()
		if err == nil && len(vals) == 4 {
			return RateResult{
				Allowed:    vals[0] == 1,
				Burst:      burst,
				Remaining:  int(vals[1]),
				RetryAfter: time.Duration(vals[2]) * time.Millisecond,
				Reset:      time.Duration(vals[3]) * time.Millisecond,
			}
		}
		if s.Logger != nil {
			s.Logger.Warn("rate_limit_redis_failed_using_memory", "error", err)
		}
	}
	return fallbackLimiter.take(key, rate, burst, cost)
}

func ceilSeconds(d time.Duration) int {
	n := int(math.Ceil(d.Seconds()))
	if n < 1 {
		n = 1
	}
	return n
}

// setRateHeaders writes X-RateLimit-* (Limit = bucket capacity, Remaining = whole
// tokens left, Reset = seconds until the bucket is full again).
func setRateHeaders(w http.ResponseWriter, res RateResult) {
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(res.Burst))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
	h.Set("X-RateLimit-Reset", strconv.Itoa(int(math.Ceil(res.Reset.Seconds()))))
}

func writeRateLimited(w http.ResponseWriter, res RateResult) {
	setRateHeaders(w, res)
	w.Header().Set("Retry-After", strconv.Itoa(ceilSeconds(res.RetryAfter)))
	writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "too many requests - slow down and retry after the Retry-After interval")
}

// ---- Bot API: per-key bucket ----

type apiRateCache struct {
	mu      sync.Mutex
	rps     int
	burst   int
	fetched time.Time
}

var apiRates apiRateCache

// apiRateSettings returns the configured per-key rate/burst, cached for 10 seconds
// (one settings read per request would double the auth path's DB load).
func (s *Server) apiRateSettings(ctx context.Context) (float64, int) {
	apiRates.mu.Lock()
	defer apiRates.mu.Unlock()
	if time.Since(apiRates.fetched) > 10*time.Second || apiRates.rps == 0 {
		rps, burst := defaultAPIRateRPS, defaultAPIRateBurst
		if s.Store != nil {
			if st, err := s.Store.GetSettings(ctx); err == nil && st.APIRateRPS > 0 && st.APIRateBurst > 0 {
				rps, burst = st.APIRateRPS, st.APIRateBurst
			}
		}
		apiRates.rps, apiRates.burst, apiRates.fetched = rps, burst, time.Now()
	}
	return float64(apiRates.rps), apiRates.burst
}

// resetAPIRateCache is for tests and settings updates: take effect immediately.
func resetAPIRateCache() {
	apiRates.mu.Lock()
	apiRates.fetched = time.Time{}
	apiRates.mu.Unlock()
}

// allowAPIKey charges one token from the key's bucket; on refusal it writes the 429.
func (s *Server) allowAPIKey(w http.ResponseWriter, r *http.Request, keyID string) bool {
	rate, burst := s.apiRateSettings(r.Context())
	res := s.takeToken(r.Context(), "key:"+keyID, rate, burst, 1)
	if !res.Allowed {
		writeRateLimited(w, res)
		return false
	}
	setRateHeaders(w, res)
	return true
}

// ---- Public subscription endpoints: per-IP bucket ----

// clientIP is the caller's address for rate limiting. Behind the reverse proxy
// (loopback/private peer) the rightmost X-Forwarded-For entry is the address Caddy
// saw; a public peer connected directly, so its own address is used and the header
// (client-controlled) is ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if cand := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(cand) != nil {
				return cand
			}
		}
	}
	return host
}

// subRateGate wraps a public subscription handler: every request costs one token from
// the caller's IP bucket, and an IP that already burned its unknown-token allowance is
// refused outright (the handler reports misses through noteSubscriptionMiss).
func (s *Server) subRateGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if miss := s.takeToken(r.Context(), "submiss:"+ip, subMissRate, subMissBurst, 0); !miss.Allowed {
			writeRateLimited(w, miss)
			return
		}
		res := s.takeToken(r.Context(), "sub:"+ip, subIPRate, subIPBurst, 1)
		if !res.Allowed {
			writeRateLimited(w, res)
			return
		}
		setRateHeaders(w, res)
		next(w, r)
	}
}

// noteSubscriptionMiss charges the caller's unknown-token allowance.
func (s *Server) noteSubscriptionMiss(r *http.Request) {
	s.takeToken(r.Context(), "submiss:"+clientIP(r), subMissRate, subMissBurst, 1)
}
