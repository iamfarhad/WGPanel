package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Progressive login lockout, kept in Redis so it survives API restarts and is shared
// across replicas. Two independent subjects are tracked - the (case-folded) username
// and the client IP - because each covers what the other misses: per-username stops a
// distributed guess at one account, per-IP stops one host spraying many usernames.
// Unknown usernames are tracked exactly like real ones, so a lockout never reveals
// whether an account exists.
//
//	failures within the window >= threshold  ->  locked for base * 2^(level-1)
//
// where level counts the lockouts of that subject in the last 24h (cap 24h per lock).
// The IP threshold is ipFailureMultiplier x the per-username threshold, since several
// admins may share one NAT address.
const (
	ipFailureMultiplier = 4
	lockLevelTTL        = 24 * time.Hour
	maxLockDuration     = 24 * time.Hour
)

type loginSubject struct {
	kind string // "user" or "ip"
	id   string
}

func (l loginSubject) failKey() string  { return "login:fail:" + l.kind + ":" + l.id }
func (l loginSubject) lockKey() string  { return "login:lock:" + l.kind + ":" + l.id }
func (l loginSubject) levelKey() string { return "login:lvl:" + l.kind + ":" + l.id }

func loginSubjects(username, ip string) []loginSubject {
	subs := []loginSubject{{"ip", ip}}
	if u := strings.ToLower(strings.TrimSpace(username)); u != "" {
		subs = append(subs, loginSubject{"user", u})
	}
	return subs
}

// loginLockedFor returns how long the caller is still locked out (0 = not locked).
func (s *Server) loginLockedFor(ctx context.Context, username, ip string) time.Duration {
	if s.Redis == nil {
		return 0
	}
	var longest time.Duration
	for _, sub := range loginSubjects(username, ip) {
		ttl, err := s.Redis.TTL(ctx, sub.lockKey()).Result()
		if err == nil && ttl > longest {
			longest = ttl
		}
	}
	return longest
}

// writeTooManyAttempts sends the 429 with Retry-After.
func writeTooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSONError(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed sign-in attempts - try again later")
}

// recordLoginFailure counts one failed credential check against the username and IP
// and applies/extends a lockout when a threshold is crossed. It reports whether the
// caller is now locked out (and for how long) and writes a lockout audit entry.
func (s *Server) recordLoginFailure(ctx context.Context, username, ip string) (time.Duration, bool) {
	if s.Redis == nil {
		return 0, false
	}
	_, maxFail, lockMin := s.loadSecurity(ctx)
	if maxFail < 1 {
		maxFail = 5
	}
	if lockMin < 1 {
		lockMin = 15
	}
	base := time.Duration(lockMin) * time.Minute
	window := base
	if window < 15*time.Minute {
		window = 15 * time.Minute
	}

	var lockedFor time.Duration
	for _, sub := range loginSubjects(username, ip) {
		threshold := int64(maxFail)
		if sub.kind == "ip" {
			threshold *= ipFailureMultiplier
		}
		// Already locked: do not extend the lock by hammering it.
		if ttl, err := s.Redis.TTL(ctx, sub.lockKey()).Result(); err == nil && ttl > 0 {
			if ttl > lockedFor {
				lockedFor = ttl
			}
			continue
		}
		n, err := s.Redis.Incr(ctx, sub.failKey()).Result()
		if err != nil {
			s.Logger.Error("login_fail_counter_failed", "error", err)
			continue
		}
		if n == 1 {
			s.Redis.Expire(ctx, sub.failKey(), window)
		}
		if n < threshold {
			continue
		}
		level, err := s.Redis.Incr(ctx, sub.levelKey()).Result()
		if err != nil {
			level = 1
		}
		s.Redis.Expire(ctx, sub.levelKey(), lockLevelTTL)
		d := base
		for i := int64(1); i < level && d < maxLockDuration; i++ {
			d *= 2
		}
		if d > maxLockDuration {
			d = maxLockDuration
		}
		s.Redis.Set(ctx, sub.lockKey(), "1", d)
		s.Redis.Del(ctx, sub.failKey())
		if d > lockedFor {
			lockedFor = d
		}
		actor := sub.id
		if sub.kind == "ip" {
			actor = "ip:" + sub.id
		}
		if err := s.Store.InsertAuditLog(ctx, actor, "admin.login_locked", actor,
			map[string]any{"subject": sub.kind, "lock_seconds": int(d.Seconds()), "level": level}, ip); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
	return lockedFor, lockedFor > 0
}

// clearLoginFailures resets the username's failure counter after a fully successful
// sign-in. The IP counter is left to expire on its own: a successful login from a
// shared address must not wipe the evidence of a spray from that same address.
func (s *Server) clearLoginFailures(ctx context.Context, username string) {
	if s.Redis == nil {
		return
	}
	if u := strings.ToLower(strings.TrimSpace(username)); u != "" {
		sub := loginSubject{"user", u}
		s.Redis.Del(ctx, sub.failKey(), sub.levelKey())
	}
}
