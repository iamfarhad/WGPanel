package authcrypto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Refresh-token rotation with reuse detection.
//
// Every successful refresh swaps the presented token for a new one and remembers the
// old one as "used". All tokens descended from one login share a family id. If a
// token that was already rotated is presented again, the only explanations are a
// stolen token or a client replaying a stale one, and we cannot tell which - so the
// whole family is revoked and the user signs in again (the OAuth 2.0 Security BCP
// recommendation for rotated refresh tokens).
//
// Concurrency: two browser tabs commonly refresh at the same moment with the same
// token. Revoking on that would log people out for nothing, so a token used again
// within RefreshReuseGrace of its first use gets the family's current token back
// instead of a reuse verdict. The holder of the old token is entitled to its
// successor anyway, so this hands out nothing new. Redis state (all keys expire with RefreshTokenTTL):
//
//	refresh_token:<tok>  -> "<admin>|<family>"                   the live token
//	rt_used:<tok>        -> "<admin>|<family>|<successor>|<unix>" an already rotated token
//	rt_family:<family>   -> <tok>                                the family's live token, so revocation can delete it
//
// Everything is done in one Lua script so check-and-rotate is atomic across API
// replicas.

// RefreshReuseGrace is the window in which a repeat presentation of a just-rotated
// token is treated as a duplicate request rather than theft.
const RefreshReuseGrace = 10 * time.Second

// ErrRefreshReused means an already-rotated token was presented outside the grace
// window; the family has been revoked by the time this is returned.
var ErrRefreshReused = errors.New("refresh token reuse detected")

// RotateResult is the outcome of a successful rotation.
type RotateResult struct {
	AdminID   string
	NewToken  string
	FamilyID  string
	Duplicate bool // true when the grace window returned an earlier successor
}

// ReuseError carries who owned the revoked family, for the audit entry and alert.
type ReuseError struct {
	AdminID  string
	FamilyID string
}

func (e *ReuseError) Error() string { return ErrRefreshReused.Error() }
func (e *ReuseError) Unwrap() error { return ErrRefreshReused }

var rotateScript = redis.NewScript(`
local ttl = tonumber(ARGV[4])
local cur = redis.call('GET', KEYS[1])
if cur then
  local sep = string.find(cur, '|', 1, true)
  local admin, fam
  if sep then
    admin = string.sub(cur, 1, sep - 1)
    fam = string.sub(cur, sep + 1)
  else
    -- token issued before rotation existed: adopt it into a fresh family
    admin = cur
    fam = ARGV[5]
  end
  redis.call('SET', 'refresh_token:' .. ARGV[2], admin .. '|' .. fam, 'EX', ttl)
  redis.call('DEL', KEYS[1])
  redis.call('SET', 'rt_used:' .. ARGV[1], admin .. '|' .. fam .. '|' .. ARGV[2] .. '|' .. ARGV[3], 'EX', ttl)
  redis.call('SET', 'rt_family:' .. fam, ARGV[2], 'EX', ttl)
  return {'ok', admin, fam, ARGV[2]}
end
local used = redis.call('GET', 'rt_used:' .. ARGV[1])
if used then
  local p = {}
  for part in string.gmatch(used, '([^|]+)') do p[#p + 1] = part end
  local admin, fam, succ, at = p[1], p[2], p[3], tonumber(p[4])
  local live = redis.call('GET', 'rt_family:' .. fam)
  -- Hand back the family's CURRENT token, not necessarily this token's direct
  -- successor: another tab may already have rotated that one again.
  if live and tonumber(ARGV[3]) - at <= tonumber(ARGV[6]) and redis.call('EXISTS', 'refresh_token:' .. live) == 1 then
    return {'dup', admin, fam, live}
  end
  if live then redis.call('DEL', 'refresh_token:' .. live) end
  redis.call('DEL', 'rt_family:' .. fam)
  return {'reuse', admin, fam, ''}
end
return {'invalid', '', '', ''}
`)

func newOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// RotateRefreshToken exchanges token for a successor. Returns ErrInvalidRefreshToken
// for an unknown/expired token and a *ReuseError (wrapping ErrRefreshReused) when a
// rotated token is replayed.
func RotateRefreshToken(ctx context.Context, rdb *redis.Client, token string) (RotateResult, error) {
	newTok, err := newOpaqueToken()
	if err != nil {
		return RotateResult{}, err
	}
	fam, err := newOpaqueToken()
	if err != nil {
		return RotateResult{}, err
	}
	res, err := rotateScript.Run(ctx, rdb, []string{refreshTokenKey(token)},
		token, newTok, strconv.FormatInt(time.Now().Unix(), 10),
		strconv.Itoa(int(RefreshTokenTTL.Seconds())), fam[:32], int(RefreshReuseGrace.Seconds())).StringSlice()
	if err != nil {
		return RotateResult{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	if len(res) != 4 {
		return RotateResult{}, fmt.Errorf("rotate refresh token: unexpected script reply")
	}
	switch res[0] {
	case "ok":
		return RotateResult{AdminID: res[1], FamilyID: res[2], NewToken: res[3]}, nil
	case "dup":
		return RotateResult{AdminID: res[1], FamilyID: res[2], NewToken: res[3], Duplicate: true}, nil
	case "reuse":
		return RotateResult{}, &ReuseError{AdminID: res[1], FamilyID: res[2]}
	}
	return RotateResult{}, ErrInvalidRefreshToken
}

// issueFamilyToken stores the first token of a new family (login).
func issueFamilyToken(ctx context.Context, rdb *redis.Client, adminID string) (string, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	fam, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	fam = fam[:32]
	pipe := rdb.TxPipeline()
	pipe.Set(ctx, refreshTokenKey(token), adminID+"|"+fam, RefreshTokenTTL)
	pipe.Set(ctx, "rt_family:"+fam, token, RefreshTokenTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}
	return token, nil
}

// adminOfRefreshValue extracts the admin id from a stored value in either format.
func adminOfRefreshValue(v string) string {
	if i := strings.IndexByte(v, '|'); i >= 0 {
		return v[:i]
	}
	return v
}
