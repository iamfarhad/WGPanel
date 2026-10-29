package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// registerNotificationRoutes wires the notification center, channels, webhooks and
// scheduled-backup endpoints. Reseller sessions reach only the notification-center
// and webhook routes listed in reseller_gate.go.
func (s *Server) registerNotificationRoutes(mux *http.ServeMux) {
	// In-panel notification center: any admin session (a reseller sees only its own
	// namespace's events - see notificationScope).
	mux.Handle("GET /api/v1/notifications", s.requireAdmin(http.HandlerFunc(s.handleListNotifications)))
	mux.Handle("POST /api/v1/notifications/read-all", s.requireAdmin(http.HandlerFunc(s.handleReadAllNotifications)))
	mux.Handle("POST /api/v1/notifications/{id}/read", s.requireAdmin(http.HandlerFunc(s.handleReadNotification)))

	// Channels hold SMTP/bot credentials: super_admin only.
	mux.Handle("GET /api/v1/notification-channels", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListChannels))))
	mux.Handle("POST /api/v1/notification-channels", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleCreateChannel))))
	mux.Handle("PATCH /api/v1/notification-channels/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateChannel))))
	mux.Handle("DELETE /api/v1/notification-channels/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleDeleteChannel))))
	mux.Handle("POST /api/v1/notification-channels/{id}/test", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleTestChannel))))

	// Webhooks: staff (operator+) manage global endpoints, resellers their own.
	mux.Handle("GET /api/v1/webhooks", s.requireAdmin(s.requireRoleOrReseller("support", http.HandlerFunc(s.handleListWebhooks))))
	mux.Handle("POST /api/v1/webhooks", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleCreateWebhook))))
	mux.Handle("PATCH /api/v1/webhooks/{id}", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleUpdateWebhook))))
	mux.Handle("DELETE /api/v1/webhooks/{id}", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleDeleteWebhook))))
	mux.Handle("POST /api/v1/webhooks/{id}/rotate-secret", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleRotateWebhookSecret))))
	mux.Handle("POST /api/v1/webhooks/{id}/test", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleTestWebhook))))
	mux.Handle("GET /api/v1/webhooks/{id}/deliveries", s.requireAdmin(s.requireRoleOrReseller("support", http.HandlerFunc(s.handleListDeliveries))))
	mux.Handle("POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver", s.requireAdmin(s.requireRoleOrReseller("operator", http.HandlerFunc(s.handleRedeliver))))
	mux.Handle("GET /api/v1/webhook-settings", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleGetWebhookSettings))))
	mux.Handle("PATCH /api/v1/webhook-settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateWebhookSettings))))

	// Scheduled off-box backups: super_admin only (the file holds every secret).
	mux.Handle("GET /api/v1/backup-schedule", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGetBackupSchedule))))
	mux.Handle("PUT /api/v1/backup-schedule", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateBackupSchedule))))
	mux.Handle("POST /api/v1/backup-schedule/run", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRunBackupNow))))
}

// notificationScope returns the namespace filter for the caller: nil for staff
// (everything), the reseller's own namespace otherwise.
func notificationScope(identity *CallerIdentity) *string {
	if identity.ResellerID != "" {
		ns := store.ResellerNamespace(identity.ResellerID)
		return &ns
	}
	return nil
}

// ---- notification center --------------------------------------------------------

type notificationResponse struct {
	ID        int64          `json:"id"`
	Event     string         `json:"event"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Data      map[string]any `json:"data"`
	Read      bool           `json:"read"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	scope := notificationScope(identity)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	unreadOnly := r.URL.Query().Get("unread") == "true"

	items, err := s.Store.ListNotifications(ctx, identity.AdminID, scope, limit, unreadOnly)
	if err != nil {
		s.Logger.Error("list_notifications_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list notifications")
		return
	}
	unread, err := s.Store.CountUnreadNotifications(ctx, identity.AdminID, scope)
	if err != nil {
		s.Logger.Error("count_notifications_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list notifications")
		return
	}
	out := make([]notificationResponse, 0, len(items))
	for _, n := range items {
		var data map[string]any
		_ = json.Unmarshal(n.Data, &data)
		out = append(out, notificationResponse{ID: n.ID, Event: n.Event, Severity: n.Severity, Title: n.Title, Body: n.Body, Data: data, Read: n.Read, CreatedAt: n.CreatedAt})
	}
	broken := 0
	if scope == nil && roleRank(identity.AdminRole) >= roleRank("operator") {
		if chans, err := s.Store.ListChannels(ctx); err == nil {
			for _, c := range chans {
				if c.Enabled && c.Broken() {
					broken++
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "unread_count": unread, "broken_channels": broken})
}

func (s *Server) handleReadNotification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "notification_not_found", "no notification with that id")
		return
	}
	ok, err := s.Store.MarkNotificationRead(ctx, identity.AdminID, notificationScope(identity), id)
	if err != nil {
		s.Logger.Error("mark_notification_read_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not mark notification as read")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "notification_not_found", "no notification with that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadAllNotifications(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	if err := s.Store.MarkAllNotificationsRead(r.Context(), identity.AdminID); err != nil {
		s.Logger.Error("mark_all_read_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not mark notifications as read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- channels -------------------------------------------------------------------

type channelResponse struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Name                string          `json:"name"`
	Enabled             bool            `json:"enabled"`
	Config              json.RawMessage `json:"config"`
	HasSecret           bool            `json:"has_secret"`
	Events              []string        `json:"events"`
	Lang                string          `json:"lang"`
	Broken              bool            `json:"broken"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	LastError           string          `json:"last_error"`
	LastSuccessAt       *time.Time      `json:"last_success_at"`
	LastFailureAt       *time.Time      `json:"last_failure_at"`
}

func toChannelResponse(c store.NotificationChannel) channelResponse {
	ev := c.Events
	if ev == nil {
		ev = []string{}
	}
	return channelResponse{ID: c.ID, Kind: c.Kind, Name: c.Name, Enabled: c.Enabled, Config: c.Config, HasSecret: c.SecretEncrypted != "",
		Events: ev, Lang: c.Lang, Broken: c.Broken(), ConsecutiveFailures: c.ConsecutiveFailures, LastError: c.LastError,
		LastSuccessAt: c.LastSuccessAt, LastFailureAt: c.LastFailureAt}
}

type channelRequest struct {
	Kind    string          `json:"kind"`
	Name    *string         `json:"name"`
	Enabled *bool           `json:"enabled"`
	Config  json.RawMessage `json:"config"`
	// Secret is the bot token / SMTP password. Omitted or empty on update = keep.
	Secret *string  `json:"secret"`
	Events []string `json:"events"`
	Lang   *string  `json:"lang"`
}

// validateChannelConfig checks the kind-specific config and returns its normalised JSON.
func validateChannelConfig(kind string, raw json.RawMessage) (json.RawMessage, string) {
	switch kind {
	case "telegram":
		var c telegramConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, "config must be an object with chat_ids"
		}
		var ids []string
		for _, id := range c.ChatIDs {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, "at least one Telegram chat id is required"
		}
		out, _ := json.Marshal(telegramConfig{ChatIDs: ids})
		return out, ""
	case "email":
		var c emailConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, "config must be an object with host, port, from and to"
		}
		c.Host = strings.TrimSpace(c.Host)
		if c.Host == "" || c.Port < 1 || c.Port > 65535 {
			return nil, "SMTP host and a valid port are required"
		}
		if _, err := mail.ParseAddress(c.From); err != nil {
			return nil, "a valid From address is required"
		}
		if len(c.To) == 0 {
			return nil, "at least one recipient is required"
		}
		for _, to := range c.To {
			if _, err := mail.ParseAddress(to); err != nil {
				return nil, "recipient " + to + " is not a valid email address"
			}
		}
		switch c.TLS {
		case "starttls", "tls", "none":
		case "":
			c.TLS = "starttls"
		default:
			return nil, "tls must be starttls, tls or none"
		}
		out, _ := json.Marshal(c)
		return out, ""
	}
	return nil, "kind must be telegram or email"
}

func validEventList(list []string) (string, bool) {
	for _, e := range list {
		if !events.Valid(e) {
			return e, false
		}
	}
	return "", true
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.Store.ListChannels(r.Context())
	if err != nil {
		s.Logger.Error("list_channels_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list channels")
		return
	}
	out := make([]channelResponse, 0, len(chans))
	for _, c := range chans {
		out = append(out, toChannelResponse(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "events": events.All()})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	cfg, msg := validateChannelConfig(req.Kind, req.Config)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if req.Kind == "telegram" && (req.Secret == nil || strings.TrimSpace(*req.Secret) == "") {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "the Telegram bot token is required")
		return
	}
	if bad, ok := validEventList(req.Events); !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown event "+bad)
		return
	}
	lang := "en"
	if req.Lang != nil {
		lang = *req.Lang
	}
	if lang != "en" && lang != "fa" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "lang must be en or fa")
		return
	}
	name := strings.TrimSpace(derefStr(req.Name))
	if name == "" {
		name = strings.ToUpper(req.Kind[:1]) + req.Kind[1:]
	}
	enc := ""
	if req.Secret != nil && *req.Secret != "" {
		var err error
		if enc, err = wgkeys.Encrypt(s.AccountKeyEncryptionKey, *req.Secret); err != nil {
			s.Logger.Error("encrypt_channel_secret_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not store credentials")
			return
		}
	}
	enabled := req.Enabled == nil || *req.Enabled
	ch, err := s.Store.CreateChannel(r.Context(), store.NotificationChannel{Kind: req.Kind, Name: name, Enabled: enabled, Config: cfg, SecretEncrypted: enc, Events: req.Events, Lang: lang})
	if err != nil {
		s.Logger.Error("create_channel_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create channel")
		return
	}
	s.auditAdmin(r, "notification_channel.created", ch.ID, map[string]string{"kind": ch.Kind})
	writeJSON(w, http.StatusCreated, toChannelResponse(ch))
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	cur, err := s.Store.GetChannel(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrChannelNotFound) {
		writeJSONError(w, http.StatusNotFound, "channel_not_found", "no notification channel with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_channel_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load channel")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		cur.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if len(req.Config) > 0 {
		cfg, msg := validateChannelConfig(cur.Kind, req.Config)
		if msg != "" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		cur.Config = cfg
	}
	if req.Events != nil {
		if bad, ok := validEventList(req.Events); !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown event "+bad)
			return
		}
		cur.Events = req.Events
	}
	if req.Lang != nil {
		if *req.Lang != "en" && *req.Lang != "fa" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "lang must be en or fa")
			return
		}
		cur.Lang = *req.Lang
	}
	if req.Secret != nil && *req.Secret != "" {
		if cur.SecretEncrypted, err = wgkeys.Encrypt(s.AccountKeyEncryptionKey, *req.Secret); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not store credentials")
			return
		}
	}
	out, err := s.Store.UpdateChannel(ctx, cur)
	if err != nil {
		s.Logger.Error("update_channel_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update channel")
		return
	}
	s.auditAdmin(r, "notification_channel.updated", out.ID, nil)
	writeJSON(w, http.StatusOK, toChannelResponse(out))
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteChannel(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrChannelNotFound) {
		writeJSONError(w, http.StatusNotFound, "channel_not_found", "no notification channel with that id")
		return
	}
	if err != nil {
		s.Logger.Error("delete_channel_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete channel")
		return
	}
	s.auditAdmin(r, "notification_channel.deleted", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestChannel sends a test message through one channel and reports the real
// outcome - the failure streak is updated exactly like a real delivery.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ch, err := s.Store.GetChannel(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrChannelNotFound) {
		writeJSONError(w, http.StatusNotFound, "channel_not_found", "no notification channel with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load channel")
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.sendToChannel(tctx, ch, events.SeverityInfo, renderEvent(events.Event{Type: "test"}, ch.Lang)); err != nil {
		writeJSONError(w, http.StatusBadGateway, "channel_test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// auditAdmin records an admin action in the audit log (best effort).
func (s *Server) auditAdmin(r *http.Request, action, target string, detail any) {
	identity, ok := callerIdentityFromContext(r.Context())
	if !ok {
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), auditActor(identity), action, target, detail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}
