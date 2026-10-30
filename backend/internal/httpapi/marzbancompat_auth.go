package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// Authentication of the Marzban-compatible facade (docs/marzban-compat.md).
//
// Marzban bots log in with POST /api/admin/token (OAuth2 password form) and then send
// "Authorization: Bearer <token>". We map that onto the existing Bot API keys:
//
//	username = the key's key_id        password = the key's raw secret
//
// A raw-secret check is possible because the secret is stored ENCRYPTED (AES, under
// APIHMACMasterKey) rather than hashed - the HMAC scheme needs the raw secret to
// recompute signatures - so the token endpoint decrypts it and compares in constant
// time. No second credential exists, hence nothing extra to leak, rotate or revoke:
// rotating the key's secret (the previous one stays valid 24h, as for HMAC) or
// revoking the key takes effect here too.
//
// The bearer token is a short JWT, but it is only a pointer to the key: every request
// re-loads the key and rebuilds the identity through apiKeyIdentity - the very code the
// HMAC scheme uses - so namespace, permissions, node groups, reseller state and the
// per-key rate limit are exactly the key's own, and a revoked key or disabled reseller
// stops working at once instead of at token expiry.

// mzTokenTTL is Marzban's default ACCESS_TOKEN_EXPIRE_MINUTES (1440). The Mirza bot
// caches a token for an hour, so anything shorter than that would force a re-login per
// call.
const mzTokenTTL = 24 * time.Hour

const mzTokenAudience = "marzban-compat"

type mzClaims struct {
	// KeyFP binds the token to the key's current encrypted secret: after a rotation the
	// stored blob changes and every token minted under the old secret dies, even though
	// the old secret itself still logs in during its 24h grace.
	KeyFP string `json:"kfp"`
	// Access mirrors Marzban's claim; informational only.
	Access string `json:"access"`
	jwt.RegisteredClaims
}

// mzSigningKey derives a signing key distinct from the one that signs admin JWTs, so a
// compat token can never be replayed as an admin token (and vice versa) even though both
// are HS256 JWTs - authenticateAdmin would otherwise accept any token signed with
// JWTSecret and merely fail later on the missing admin claims.
func (s *Server) mzSigningKey() []byte {
	mac := hmac.New(sha256.New, []byte(s.JWTSecret))
	mac.Write([]byte("marzban-compat/v1"))
	return mac.Sum(nil)
}

func mzKeyFingerprint(k store.APIKey) string {
	sum := sha256.Sum256([]byte(k.SecretEncrypted))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) mzIssueToken(k store.APIKey) (string, error) {
	now := time.Now()
	claims := mzClaims{
		KeyFP:  mzKeyFingerprint(k),
		Access: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   k.KeyID,
			Audience:  jwt.ClaimStrings{mzTokenAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(mzTokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.mzSigningKey())
}

func (s *Server) mzParseToken(tokenString string) (*mzClaims, error) {
	claims := &mzClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		return s.mzSigningKey(), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(mzTokenAudience), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// mzSecretMatches compares the offered password against the key's current secret, or
// its previous one while the rotation grace window is open (same rule as
// verifyAPIKeySignature). Constant-time on equal-length input; the lengths of real
// secrets are fixed (64 hex chars), so the length shortcut leaks nothing useful.
func (s *Server) mzSecretMatches(k store.APIKey, password string) (bool, error) {
	cur, err := wgkeys.Decrypt(s.APIHMACMasterKey, k.SecretEncrypted)
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(cur), []byte(password)) == 1 {
		return true, nil
	}
	if k.PreviousSecretEncrypted != nil && k.PreviousSecretExpiresAt != nil && k.PreviousSecretExpiresAt.After(time.Now()) {
		prev, err := wgkeys.Decrypt(s.APIHMACMasterKey, *k.PreviousSecretEncrypted)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(prev), []byte(password)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// mzEnabled reads the panel-wide switch. A read failure fails closed (as disabled).
func (s *Server) mzEnabled(ctx context.Context) bool {
	on, err := s.Store.GetMarzbanCompatEnabled(ctx)
	if err != nil {
		s.Logger.Error("marzban_compat_enabled_lookup_failed", "error", err)
		return false
	}
	return on
}

// mzCaller is what a facade handler receives: the key-scoped identity and its namespace
// argument for the store.
type mzCaller struct {
	Identity *CallerIdentity
	NS       *string
	Key      store.APIKey
}

type mzHandler func(w http.ResponseWriter, r *http.Request, c mzCaller)

// mzRoute wraps a facade handler: switch check, bearer auth, per-key rate limit and the
// permission the operation needs ("" = any authenticated key). perm is the key permission
// the route maps to - see docs/marzban-compat.md for the table.
func (s *Server) mzRoute(perm string, h mzHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !s.mzEnabled(ctx) {
			// Indistinguishable from a route that does not exist (FastAPI's own 404).
			mzDetail(w, http.StatusNotFound, "Not Found")
			return
		}
		authz := r.Header.Get("Authorization")
		if authz == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			mzDetail(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		tokenString, ok := strings.CutPrefix(authz, "Bearer ")
		if !ok {
			tokenString, ok = strings.CutPrefix(authz, "bearer ")
		}
		if !ok || tokenString == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			mzDetail(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		claims, err := s.mzParseToken(tokenString)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			mzDetail(w, http.StatusUnauthorized, "Could not validate credentials")
			return
		}
		key, err := s.Store.GetAPIKeyByKeyID(ctx, claims.Subject)
		switch {
		case errors.Is(err, store.ErrAPIKeyNotFound):
			mzDetail(w, http.StatusUnauthorized, "Could not validate credentials")
			return
		case err != nil:
			s.Logger.Error("marzban_compat_get_api_key_failed", "error", err)
			mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if key.RevokedAt != nil || mzKeyFingerprint(key) != claims.KeyFP {
			mzDetail(w, http.StatusUnauthorized, "Could not validate credentials")
			return
		}

		// Same per-key bucket as the HMAC scheme: a bot cannot dodge its limit by
		// switching protocols.
		rate, burst := s.apiRateSettings(ctx)
		res := s.takeToken(ctx, "key:"+key.KeyID, rate, burst, 1)
		setRateHeaders(w, res)
		if !res.Allowed {
			w.Header().Set("Retry-After", strconv.Itoa(ceilSeconds(res.RetryAfter)))
			mzDetail(w, http.StatusTooManyRequests, "Too many requests")
			return
		}

		identity, aerr := s.apiKeyIdentity(ctx, key)
		if aerr != nil {
			mzDetail(w, aerr.Status, aerr.Message)
			return
		}
		if perm != "" && !stringSliceContains(identity.Permissions, perm) {
			mzDetail(w, http.StatusForbidden, "This API key lacks the '"+perm+"' permission")
			return
		}
		h(w, r, mzCaller{Identity: identity, NS: callerNamespaceArg(identity), Key: key})
	})
}
