package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

const (
	mfaTokenTTL = 5 * time.Minute
	// mfaMaxTries wrong second-factor submissions burn the token, forcing a fresh
	// password step (which is itself lockout-counted).
	mfaMaxTries = 5
)

type mfaChallengeResponse struct {
	MFARequired bool     `json:"mfa_required"`
	MFAToken    string   `json:"mfa_token"`
	Methods     []string `json:"methods"`
}

func mfaTokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "mfa:token:" + hex.EncodeToString(sum[:])
}

// beginMFA runs after a correct password. If the admin has a second factor it writes
// the mfa_required response (no session tokens) and returns true; otherwise it
// returns false and the caller completes the login. Also returns true after writing
// an error response.
func (s *Server) beginMFA(w http.ResponseWriter, r *http.Request, admin store.Admin) bool {
	ctx := r.Context()
	totp, passkeys, err := s.Store.HasSecondFactor(ctx, admin.ID)
	if err != nil {
		s.Logger.Error("mfa_lookup_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return true
	}
	if !totp && passkeys == 0 {
		return false
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return true
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := mfaTokenKey(token)
	pipe := s.Redis.TxPipeline()
	pipe.HSet(ctx, key, "admin_id", admin.ID, "tries", 0)
	pipe.Expire(ctx, key, mfaTokenTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		s.Logger.Error("mfa_token_store_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return true
	}
	var methods []string
	if totp {
		methods = append(methods, "totp", "recovery")
	}
	if passkeys > 0 {
		methods = append(methods, "passkey")
	}
	writeJSON(w, http.StatusOK, mfaChallengeResponse{MFARequired: true, MFAToken: token, Methods: methods})
	return true
}

// peekMFAToken returns the admin id behind a still-valid token without consuming it.
func (s *Server) peekMFAToken(ctx context.Context, token string) (string, bool) {
	if token == "" {
		return "", false
	}
	id, err := s.Redis.HGet(ctx, mfaTokenKey(token), "admin_id").Result()
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// consumeMFAToken burns the token; only one concurrent caller gets true (single use).
func (s *Server) consumeMFAToken(ctx context.Context, token string) bool {
	n, err := s.Redis.Del(ctx, mfaTokenKey(token)).Result()
	return err == nil && n == 1
}

// mfaFailed counts a wrong second-factor attempt against the token, the username and
// the IP, and answers 401 (or 429 if that tripped a lockout).
func (s *Server) mfaFailed(w http.ResponseWriter, r *http.Request, token, username string) {
	ctx := r.Context()
	if n, err := s.Redis.HIncrBy(ctx, mfaTokenKey(token), "tries", 1).Result(); err == nil && n >= mfaMaxTries {
		s.Redis.Del(ctx, mfaTokenKey(token))
	}
	if wait, locked := s.recordLoginFailure(ctx, username, clientIP(r)); locked {
		writeTooManyAttempts(w, wait)
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid_mfa_code", "the verification code is invalid")
}

// mfaContext validates the allowlist, lockout and mfa_token shared by every
// second-factor endpoint and loads the admin. ok=false means a response was written.
func (s *Server) mfaContext(w http.ResponseWriter, r *http.Request, token string) (store.Admin, bool) {
	if !s.enforceAdminIPAllowlist(w, r) {
		return store.Admin{}, false
	}
	ctx := r.Context()
	adminID, ok := s.peekMFAToken(ctx, token)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return store.Admin{}, false
	}
	admin, err := s.Store.GetAdminByID(ctx, adminID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return store.Admin{}, false
	}
	if wait := s.loginLockedFor(ctx, admin.Username, clientIP(r)); wait > 0 {
		writeTooManyAttempts(w, wait)
		return store.Admin{}, false
	}
	return admin, true
}

type mfaCodeRequest struct {
	MFAToken     string `json:"mfa_token"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

// checkTOTP validates code against the admin's confirmed secret and records the step
// so the same code cannot be replayed.
func (s *Server) checkTOTP(ctx context.Context, adminID, code string) bool {
	t, err := s.Store.GetAdminTOTP(ctx, adminID)
	if err != nil || !t.Confirmed {
		return false
	}
	secret, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, t.SecretEnc)
	if err != nil {
		s.Logger.Error("totp_decrypt_failed", "error", err)
		return false
	}
	step, ok := verifyTOTP(secret, code, time.Now())
	if !ok {
		return false
	}
	fresh, err := s.Store.AdvanceTOTPStep(ctx, adminID, step)
	return err == nil && fresh
}

func (s *Server) checkRecoveryCode(ctx context.Context, adminID, code string) bool {
	if normalizeRecoveryCode(code) == "" {
		return false
	}
	ok, err := s.Store.ConsumeRecoveryCode(ctx, adminID, hashRecoveryCode(code))
	return err == nil && ok
}

// handleMFATOTP exchanges mfa_token + a 6-digit authenticator code for the session.
func (s *Server) handleMFATOTP(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MFAToken == "" || req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "mfa_token and code are required")
		return
	}
	admin, ok := s.mfaContext(w, r, req.MFAToken)
	if !ok {
		return
	}
	if !s.checkTOTP(r.Context(), admin.ID, req.Code) {
		s.mfaFailed(w, r, req.MFAToken, admin.Username)
		return
	}
	if !s.consumeMFAToken(r.Context(), req.MFAToken) {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return
	}
	s.completeLogin(w, r, admin, "totp")
}

// handleMFARecovery exchanges mfa_token + a one-time recovery code for the session.
func (s *Server) handleMFARecovery(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MFAToken == "" || req.RecoveryCode == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "mfa_token and recovery_code are required")
		return
	}
	admin, ok := s.mfaContext(w, r, req.MFAToken)
	if !ok {
		return
	}
	if !s.checkRecoveryCode(r.Context(), admin.ID, req.RecoveryCode) {
		s.mfaFailed(w, r, req.MFAToken, admin.Username)
		return
	}
	if !s.consumeMFAToken(r.Context(), req.MFAToken) {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), admin.Username, "admin.recovery_code_used", admin.Username, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.completeLogin(w, r, admin, "recovery_code")
}

var errPasskeyUnavailable = errors.New("passkeys unavailable")
