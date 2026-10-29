package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

const totpIssuer = "Jirjirak WireGuard"

// registerSecurityRoutes mounts the admin sign-in security surface. It lives in its own
// function (and file) so this feature group stays self-contained; reseller_gate_test.go
// reads this file as well as server.go when classifying routes.
func (s *Server) registerSecurityRoutes(mux *http.ServeMux) {
	// Public (pre-session) steps of the sign-in flow. All of them enforce the admin IP
	// allowlist and the login lockout themselves.
	mux.HandleFunc("POST /api/v1/auth/mfa/totp", s.handleMFATOTP)
	mux.HandleFunc("POST /api/v1/auth/mfa/recovery", s.handleMFARecovery)
	mux.HandleFunc("POST /api/v1/auth/mfa/passkey/begin", s.handleMFAPasskeyBegin)
	mux.HandleFunc("POST /api/v1/auth/mfa/passkey/finish", s.handleMFAPasskeyFinish)
	mux.HandleFunc("POST /api/v1/auth/passkey/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/v1/auth/passkey/finish", s.handlePasskeyLoginFinish)

	// Self-service: every admin role, including resellers (see reseller_gate.go), manages
	// only their own factors - the handlers act on the JWT's admin id, never a path id.
	mux.Handle("GET /api/v1/me/security", s.requireAdmin(http.HandlerFunc(s.handleMySecurity)))
	mux.Handle("POST /api/v1/me/security/totp/enroll", s.requireAdmin(http.HandlerFunc(s.handleTOTPEnroll)))
	mux.Handle("POST /api/v1/me/security/totp/confirm", s.requireAdmin(http.HandlerFunc(s.handleTOTPConfirm)))
	mux.Handle("POST /api/v1/me/security/totp/disable", s.requireAdmin(http.HandlerFunc(s.handleTOTPDisable)))
	mux.Handle("POST /api/v1/me/security/recovery-codes", s.requireAdmin(http.HandlerFunc(s.handleRegenerateRecoveryCodes)))
	mux.Handle("POST /api/v1/me/security/passkeys/begin", s.requireAdmin(http.HandlerFunc(s.handlePasskeyRegisterBegin)))
	mux.Handle("POST /api/v1/me/security/passkeys/finish", s.requireAdmin(http.HandlerFunc(s.handlePasskeyRegisterFinish)))
	mux.Handle("DELETE /api/v1/me/security/passkeys/{id}", s.requireAdmin(http.HandlerFunc(s.handlePasskeyDelete)))

	// Super-admin controls.
	mux.Handle("DELETE /api/v1/admins/{id}/mfa", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleResetAdminMFA))))
	mux.Handle("GET /api/v1/security/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGetSecuritySettings))))
	mux.Handle("PATCH /api/v1/security/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateSecuritySettings))))

	// Break-glass for `wgpanel clear-ip-allowlist` (same X-Internal-Token tier as /internal/admins).
	mux.Handle("POST /internal/security/clear-allowlist", s.internalOnly(http.HandlerFunc(s.handleClearAllowlist)))
}

type mySecurityResponse struct {
	TOTPEnabled          bool              `json:"totp_enabled"`
	TOTPPending          bool              `json:"totp_pending"`
	RecoveryCodesLeft    int               `json:"recovery_codes_remaining"`
	Passkeys             []passkeyResponse `json:"passkeys"`
	PasskeysSupportedAPI bool              `json:"passkeys_supported"`
}

func (s *Server) handleMySecurity(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	resp := mySecurityResponse{Passkeys: []passkeyResponse{}, PasskeysSupportedAPI: true}
	if t, err := s.Store.GetAdminTOTP(ctx, identity.AdminID); err == nil {
		resp.TOTPEnabled, resp.TOTPPending = t.Confirmed, !t.Confirmed
	} else if !errors.Is(err, store.ErrTOTPNotFound) {
		s.Logger.Error("get_totp_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load security settings")
		return
	}
	if resp.TOTPEnabled {
		resp.RecoveryCodesLeft, _ = s.Store.CountRecoveryCodes(ctx, identity.AdminID)
	}
	pks, err := s.Store.ListPasskeys(ctx, identity.AdminID)
	if err != nil {
		s.Logger.Error("list_passkeys_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load security settings")
		return
	}
	for _, p := range pks {
		resp.Passkeys = append(resp.Passkeys, toPasskeyResponse(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

type totpEnrollResponse struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauth_uri"`
}

func (s *Server) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	secret, err := newTOTPSecret()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start enrolment")
		return
	}
	enc, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, secret)
	if err != nil {
		s.Logger.Error("totp_encrypt_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start enrolment")
		return
	}
	ok, err := s.Store.SetPendingTOTP(r.Context(), identity.AdminID, enc)
	if err != nil {
		s.Logger.Error("set_pending_totp_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start enrolment")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "totp_already_enabled", "two-factor authentication is already enabled - disable it first")
		return
	}
	writeJSON(w, http.StatusOK, totpEnrollResponse{Secret: secret, OtpauthURI: totpURI(totpIssuer, identity.AdminUsername, secret)})
}

type codeRequest struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "code is required")
		return
	}
	t, err := s.Store.GetAdminTOTP(ctx, identity.AdminID)
	if errors.Is(err, store.ErrTOTPNotFound) || (err == nil && t.Confirmed) {
		writeJSONError(w, http.StatusConflict, "totp_not_pending", "there is no pending two-factor enrolment - start again")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not confirm enrolment")
		return
	}
	secret, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, t.SecretEnc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not confirm enrolment")
		return
	}
	step, ok := verifyTOTP(secret, req.Code, time.Now())
	if !ok {
		s.recordLoginFailure(ctx, identity.AdminUsername, clientIP(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid_mfa_code", "the verification code is invalid")
		return
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not confirm enrolment")
		return
	}
	if err := s.Store.ConfirmTOTP(ctx, identity.AdminID, step, hashes); err != nil {
		s.Logger.Error("confirm_totp_failed", "error", err)
		writeJSONError(w, http.StatusConflict, "totp_not_pending", "there is no pending two-factor enrolment - start again")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.totp_enabled", identity.AdminUsername, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// reauthenticate proves the caller still knows the password and holds a TOTP/recovery
// code - required before weakening or regenerating the second factor, so a stolen
// access token alone cannot strip it. Failures count toward the login lockout.
func (s *Server) reauthenticate(w http.ResponseWriter, r *http.Request, adminID string, req codeRequest) bool {
	ctx := r.Context()
	admin, err := s.Store.GetAdminByID(ctx, adminID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return false
	}
	ip := clientIP(r)
	if wait := s.loginLockedFor(ctx, admin.Username, ip); wait > 0 {
		writeTooManyAttempts(w, wait)
		return false
	}
	valid, verr := authcrypto.VerifyPassword(req.Password, admin.PasswordHash)
	if verr != nil || !valid {
		if wait, locked := s.recordLoginFailure(ctx, admin.Username, ip); locked {
			writeTooManyAttempts(w, wait)
			return false
		}
		writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "the password is incorrect")
		return false
	}
	if !s.checkTOTP(ctx, adminID, req.Code) && !s.checkRecoveryCode(ctx, adminID, req.Code) {
		if wait, locked := s.recordLoginFailure(ctx, admin.Username, ip); locked {
			writeTooManyAttempts(w, wait)
			return false
		}
		writeJSONError(w, http.StatusUnauthorized, "invalid_mfa_code", "the verification code is invalid")
		return false
	}
	return true
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password and code are required")
		return
	}
	if !s.reauthenticate(w, r, identity.AdminID, req) {
		return
	}
	if err := s.Store.DisableTOTP(r.Context(), identity.AdminID); err != nil {
		s.Logger.Error("disable_totp_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not disable two-factor authentication")
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), identity.AdminUsername, "admin.totp_disabled", identity.AdminUsername, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password and code are required")
		return
	}
	if t, err := s.Store.GetAdminTOTP(r.Context(), identity.AdminID); err != nil || !t.Confirmed {
		writeJSONError(w, http.StatusConflict, "totp_not_enabled", "two-factor authentication is not enabled")
		return
	}
	if !s.reauthenticate(w, r, identity.AdminID, req) {
		return
	}
	codes, hashes, err := newRecoveryCodes()
	if err == nil {
		err = s.Store.ReplaceRecoveryCodes(r.Context(), identity.AdminID, hashes)
	}
	if err != nil {
		s.Logger.Error("regenerate_recovery_codes_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not regenerate recovery codes")
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), identity.AdminUsername, "admin.recovery_codes_regenerated", identity.AdminUsername, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// handleResetAdminMFA lets a super admin strip every second factor from another admin
// (lost phone, lost recovery codes).
func (s *Server) handleResetAdminMFA(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	target, err := s.Store.GetAdminByID(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrAdminNotFound) {
		writeJSONError(w, http.StatusNotFound, "admin_not_found", "no admin with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reset two-factor authentication")
		return
	}
	if err := s.Store.ResetAdminMFA(ctx, target.ID); err != nil {
		s.Logger.Error("reset_admin_mfa_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reset two-factor authentication")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.mfa_reset", target.Username, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- security settings (allowlist + lockout) ----

type securitySettingsResponse struct {
	AdminIPAllowlist    []string `json:"admin_ip_allowlist"`
	LoginMaxFailures    int      `json:"login_max_failures"`
	LoginLockoutMinutes int      `json:"login_lockout_minutes"`
	YourIP              string   `json:"your_ip"`
}

func (s *Server) handleGetSecuritySettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.GetSecuritySettings(r.Context())
	if err != nil {
		s.Logger.Error("get_security_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch security settings")
		return
	}
	writeJSON(w, http.StatusOK, securitySettingsResponse{cfg.AdminIPAllowlist, cfg.LoginMaxFailures, cfg.LoginLockoutMinutes, clientIP(r)})
}

type updateSecuritySettingsRequest struct {
	AdminIPAllowlist    *[]string `json:"admin_ip_allowlist"`
	LoginMaxFailures    *int      `json:"login_max_failures"`
	LoginLockoutMinutes *int      `json:"login_lockout_minutes"`
}

func (s *Server) handleUpdateSecuritySettings(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	var req updateSecuritySettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.LoginMaxFailures != nil && (*req.LoginMaxFailures < 1 || *req.LoginMaxFailures > 100) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "login_max_failures must be between 1 and 100")
		return
	}
	if req.LoginLockoutMinutes != nil && (*req.LoginLockoutMinutes < 1 || *req.LoginLockoutMinutes > 1440) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "login_lockout_minutes must be between 1 and 1440")
		return
	}
	var list []string
	if req.AdminIPAllowlist != nil {
		if len(*req.AdminIPAllowlist) > 100 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "the allowlist can hold at most 100 entries")
			return
		}
		prefixes, norm, err := parseAllowlist(*req.AdminIPAllowlist)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		// Refuse a list that would lock the requesting admin out of the panel.
		if len(prefixes) > 0 && !ipInPrefixes(clientIP(r), prefixes) {
			writeJSONError(w, http.StatusBadRequest, "allowlist_excludes_you", "this list would block your own IP address ("+clientIP(r)+") - add it first")
			return
		}
		list = norm
		if list == nil {
			list = []string{}
		}
	}
	cfg, err := s.Store.UpdateSecuritySettings(r.Context(), list, req.LoginMaxFailures, req.LoginLockoutMinutes)
	if err != nil {
		s.Logger.Error("update_security_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update security settings")
		return
	}
	s.invalidateSecurityCache()
	if err := s.Store.InsertAuditLog(r.Context(), identity.AdminUsername, "settings.security_updated", "panel_settings", nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusOK, securitySettingsResponse{cfg.AdminIPAllowlist, cfg.LoginMaxFailures, cfg.LoginLockoutMinutes, clientIP(r)})
}

func (s *Server) handleClearAllowlist(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ClearAdminIPAllowlist(r.Context()); err != nil {
		s.Logger.Error("clear_allowlist_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not clear the allowlist")
		return
	}
	s.invalidateSecurityCache()
	if err := s.Store.InsertAuditLog(r.Context(), "cli", "settings.allowlist_cleared", "panel_settings", nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
