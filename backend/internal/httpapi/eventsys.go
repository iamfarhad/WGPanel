package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/webhook"
)

const (
	// eventDedupeWindow is the PRD's 15-minute suppression window: a condition that
	// re-crosses its threshold sooner than this after the last firing is not re-announced.
	eventDedupeWindow = 15 * time.Minute

	thresholdSweepEvery = 60 * time.Second
	webhookPollEvery    = 2 * time.Second
	webhookLease        = 2 * time.Minute
	webhookTimeout      = 10 * time.Second
	webhookBodyKeep     = 1024
	dispatcherWorkers   = 4
)

// emit publishes a domain event; never blocks and never fails the caller.
func (s *Server) emit(typ, namespace string, data map[string]any) {
	s.Events.Emit(typ, namespace, data)
}

// RunNotificationWorkers starts every background worker of the events system and
// blocks until ctx is cancelled: the event dispatcher, the webhook delivery loop, the
// threshold sweep (node/account/reseller conditions) and the scheduled-backup check.
// cmd/api/main.go starts it as a goroutine.
func (s *Server) RunNotificationWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	run := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	if s.Events != nil {
		for i := 0; i < dispatcherWorkers; i++ {
			run(s.runDispatcher)
		}
	}
	run(s.runWebhookWorker)
	run(s.runThresholdSweepLoop)
	wg.Wait()
}

func (s *Server) runDispatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.Events.C():
			s.dispatchEvent(ctx, ev)
		}
	}
}

// dispatchEvent turns one event into an in-panel notification, channel messages and
// webhook deliveries. Failures are logged, never propagated - delivery is best effort
// per sink and one broken sink must not affect the others.
func (s *Server) dispatchEvent(ctx context.Context, ev events.Event) {
	meta, ok := events.Lookup(ev.Type)
	if !ok {
		return
	}
	if meta.InPanel {
		r := renderEvent(ev, "en")
		data, _ := json.Marshal(ev.Data)
		if _, err := s.Store.InsertNotification(ctx, store.Notification{
			Event: ev.Type, Severity: meta.Severity, Namespace: ev.Namespace, Title: r.Title, Body: r.Body, Data: data,
		}); err != nil {
			s.Logger.Error("insert_notification_failed", "error", err)
		}
	}
	s.deliverToChannels(ctx, ev, meta.Severity)
	s.enqueueWebhooks(ctx, ev)
}

type webhookPayload struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	Namespace string         `json:"namespace"`
	Data      map[string]any `json:"data"`
}

func marshalPayload(ev events.Event) string {
	data := ev.Data
	if data == nil {
		data = map[string]any{}
	}
	b, _ := json.Marshal(webhookPayload{ID: ev.ID, Type: ev.Type, CreatedAt: ev.CreatedAt.Format(time.RFC3339), Namespace: ev.Namespace, Data: data})
	return string(b)
}

func (s *Server) enqueueWebhooks(ctx context.Context, ev events.Event) {
	hooks, err := s.Store.WebhooksForEvent(ctx, ev.Type, ev.Namespace)
	if err != nil {
		s.Logger.Error("webhooks_for_event_failed", "error", err)
		return
	}
	if len(hooks) == 0 {
		return
	}
	payload := marshalPayload(ev)
	for _, h := range hooks {
		if _, err := s.Store.EnqueueDelivery(ctx, h.ID, ev.Type, ev.ID, payload); err != nil {
			s.Logger.Error("enqueue_delivery_failed", "error", err)
		}
	}
}

// ---- delivery worker ------------------------------------------------------------

func (s *Server) allowPrivateWebhooks(ctx context.Context) bool {
	rt := &s.NotifyRT
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if time.Since(rt.allowPrivateAt) < 5*time.Second {
		return rt.allowPrivateValue
	}
	v, err := s.Store.AllowPrivateWebhookTargets(ctx)
	if err != nil {
		return rt.allowPrivateValue
	}
	rt.allowPrivateValue, rt.allowPrivateAt = v, time.Now()
	return v
}

// webhookClient picks the guarded client: the staff override only ever applies to
// staff-owned (global) endpoints, never to reseller-owned ones.
func (s *Server) webhookClient(global bool) *http.Client {
	rt := &s.NotifyRT
	if global && rt.StaffWebhookHTTP != nil {
		return rt.StaffWebhookHTTP
	}
	if !global && rt.ResellerWebhookHTTP != nil {
		return rt.ResellerWebhookHTTP
	}
	rt.once.Do(func() {
		rt.staff = webhook.NewClient(webhookTimeout, func() bool { return s.allowPrivateWebhooks(context.Background()) })
		rt.reseller = webhook.NewClient(webhookTimeout, func() bool { return false })
	})
	if global {
		return rt.staff
	}
	return rt.reseller
}

func (s *Server) runWebhookWorker(ctx context.Context) {
	ticker := time.NewTicker(webhookPollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueDeliveries(ctx)
		}
	}
}

func (s *Server) processDueDeliveries(ctx context.Context) {
	due, err := s.Store.ClaimDueDeliveries(ctx, 20, webhookLease)
	if err != nil {
		s.Logger.Error("claim_deliveries_failed", "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Add(1)
		go func(d store.WebhookDelivery) {
			defer wg.Done()
			s.attemptDelivery(ctx, d)
		}(d)
	}
	wg.Wait()
}

// attemptDelivery makes one HTTP attempt for d and records the outcome, scheduling a
// retry with exponential backoff or giving up. It returns the updated row.
func (s *Server) attemptDelivery(ctx context.Context, d store.WebhookDelivery) store.WebhookDelivery {
	hook, err := s.Store.GetWebhookByID(ctx, d.WebhookID)
	if err != nil || (!hook.Enabled && d.Event != events.Ping) {
		msg := "webhook is disabled or was deleted"
		_ = s.Store.RecordDeliveryAttempt(ctx, d.ID, store.DeliveryAttempt{Error: msg})
		out, _ := s.Store.GetDeliveryByID(ctx, d.ID)
		return out
	}
	att := s.postWebhook(ctx, hook, d)
	if !att.Success && !att.terminal {
		if wait, ok := webhook.NextBackoff(d.Attempts + 1); ok {
			att.RetryIn = wait
		}
	}
	if err := s.Store.RecordDeliveryAttempt(ctx, d.ID, att.DeliveryAttempt); err != nil {
		s.Logger.Error("record_delivery_failed", "error", err)
	}
	out, _ := s.Store.GetDeliveryByID(ctx, d.ID)
	return out
}

type attemptResult struct {
	store.DeliveryAttempt
	terminal bool // do not retry (blocked target, undecryptable secret)
}

func (s *Server) postWebhook(ctx context.Context, hook store.Webhook, d store.WebhookDelivery) attemptResult {
	secret, err := s.decryptSecret(hook.SecretEncrypted)
	if err != nil {
		return attemptResult{DeliveryAttempt: store.DeliveryAttempt{Error: "webhook secret could not be decrypted"}, terminal: true}
	}
	now := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader([]byte(d.Payload)))
	if err != nil {
		return attemptResult{DeliveryAttempt: store.DeliveryAttempt{Error: "invalid url"}, terminal: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Jirjirak-Webhook/1")
	req.Header.Set("X-Jirjirak-Event", d.Event)
	req.Header.Set("X-Jirjirak-Delivery", d.ID)
	req.Header.Set(webhook.SignatureHeader, webhook.Sign(secret, now, []byte(d.Payload)))

	start := time.Now()
	resp, err := s.webhookClient(hook.Namespace == nil).Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return attemptResult{
			DeliveryAttempt: store.DeliveryAttempt{LatencyMS: latency, Error: scrubSecret(err, "").Error()},
			terminal:        errors.Is(err, webhook.ErrBlockedTarget),
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webhookBodyKeep))
	code := resp.StatusCode
	att := store.DeliveryAttempt{ResponseStatus: &code, LatencyMS: latency, ResponseBody: string(body)}
	if code/100 == 2 {
		att.Success = true
	} else {
		att.Error = "endpoint answered HTTP " + http.StatusText(code)
	}
	return attemptResult{DeliveryAttempt: att}
}

// emitAccountEvent publishes an account lifecycle event under the account's owner
// namespace (so resellers only ever see their own accounts' events).
func (s *Server) emitAccountEvent(typ string, a store.Account) {
	ns := ""
	if a.OwnerKeyNamespace != nil {
		ns = *a.OwnerKeyNamespace
	}
	s.emit(typ, ns, map[string]any{"account_id": a.ID, "label": a.Label, "status": a.Status})
}
