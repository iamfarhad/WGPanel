package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// Marzban-compatible API facade (docs/marzban-compat.md).
//
// Why: Iranian sellers pick panels the Telegram sales bots (Mirza panel, Faoxima,
// ShopVPN...) support, and those speak Marzban's REST API. This file set makes a bot
// configured with panel type "Marzban" work unmodified against this panel. It is a thin
// translation layer: every state change goes through the same store methods and event /
// audit code as /api/v1/accounts, so quota and reseller caps, enable-refuses-over-cap,
// renew semantics, webhooks and the audit log are not re-implemented here.
//
// Files: marzbancompat_api.go (routes, errors, token, system), marzbancompat_auth.go
// (credentials and bearer tokens), marzbancompat_users.go (the /api/user endpoints).
//
// Route paths cannot collide with the panel API: everything of ours lives under
// /api/v1/, the facade under /api/admin, /api/user(s), /api/system, /api/inbounds, ...
// Caddy proxies /api/* on the panel domain to the Go API (deploy/Caddyfile), so no proxy
// change is needed. The separate subscription domain only serves /api/v1/sub/* and the
// /s/* page (internal/caddyadmin), so bots must be pointed at the PANEL domain.

// registerMarzbanCompatRoutes wires the facade's bot-facing routes. Nothing here uses
// requireAdmin or the reseller allowlist: callers are API keys, authenticated by mzRoute
// with a bearer token that a panel (JWT) session can never produce. The two admin-UI
// routes for the on/off switch are registered in server.go, next to Settings, where
// TestResellerAllowlist's source scan classifies them.
func (s *Server) registerMarzbanCompatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/token", s.handleMzToken)
	mux.Handle("GET /api/admin", s.mzRoute("", s.handleMzCurrentAdmin))

	mux.Handle("POST /api/user", s.mzRoute("create", s.handleMzCreateUser))
	mux.Handle("GET /api/user/{username}", s.mzRoute("read", s.handleMzGetUser))
	mux.Handle("PUT /api/user/{username}", s.mzRoute("update", s.handleMzModifyUser))
	mux.Handle("DELETE /api/user/{username}", s.mzRoute("delete", s.handleMzDeleteUser))
	mux.Handle("POST /api/user/{username}/reset", s.mzRoute("update", s.handleMzResetUser))
	mux.Handle("POST /api/user/{username}/revoke_sub", s.mzRoute("update", s.handleMzRevokeSub))
	mux.Handle("POST /api/user/{username}/active-next", s.mzRoute("update", s.handleMzActiveNext))
	mux.Handle("GET /api/user/{username}/usage", s.mzRoute("read", s.handleMzUserUsage))
	mux.Handle("GET /api/users", s.mzRoute("read", s.handleMzListUsers))
	mux.Handle("GET /api/users/expired", s.mzRoute("read", s.handleMzListExpired))
	mux.Handle("DELETE /api/users/expired", s.mzRoute("delete", s.handleMzDeleteExpired))

	mux.Handle("GET /api/system", s.mzRoute("read", s.handleMzSystem))
	mux.Handle("GET /api/inbounds", s.mzRoute("read", s.handleMzInbounds))
	// Answered with empty lists so a bot that probes them does not take a 404 for a
	// broken panel: we have no Marzban-style user templates or nodes to expose.
	mux.Handle("GET /api/user-templates", s.mzRoute("read", s.handleMzEmptyList))
	mux.Handle("GET /api/nodes", s.mzRoute("read", s.handleMzEmptyList))
}

// ---- Marzban error shapes (FastAPI) ----

// mzDetail writes {"detail": "..."} - the shape of every HTTPException: 401, 403, 404,
// 409 and the 4xx a bot reads its message from.
func mzDetail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

// mzFieldError is one entry of a 422 "detail" list (pydantic v1 shape).
type mzFieldError struct {
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
	Type string   `json:"type"`
}

func mzBodyError(field, msg, typ string) mzFieldError {
	return mzFieldError{Loc: []string{"body", field}, Msg: msg, Type: typ}
}

func mzValidation(w http.ResponseWriter, errs ...mzFieldError) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
}

// mzAPIError maps the panel's own apiError onto Marzban's wording where the bots key on
// it ("User not found", "User already exists") and passes the message through otherwise.
func mzAPIError(w http.ResponseWriter, e *apiError) {
	switch e.Code {
	case "account_not_found":
		mzDetail(w, http.StatusNotFound, "User not found")
	case "external_ref_taken":
		mzDetail(w, http.StatusConflict, "User already exists")
	default:
		mzDetail(w, e.Status, e.Message)
	}
}

// ---- switch endpoints (panel UI) ----

func (s *Server) handleGetMarzbanCompat(w http.ResponseWriter, r *http.Request) {
	on, err := s.Store.GetMarzbanCompatEnabled(r.Context())
	if err != nil {
		s.Logger.Error("get_marzban_compat_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read the setting")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (s *Server) handleSetMarzbanCompat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "body must be {\"enabled\": true|false}")
		return
	}
	ctx := r.Context()
	if err := s.Store.SetMarzbanCompatEnabled(ctx, *req.Enabled); err != nil {
		s.Logger.Error("set_marzban_compat_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update the setting")
		return
	}
	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "settings.marzban_compat_updated", "panel_settings", map[string]bool{"enabled": *req.Enabled}, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}

// ---- POST /api/admin/token ----

// handleMzToken is Marzban's OAuth2 password login: form fields username and password,
// answered with {"access_token", "token_type": "bearer"}. See marzbancompat_auth.go for
// what the credentials are.
func (s *Server) handleMzToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.mzEnabled(ctx) {
		mzDetail(w, http.StatusNotFound, "Not Found")
		return
	}
	// Unauthenticated, so budget guesses per source address before doing any lookup.
	if res := s.takeToken(ctx, "mzlogin:"+clientIP(r), 1, 20, 1); !res.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(ceilSeconds(res.RetryAfter)))
		mzDetail(w, http.StatusTooManyRequests, "Too many requests")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		mzValidation(w, mzFieldError{Loc: []string{"body"}, Msg: "malformed form body", Type: "value_error"})
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	var missing []mzFieldError
	if username == "" {
		missing = append(missing, mzBodyError("username", "field required", "value_error.missing"))
	}
	if password == "" {
		missing = append(missing, mzBodyError("password", "field required", "value_error.missing"))
	}
	if len(missing) > 0 {
		mzValidation(w, missing...)
		return
	}

	bad := func() {
		s.Logger.Warn("marzban_compat_login_failed", "key_id", username, "ip", clientIP(r))
		w.Header().Set("WWW-Authenticate", "Bearer")
		mzDetail(w, http.StatusUnauthorized, "Incorrect username or password")
	}
	key, err := s.Store.GetAPIKeyByKeyID(ctx, username)
	if errors.Is(err, store.ErrAPIKeyNotFound) {
		bad()
		return
	}
	if err != nil {
		s.Logger.Error("marzban_compat_get_api_key_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	ok, err := s.mzSecretMatches(key, password)
	if err != nil {
		s.Logger.Error("marzban_compat_verify_secret_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !ok || key.RevokedAt != nil {
		bad()
		return
	}
	// Refuse a token for a key whose owner (a reseller) can no longer act, so the bot
	// sees the reason at login rather than on its first call.
	if _, aerr := s.apiKeyIdentity(ctx, key); aerr != nil {
		mzDetail(w, aerr.Status, aerr.Message)
		return
	}
	token, err := s.mzIssueToken(key)
	if err != nil {
		s.Logger.Error("marzban_compat_issue_token_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer"})
}

// ---- GET /api/admin, /api/system, /api/inbounds ----

// handleMzCurrentAdmin answers Marzban's "who am I": the API key is the admin. It is
// never sudo - a key cannot manage other admins, which the facade does not expose anyway.
func (s *Server) handleMzCurrentAdmin(w http.ResponseWriter, r *http.Request, c mzCaller) {
	accounts, err := s.Store.ListAccountsForCompat(r.Context(), c.NS, "", nil)
	if err != nil {
		s.Logger.Error("marzban_compat_list_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	var used int64
	for _, a := range accounts {
		used += a.DataUsedBytes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":        c.Key.KeyID,
		"is_sudo":         false,
		"telegram_id":     nil,
		"discord_webhook": nil,
		"users_usage":     used,
	})
}

// mzReportedVersion is what GET /api/system says. The Mirza bot switches to Marzban's
// newer "groups" API when version > 0.8.4 (and then calls /api/group, /api/cores ...
// which we do not have), so we claim exactly the last pre-groups release.
const mzReportedVersion = "0.8.4"

func (s *Server) handleMzSystem(w http.ResponseWriter, r *http.Request, c mzCaller) {
	accounts, err := s.Store.ListAccountsForCompat(r.Context(), c.NS, "", nil)
	if err != nil {
		s.Logger.Error("marzban_compat_list_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	now := time.Now()
	counts := map[string]int{}
	var used int64
	online := 0
	for _, a := range accounts {
		counts[mzStatus(a, now)]++
		used += a.DataUsedBytes
		if a.LastHandshakeAt != nil && now.Sub(*a.LastHandshakeAt) <= peerOnlineWindow {
			online++
		}
	}
	// Host figures (memory, CPU) belong to the nodes, not to a Marzban-style single
	// core, so they are reported as zero; traffic is the namespace's total usage
	// (we do not split rx/tx per account, so it is all "incoming").
	writeJSON(w, http.StatusOK, map[string]any{
		"version":                  mzReportedVersion,
		"mem_total":                0,
		"mem_used":                 0,
		"cpu_cores":                0,
		"cpu_usage":                0.0,
		"total_user":               len(accounts),
		"online_users":             online,
		"users_active":             counts["active"],
		"users_on_hold":            counts["on_hold"],
		"users_disabled":           counts["disabled"],
		"users_expired":            counts["expired"],
		"users_limited":            counts["limited"],
		"incoming_bandwidth":       used,
		"outgoing_bandwidth":       0,
		"incoming_bandwidth_speed": 0,
		"outgoing_bandwidth_speed": 0,
	})
}

// handleMzInbounds: Marzban returns {protocol: [inbound...]}. We have one pseudo-inbound
// so a bot that insists on picking inbound tags finds something; the tag is ignored on
// user create/modify.
func (s *Server) handleMzInbounds(w http.ResponseWriter, _ *http.Request, _ mzCaller) {
	writeJSON(w, http.StatusOK, map[string]any{
		"wireguard": []map[string]any{{
			"tag": "wireguard", "protocol": "wireguard", "network": "udp", "tls": "none", "port": 51820,
		}},
	})
}

func (s *Server) handleMzEmptyList(w http.ResponseWriter, _ *http.Request, _ mzCaller) {
	writeJSON(w, http.StatusOK, []any{})
}

// ---- subscription URL ----

// mzOrigin is the public origin subscription links are built on: the separate
// subscription domain when one is configured (Settings), else the panel's own domain
// (Settings' panel_domain, the PANEL_DOMAIN the stack booted with, or public_base_url).
// "" means none is known and links stay relative.
func (s *Server) mzOrigin(settings store.PanelSettings) string {
	if base := subscriptionBaseURL(settings); base != nil {
		return *base
	}
	domain := s.BootPanelDomain
	if settings.PanelDomain != nil && *settings.PanelDomain != "" {
		domain = *settings.PanelDomain
	}
	if domain != "" {
		return "https://" + domain
	}
	if settings.PublicBaseURL != nil {
		return strings.TrimRight(*settings.PublicBaseURL, "/")
	}
	return ""
}

// mzBotURLRE is the exact check the Mirza bot applies to subscription_url: when it does
// NOT match (an IP host, localhost, a port-only dev setup) the bot prepends its panel
// URL to whatever we returned, which would corrupt an absolute URL.
var mzBotURLRE = regexp.MustCompile(`^(https?://)?([a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}(:\d+)?((/[^\s/]+)+)?$`)

// mzAbsoluteOrRelative returns origin+path when the bot would accept it as an absolute
// URL and the bare path otherwise (the bot then prefixes the panel URL it was configured
// with - the same origin, as docs/marzban-compat.md tells the operator to enter).
func mzAbsoluteOrRelative(origin, path string) string {
	if origin != "" {
		if u := origin + path; mzBotURLRE.MatchString(strings.SplitN(u, "?", 2)[0]) {
			return u
		}
	}
	return path
}

// mzSubscriptionURL is the customer-facing subscription page (/s/{token}), which also
// offers the per-node configs. On the subscription domain when one is configured.
func mzSubscriptionURL(origin, token string) string {
	return mzAbsoluteOrRelative(origin, "/s/"+token)
}

func (s *Server) mzSettings(ctx context.Context) store.PanelSettings {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		// Best effort, like subscriptionBaseURLFromStore: relative links beat a 500.
		s.Logger.Warn("marzban_compat_get_settings_failed", "error", err)
	}
	return settings
}
