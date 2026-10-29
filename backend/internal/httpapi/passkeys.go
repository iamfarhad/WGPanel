package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"wgpanel-api/internal/store"
)

const (
	waSessionTTL     = 5 * time.Minute
	maxPasskeys      = 10
	maxPasskeyName   = 64
	maxWebAuthnBytes = 64 << 10
)

// waUser adapts an admin and its stored passkeys to webauthn.User. The user handle is
// the admin id, so a discoverable (usernameless) assertion maps straight back to it.
type waUser struct {
	admin store.Admin
	creds []store.Passkey
}

func (u waUser) WebAuthnID() []byte          { return []byte(u.admin.ID) }
func (u waUser) WebAuthnName() string        { return u.admin.Username }
func (u waUser) WebAuthnDisplayName() string { return u.admin.Username }
func (u waUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.creds))
	for _, p := range u.creds {
		out = append(out, passkeyToCredential(p))
	}
	return out
}

func passkeyToCredential(p store.Passkey) webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, 0, len(p.Transports))
	for _, t := range p.Transports {
		transports = append(transports, protocol.AuthenticatorTransport(t))
	}
	sc := p.SignCount
	if sc < 0 {
		sc = 0
	}
	return webauthn.Credential{
		ID:              p.CredentialID,
		PublicKey:       p.PublicKey,
		AttestationType: p.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			UserPresent:    true,
			UserVerified:   true, // registration requires user verification
			BackupEligible: p.BackupEligible,
			BackupState:    p.BackupState,
		},
		Authenticator: webauthn.Authenticator{AAGUID: p.AAGUID, SignCount: uint32(sc)},
	}
}

// webAuthnFor derives the relying party from the panel domain (panel settings, falling
// back to the PANEL_DOMAIN the stack booted with). The RP ID is the bare hostname and the
// origin is the one the browser actually used, accepted only when its host equals the RP
// ID and it is https - or when it is http://localhost (development). A request from any
// other host cannot register or use passkeys.
func (s *Server) webAuthnFor(r *http.Request) (*webauthn.WebAuthn, error) {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || origin == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("%w: missing Origin header", errPasskeyUnavailable)
	}
	host := strings.ToLower(u.Hostname())
	rpID := ""
	if host == "localhost" && u.Scheme == "http" {
		rpID = "localhost"
	} else {
		domain := s.BootPanelDomain
		if st, err := s.Store.GetSettings(r.Context()); err == nil && st.PanelDomain != nil && *st.PanelDomain != "" {
			domain = *st.PanelDomain
		}
		if h, _, err := net.SplitHostPort(domain); err == nil {
			domain = h
		}
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" || host != domain || u.Scheme != "https" {
			return nil, fmt.Errorf("%w: origin %q does not match the panel domain", errPasskeyUnavailable, origin)
		}
		rpID = domain
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Jirjirak WireGuard",
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

func (s *Server) waUnavailable(w http.ResponseWriter, err error) {
	s.Logger.Warn("webauthn_unavailable", "error", err)
	writeJSONError(w, http.StatusBadRequest, "passkeys_unavailable", "passkeys need the panel to be opened over HTTPS on its configured domain")
}

// ---- session storage (Redis, short TTL, single use) ----

type waSessionRecord struct {
	Session  webauthn.SessionData `json:"session"`
	AdminID  string               `json:"admin_id,omitempty"`
	MFAToken string               `json:"mfa_token,omitempty"`
}

func (s *Server) saveWASession(ctx context.Context, kind string, rec waSessionRecord) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	return id, s.Redis.Set(ctx, "wa:sess:"+kind+":"+id, b, waSessionTTL).Err()
}

// takeWASession loads and deletes the session in one step (single use).
func (s *Server) takeWASession(ctx context.Context, kind, id string) (waSessionRecord, bool) {
	var rec waSessionRecord
	if id == "" {
		return rec, false
	}
	b, err := s.Redis.GetDel(ctx, "wa:sess:"+kind+":"+id).Bytes()
	if err != nil {
		return rec, false
	}
	return rec, json.Unmarshal(b, &rec) == nil
}

// limitBody caps the credential JSON the WebAuthn library reads from the request.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebAuthnBytes)
}

type passkeyBeginResponse struct {
	SessionID string `json:"session_id"`
	Options   any    `json:"options"`
}

// ---- registration (self-service, JWT) ----

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	admin, err := s.Store.GetAdminByID(ctx, identity.AdminID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return
	}
	existing, err := s.Store.ListPasskeys(ctx, admin.ID)
	if err != nil {
		s.Logger.Error("list_passkeys_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey registration")
		return
	}
	if len(existing) >= maxPasskeys {
		writeJSONError(w, http.StatusConflict, "passkey_limit", "you have reached the maximum number of passkeys")
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	user := waUser{admin: admin, creds: existing}
	excl := make([]protocol.CredentialDescriptor, 0, len(existing))
	for _, c := range user.WebAuthnCredentials() {
		excl = append(excl, c.Descriptor())
	}
	creation, session, err := wa.BeginRegistration(user, webauthn.WithExclusions(excl))
	if err != nil {
		s.Logger.Error("webauthn_begin_registration_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey registration")
		return
	}
	id, err := s.saveWASession(ctx, "reg", waSessionRecord{Session: *session, AdminID: admin.ID})
	if err != nil {
		s.Logger.Error("webauthn_session_store_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey registration")
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{SessionID: id, Options: creation})
}

type passkeyResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

func toPasskeyResponse(p store.Passkey) passkeyResponse {
	out := passkeyResponse{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt.Format(time.RFC3339)}
	if p.LastUsedAt != nil {
		v := p.LastUsedAt.Format(time.RFC3339)
		out.LastUsedAt = &v
	}
	return out
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	rec, ok := s.takeWASession(ctx, "reg", r.URL.Query().Get("session"))
	if !ok || rec.AdminID != identity.AdminID {
		writeJSONError(w, http.StatusBadRequest, "passkey_session_expired", "the passkey registration expired - start again")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len([]rune(name)) > maxPasskeyName {
		name = string([]rune(name)[:maxPasskeyName])
	}
	admin, err := s.Store.GetAdminByID(ctx, identity.AdminID)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return
	}
	existing, _ := s.Store.ListPasskeys(ctx, admin.ID)
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	limitBody(w, r)
	cred, err := wa.FinishRegistration(waUser{admin: admin, creds: existing}, rec.Session, r)
	if err != nil {
		s.Logger.Warn("webauthn_finish_registration_failed", "error", err)
		writeJSONError(w, http.StatusBadRequest, "passkey_verification_failed", "the passkey could not be verified")
		return
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	pk, err := s.Store.CreatePasskey(ctx, store.Passkey{
		AdminID: admin.ID, Name: name, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: int64(cred.Authenticator.SignCount), AAGUID: cred.Authenticator.AAGUID, Transports: transports,
		AttestationType: cred.AttestationType, BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState,
	})
	if err != nil {
		s.Logger.Error("create_passkey_failed", "error", err)
		writeJSONError(w, http.StatusConflict, "passkey_exists", "this passkey is already registered")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, admin.Username, "admin.passkey_added", admin.Username, map[string]string{"name": name}, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusCreated, toPasskeyResponse(pk))
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ctx := r.Context()
	if err := s.Store.DeletePasskey(ctx, identity.AdminID, r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrPasskeyNotFound) {
			writeJSONError(w, http.StatusNotFound, "passkey_not_found", "no passkey with that id")
			return
		}
		s.Logger.Error("delete_passkey_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete passkey")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.passkey_removed", identity.AdminUsername, nil, clientIP(r)); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- login: shared assertion bookkeeping ----

// finishAssertion applies the post-verification checks and persistence common to the
// passwordless and second-factor flows: a sign counter that did not advance (cloned
// authenticator) is refused, otherwise the new counter is stored.
func (s *Server) finishAssertion(ctx context.Context, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		return errors.New("sign count did not increase: possible cloned authenticator")
	}
	pk, err := s.Store.GetPasskeyByCredentialID(ctx, cred.ID)
	if err != nil {
		return err
	}
	return s.Store.TouchPasskey(ctx, pk.ID, int64(cred.Authenticator.SignCount), cred.Flags.BackupState)
}

func (s *Server) passkeyFailed(w http.ResponseWriter, r *http.Request, username string, cause error) {
	s.Logger.Warn("passkey_assertion_failed", "error", cause)
	if wait, locked := s.recordLoginFailure(r.Context(), username, clientIP(r)); locked {
		writeTooManyAttempts(w, wait)
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid_passkey", "the passkey could not be verified")
}

// ---- passwordless login (discoverable credential) ----

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.enforceAdminIPAllowlist(w, r) {
		return
	}
	ctx := r.Context()
	if wait := s.loginLockedFor(ctx, "", clientIP(r)); wait > 0 {
		writeTooManyAttempts(w, wait)
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.Logger.Error("webauthn_begin_login_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey sign-in")
		return
	}
	id, err := s.saveWASession(ctx, "login", waSessionRecord{Session: *session})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey sign-in")
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{SessionID: id, Options: assertion})
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.enforceAdminIPAllowlist(w, r) {
		return
	}
	ctx := r.Context()
	if wait := s.loginLockedFor(ctx, "", clientIP(r)); wait > 0 {
		writeTooManyAttempts(w, wait)
		return
	}
	rec, ok := s.takeWASession(ctx, "login", r.URL.Query().Get("session"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "passkey_session_expired", "the passkey sign-in expired - try again")
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	var owner store.Admin
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		pk, err := s.Store.GetPasskeyByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		if string(userHandle) != pk.AdminID {
			return nil, errors.New("user handle does not match credential owner")
		}
		admin, err := s.Store.GetAdminByID(ctx, pk.AdminID)
		if err != nil {
			return nil, err
		}
		owner = admin
		return waUser{admin: admin, creds: []store.Passkey{pk}}, nil
	}
	limitBody(w, r)
	cred, err := wa.FinishDiscoverableLogin(handler, rec.Session, r)
	if err != nil {
		s.passkeyFailed(w, r, "", err)
		return
	}
	if err := s.finishAssertion(ctx, cred); err != nil {
		s.passkeyFailed(w, r, owner.Username, err)
		return
	}
	s.completeLogin(w, r, owner, "passkey")
}

// ---- passkey as the second factor (after a correct password) ----

type mfaPasskeyBeginRequest struct {
	MFAToken string `json:"mfa_token"`
}

func (s *Server) handleMFAPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	var req mfaPasskeyBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MFAToken == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "mfa_token is required")
		return
	}
	admin, ok := s.mfaContext(w, r, req.MFAToken)
	if !ok {
		return
	}
	ctx := r.Context()
	creds, err := s.Store.ListPasskeys(ctx, admin.ID)
	if err != nil || len(creds) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_passkeys", "this account has no passkeys")
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	assertion, session, err := wa.BeginLogin(waUser{admin: admin, creds: creds}, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.Logger.Error("webauthn_begin_login_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey verification")
		return
	}
	id, err := s.saveWASession(ctx, "mfa", waSessionRecord{Session: *session, AdminID: admin.ID, MFAToken: req.MFAToken})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start passkey verification")
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{SessionID: id, Options: assertion})
}

func (s *Server) handleMFAPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rec, ok := s.takeWASession(ctx, "mfa", r.URL.Query().Get("session"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "passkey_session_expired", "the passkey verification expired - try again")
		return
	}
	admin, ok := s.mfaContext(w, r, rec.MFAToken)
	if !ok {
		return
	}
	if admin.ID != rec.AdminID {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return
	}
	creds, err := s.Store.ListPasskeys(ctx, admin.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "passkey verification failed")
		return
	}
	wa, err := s.webAuthnFor(r)
	if err != nil {
		s.waUnavailable(w, err)
		return
	}
	limitBody(w, r)
	cred, err := wa.FinishLogin(waUser{admin: admin, creds: creds}, rec.Session, r)
	if err == nil {
		err = s.finishAssertion(ctx, cred)
	}
	if err != nil {
		s.Logger.Warn("passkey_mfa_failed", "error", err)
		s.mfaFailed(w, r, rec.MFAToken, admin.Username)
		return
	}
	if !s.consumeMFAToken(ctx, rec.MFAToken) {
		writeJSONError(w, http.StatusUnauthorized, "mfa_token_invalid", "the verification step expired - sign in again")
		return
	}
	s.completeLogin(w, r, admin, "passkey")
}
