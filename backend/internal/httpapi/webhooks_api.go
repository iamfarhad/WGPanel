package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/webhook"
	"wgpanel-api/internal/wgkeys"
)

type webhookResponse struct {
	ID          string    `json:"id"`
	Namespace   *string   `json:"namespace"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	// Secret is present exactly once: in the create and rotate-secret responses.
	Secret string `json:"secret,omitempty"`
}

func toWebhookResponse(w store.Webhook, secret string) webhookResponse {
	ev := w.Events
	if ev == nil {
		ev = []string{}
	}
	return webhookResponse{ID: w.ID, Namespace: w.Namespace, URL: w.URL, Description: w.Description, Events: ev, Enabled: w.Enabled, CreatedAt: w.CreatedAt, Secret: secret}
}

type deliveryResponse struct {
	ID             string     `json:"id"`
	WebhookID      string     `json:"webhook_id"`
	Event          string     `json:"event"`
	EventID        string     `json:"event_id"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	ResponseStatus *int       `json:"response_status"`
	LatencyMS      *int       `json:"latency_ms"`
	ResponseBody   string     `json:"response_body"`
	Error          string     `json:"error"`
	Payload        string     `json:"payload"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toDeliveryResponse(d store.WebhookDelivery) deliveryResponse {
	return deliveryResponse{ID: d.ID, WebhookID: d.WebhookID, Event: d.Event, EventID: d.EventID, Status: d.Status, Attempts: d.Attempts,
		NextAttemptAt: d.NextAttemptAt, ResponseStatus: d.ResponseStatus, LatencyMS: d.LatencyMS, ResponseBody: d.ResponseBody,
		Error: d.Error, Payload: d.Payload, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
}

// webhookCaller returns (callerNamespace for queries, scoped). Staff sessions get nil:
// they see and manage every webhook, and their own creations are global. A reseller
// (session or reseller-owned key) and an admin-owned API key are scoped to their own
// namespace - the same one their accounts' events carry - and get the reseller rules
// (account.*/reseller.* events only, no private-target override).
func webhookCaller(identity *CallerIdentity) (*string, bool) {
	if identity.ResellerID != "" {
		ns := store.ResellerNamespace(identity.ResellerID)
		return &ns, true
	}
	if !identity.IsAdmin {
		ns := identity.KeyNamespace
		return &ns, true
	}
	return nil, false
}

type webhookRequest struct {
	URL         *string  `json:"url"`
	Description *string  `json:"description"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled"`
}

// checkWebhookTarget validates the URL and applies the SSRF guard. The staff-only
// override never applies to reseller-owned endpoints.
func (s *Server) checkWebhookTarget(r *http.Request, w http.ResponseWriter, rawURL string, reseller bool) bool {
	allow := false
	if !reseller {
		allow = s.allowPrivateWebhooks(r.Context())
	}
	if err := webhook.ValidateURL(rawURL, allow, nil); err != nil {
		code := "invalid_request"
		if errors.Is(err, webhook.ErrBlockedTarget) {
			code = "webhook_url_blocked"
		}
		writeJSONError(w, http.StatusBadRequest, code, err.Error())
		return false
	}
	return true
}

func validWebhookEvents(list []string, reseller bool) string {
	for _, e := range list {
		if !events.Valid(e) {
			return "unknown event " + e
		}
		if reseller && !strings.HasPrefix(e, "account.") && !strings.HasPrefix(e, "reseller.") {
			return "resellers can subscribe to account.* and reseller.* events only"
		}
	}
	return ""
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	list, err := s.Store.ListWebhooks(r.Context(), ns)
	if err != nil {
		s.Logger.Error("list_webhooks_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list webhooks")
		return
	}
	out := make([]webhookResponse, 0, len(list))
	for _, h := range list {
		out = append(out, toWebhookResponse(h, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "events": events.All()})
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, reseller := webhookCaller(identity)
	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "a JSON body with a url is required")
		return
	}
	if msg := validWebhookEvents(req.Events, reseller); msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if !s.checkWebhookTarget(r, w, *req.URL, reseller) {
		return
	}
	secret := webhook.NewSecret()
	enc, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, secret)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not store webhook secret")
		return
	}
	h, err := s.Store.CreateWebhook(r.Context(), store.Webhook{
		Namespace: ns, URL: strings.TrimSpace(*req.URL), Description: derefStr(req.Description), SecretEncrypted: enc,
		Events: req.Events, Enabled: req.Enabled == nil || *req.Enabled, CreatedBy: auditActor(identity),
	})
	if err != nil {
		s.Logger.Error("create_webhook_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create webhook")
		return
	}
	s.auditAdmin(r, "webhook.created", h.ID, map[string]string{"url": h.URL})
	writeJSON(w, http.StatusCreated, toWebhookResponse(h, secret))
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, reseller := webhookCaller(identity)
	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	cur, err := s.Store.GetWebhook(r.Context(), ns, r.PathValue("id"))
	if errors.Is(err, store.ErrWebhookNotFound) {
		writeJSONError(w, http.StatusNotFound, "webhook_not_found", "no webhook with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load webhook")
		return
	}
	if req.URL != nil {
		if !s.checkWebhookTarget(r, w, *req.URL, cur.Namespace != nil) {
			return
		}
		cur.URL = strings.TrimSpace(*req.URL)
	}
	if req.Description != nil {
		cur.Description = *req.Description
	}
	if req.Events != nil {
		if msg := validWebhookEvents(req.Events, reseller || cur.Namespace != nil); msg != "" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		cur.Events = req.Events
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	out, err := s.Store.UpdateWebhook(r.Context(), ns, cur, "")
	if err != nil {
		s.Logger.Error("update_webhook_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update webhook")
		return
	}
	s.auditAdmin(r, "webhook.updated", out.ID, nil)
	writeJSON(w, http.StatusOK, toWebhookResponse(out, ""))
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	err := s.Store.DeleteWebhook(r.Context(), ns, r.PathValue("id"))
	if errors.Is(err, store.ErrWebhookNotFound) {
		writeJSONError(w, http.StatusNotFound, "webhook_not_found", "no webhook with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete webhook")
		return
	}
	s.auditAdmin(r, "webhook.deleted", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	cur, err := s.Store.GetWebhook(r.Context(), ns, r.PathValue("id"))
	if errors.Is(err, store.ErrWebhookNotFound) {
		writeJSONError(w, http.StatusNotFound, "webhook_not_found", "no webhook with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load webhook")
		return
	}
	secret := webhook.NewSecret()
	enc, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, secret)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not store webhook secret")
		return
	}
	out, err := s.Store.UpdateWebhook(r.Context(), ns, cur, enc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate secret")
		return
	}
	s.auditAdmin(r, "webhook.secret_rotated", out.ID, nil)
	writeJSON(w, http.StatusOK, toWebhookResponse(out, secret))
}

// handleTestWebhook sends a signed "ping" event synchronously (no retries) and
// returns the resulting delivery so the UI can show the status code and latency.
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	h, err := s.Store.GetWebhook(r.Context(), ns, r.PathValue("id"))
	if errors.Is(err, store.ErrWebhookNotFound) {
		writeJSONError(w, http.StatusNotFound, "webhook_not_found", "no webhook with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load webhook")
		return
	}
	ev := events.Event{ID: events.NewID(), Type: events.Ping, CreatedAt: time.Now().UTC(), Data: map[string]any{"message": "Webhook test from Jirjirak WireGuard"}}
	if h.Namespace != nil {
		ev.Namespace = *h.Namespace
	}
	d, err := s.Store.EnqueueDelivery(r.Context(), h.ID, ev.Type, ev.ID, marshalPayload(ev))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create test delivery")
		return
	}
	// Claim it inline (pushes next_attempt_at out) so the worker does not double-send.
	_ = s.Store.PushDeliveryLease(r.Context(), d.ID, webhookLease)
	d.Attempts = webhook.MaxAttempts // a test never schedules retries
	res := s.attemptDelivery(r.Context(), d)
	writeJSON(w, http.StatusOK, toDeliveryResponse(res))
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if _, err := s.Store.GetWebhook(r.Context(), ns, r.PathValue("id")); errors.Is(err, store.ErrWebhookNotFound) {
		writeJSONError(w, http.StatusNotFound, "webhook_not_found", "no webhook with that id")
		return
	}
	list, err := s.Store.ListDeliveries(r.Context(), ns, r.PathValue("id"), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list deliveries")
		return
	}
	out := make([]deliveryResponse, 0, len(list))
	for _, d := range list {
		out = append(out, toDeliveryResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleRedeliver queues a fresh delivery with the original payload and event id.
func (s *Server) handleRedeliver(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns, _ := webhookCaller(identity)
	d, err := s.Store.GetDelivery(r.Context(), ns, r.PathValue("id"), r.PathValue("delivery_id"))
	if errors.Is(err, store.ErrDeliveryNotFound) {
		writeJSONError(w, http.StatusNotFound, "delivery_not_found", "no delivery with that id")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load delivery")
		return
	}
	nd, err := s.Store.EnqueueDelivery(r.Context(), d.WebhookID, d.Event, d.EventID, d.Payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not queue delivery")
		return
	}
	writeJSON(w, http.StatusAccepted, toDeliveryResponse(nd))
}

// ---- SSRF override setting (staff) ----------------------------------------------

func (s *Server) handleGetWebhookSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.AllowPrivateWebhookTargets(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"allow_private_targets": v})
}

func (s *Server) handleUpdateWebhookSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowPrivateTargets *bool `json:"allow_private_targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AllowPrivateTargets == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "allow_private_targets is required")
		return
	}
	if err := s.Store.SetAllowPrivateWebhookTargets(r.Context(), *req.AllowPrivateTargets); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save settings")
		return
	}
	s.NotifyRT.mu.Lock()
	s.NotifyRT.allowPrivateAt = time.Time{}
	s.NotifyRT.mu.Unlock()
	s.auditAdmin(r, "webhook_settings.updated", "panel", map[string]bool{"allow_private_targets": *req.AllowPrivateTargets})
	writeJSON(w, http.StatusOK, map[string]any{"allow_private_targets": *req.AllowPrivateTargets})
}
