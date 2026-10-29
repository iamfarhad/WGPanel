package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

// resellerRoutes is the explicit allowlist of mux patterns a reseller session may
// reach. It is deliberately NOT "everything below support": the default for any route
// (including ones added later) is denied, so forgetting to classify a new route fails
// closed. Account routes are further narrowed to the reseller's own namespace by the
// store layer (404 on foreign accounts) and quota-checked on create/update/renew.
// Notably absent: node writes, join tokens, node metrics, settings, admins, API keys,
// audit log, backups, resellers management.
var resellerRoutes = map[string]bool{
	"GET /api/v1/me":          true,
	"GET /api/v1/reseller/me": true,
	"GET /api/v1/reports":     true, // always scoped to its own namespace
	// Self-service two-factor / passkeys: the handlers act only on the session's own
	// admin id.
	"GET /api/v1/me/security":                  true,
	"POST /api/v1/me/security/totp/enroll":     true,
	"POST /api/v1/me/security/totp/confirm":    true,
	"POST /api/v1/me/security/totp/disable":    true,
	"POST /api/v1/me/security/recovery-codes":  true,
	"POST /api/v1/me/security/passkeys/begin":  true,
	"POST /api/v1/me/security/passkeys/finish": true,
	"DELETE /api/v1/me/security/passkeys/{id}": true,
	// A reseller manages only the API keys it owns (store-scoped, 404 otherwise).
	"GET /api/v1/api-keys":              true,
	"POST /api/v1/api-keys":             true,
	"POST /api/v1/api-keys/{id}/revoke": true,
	"POST /api/v1/api-keys/{id}/rotate": true,
	// Read-only node visibility, filtered to the reseller's node groups, so it can
	// pick a node when creating an account.
	"GET /api/v1/nodes":                              true,
	"GET /api/v1/nodes/{id}":                         true,
	"POST /api/v1/accounts":                          true,
	"GET /api/v1/accounts":                           true,
	"GET /api/v1/accounts/{id}":                      true,
	"PATCH /api/v1/accounts/{id}":                    true,
	"DELETE /api/v1/accounts/{id}":                   true,
	"POST /api/v1/accounts/{id}/suspend":             true,
	"POST /api/v1/accounts/{id}/enable":              true,
	"POST /api/v1/accounts/{id}/renew":               true,
	"GET /api/v1/accounts/{id}/config":               true,
	"GET /api/v1/accounts/{id}/usage":                true,
	"GET /api/v1/accounts/{id}/devices":              true,
	"GET /api/v1/accounts/{id}/steer":                true,
	"POST /api/v1/accounts/{id}/subscription/rotate": true,
}

// resellerLimits fetches the reseller's limits row (test hook: ResellerLimitsLookup).
func (s *Server) resellerLimits(ctx context.Context, adminID string) (store.ResellerLimits, error) {
	if s.ResellerLimitsLookup != nil {
		return s.ResellerLimitsLookup(ctx, adminID)
	}
	return s.Store.GetResellerLimits(ctx, adminID)
}

// admitAdminClaims turns verified JWT claims into a CallerIdentity and applies the
// reseller-specific gates. Shared by requireAdmin and requireAdminOrAPIKey so the two
// auth paths cannot diverge. For a reseller it (1) enforces the route allowlist
// before touching the database, then (2) does one primary-key lookup of the limits
// row per request: a DB hit, but it makes "disabled" and "deleted" take effect on the
// very next request instead of when the 15-minute access token expires.
func (s *Server) admitAdminClaims(w http.ResponseWriter, r *http.Request, claims *authcrypto.Claims) (*CallerIdentity, bool) {
	// The admin IP allowlist guards JWT sessions only; API-key, subscription and
	// node-agent routes never pass through here.
	if !s.enforceAdminIPAllowlist(w, r) {
		return nil, false
	}
	identity := &CallerIdentity{IsAdmin: true, AdminID: claims.AdminID, AdminUsername: claims.Username, AdminRole: claims.Role}
	if claims.Role != "reseller" {
		return identity, true
	}
	if !resellerRoutes[r.Pattern] {
		writeJSONError(w, http.StatusForbidden, "forbidden", "this route is not available to reseller accounts")
		return nil, false
	}
	limits, err := s.resellerLimits(r.Context(), claims.AdminID)
	if errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return nil, false
	}
	if err != nil {
		s.Logger.Error("reseller_limits_lookup_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "authentication failed")
		return nil, false
	}
	if limits.Disabled {
		writeJSONError(w, http.StatusUnauthorized, "account_disabled", "this reseller account is disabled")
		return nil, false
	}
	if limits.ExpiresAt != nil && !limits.ExpiresAt.After(time.Now()) {
		writeJSONError(w, http.StatusUnauthorized, "reseller_expired", "this reseller plan has expired")
		return nil, false
	}
	identity.ResellerID = claims.AdminID
	identity.NodeGroups = limits.NodeGroups
	return identity, true
}

// canSeeNodeGroup is the read-side node visibility rule shared by the node list/get
// handlers: staff admins see everything; a reseller sees its groups (empty = all);
// an API key sees only its node_groups (empty = none, the pre-existing key semantics).
func canSeeNodeGroup(identity *CallerIdentity, group string) bool {
	switch {
	case identity.ResellerID != "":
		return len(identity.NodeGroups) == 0 || stringSliceContains(identity.NodeGroups, group)
	case identity.IsAdmin:
		return true
	default:
		return stringSliceContains(identity.NodeGroups, group)
	}
}
