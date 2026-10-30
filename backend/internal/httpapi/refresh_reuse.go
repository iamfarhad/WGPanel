package httpapi

import (
	"net/http"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/events"
)

// handleRefreshReuse runs after RotateRefreshToken has already revoked the family: it
// records who and from where, and raises a critical staff notification. It never
// affects the response - the caller just sees an ordinary invalid_refresh_token, so a
// thief learns nothing beyond "that token is dead".
func (s *Server) handleRefreshReuse(r *http.Request, reuse *authcrypto.ReuseError) {
	ctx := r.Context()
	ip := clientIP(r)
	username := reuse.AdminID
	if admin, err := s.Store.GetAdminByID(ctx, reuse.AdminID); err == nil {
		username = admin.Username
	}
	family := reuse.FamilyID
	if len(family) > 8 {
		family = family[:8] // enough to correlate log lines, not enough to be a credential
	}
	detail := map[string]string{"family": family, "at": time.Now().UTC().Format(time.RFC3339)}
	if err := s.Store.InsertAuditLog(ctx, username, "admin.refresh_token_reuse", username, detail, ip); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.Logger.Warn("refresh_token_reuse_detected", "admin", username, "ip", ip)
	// Namespace "" = staff only, even when the victim is a reseller: the reseller
	// cannot act on it, and a notification to them would help an attacker who has
	// their session open.
	s.emit(events.RefreshReuse, "", map[string]any{"admin": username, "ip": ip})
}
