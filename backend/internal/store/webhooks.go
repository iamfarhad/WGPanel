package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrWebhookNotFound  = errors.New("webhook not found")
	ErrDeliveryNotFound = errors.New("webhook delivery not found")
)

type Webhook struct {
	ID              string
	Namespace       *string // nil = global (staff-managed)
	URL             string
	Description     string
	SecretEncrypted string
	Events          []string // empty = every event
	Enabled         bool
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// webhookScope: $1 is the caller namespace, NULL for staff (sees every webhook).
const webhookScope = `($1::text IS NULL OR namespace = $1)`

const webhookColumns = `id, namespace, url, description, secret_encrypted, events, enabled, created_by, created_at, updated_at`

func scanWebhook(row pgx.Row, w *Webhook) error {
	return row.Scan(&w.ID, &w.Namespace, &w.URL, &w.Description, &w.SecretEncrypted, &w.Events, &w.Enabled, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt)
}

func (s *Store) CreateWebhook(ctx context.Context, w Webhook) (Webhook, error) {
	var out Webhook
	err := scanWebhook(s.pool.QueryRow(ctx, `
		INSERT INTO webhooks (namespace, url, description, secret_encrypted, events, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+webhookColumns,
		w.Namespace, w.URL, w.Description, w.SecretEncrypted, nonNilStrings(w.Events), w.Enabled, w.CreatedBy), &out)
	return out, err
}

func (s *Store) ListWebhooks(ctx context.Context, callerNS *string) ([]Webhook, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE `+webhookScope+` ORDER BY created_at, id`, callerNS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		if err := scanWebhook(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) GetWebhook(ctx context.Context, callerNS *string, id string) (Webhook, error) {
	var w Webhook
	err := scanWebhook(s.pool.QueryRow(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE id::text = $2 AND `+webhookScope, callerNS, id), &w)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrWebhookNotFound
	}
	return w, err
}

// UpdateWebhook overwrites url/description/events/enabled and (when secretEnc != "")
// the secret.
func (s *Store) UpdateWebhook(ctx context.Context, callerNS *string, w Webhook, secretEnc string) (Webhook, error) {
	var out Webhook
	err := scanWebhook(s.pool.QueryRow(ctx, `
		UPDATE webhooks SET url = $3, description = $4, events = $5, enabled = $6,
			secret_encrypted = CASE WHEN $7 = '' THEN secret_encrypted ELSE $7 END, updated_at = now()
		WHERE id::text = $2 AND `+webhookScope+` RETURNING `+webhookColumns,
		callerNS, w.ID, w.URL, w.Description, nonNilStrings(w.Events), w.Enabled, secretEnc), &out)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrWebhookNotFound
	}
	return out, err
}

func (s *Store) DeleteWebhook(ctx context.Context, callerNS *string, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhooks WHERE id::text = $2 AND `+webhookScope, callerNS, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// WebhooksForEvent returns enabled webhooks that should receive an event: global
// webhooks get every namespace, a namespaced webhook only its own.
func (s *Store) WebhooksForEvent(ctx context.Context, event, namespace string) ([]Webhook, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+webhookColumns+` FROM webhooks
		WHERE enabled AND (namespace IS NULL OR namespace = $2)
		  AND (cardinality(events) = 0 OR $1 = ANY(events))`, event, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		if err := scanWebhook(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---- deliveries / queue ---------------------------------------------------------

type WebhookDelivery struct {
	ID             string
	WebhookID      string
	Event          string
	EventID        string
	Payload        string
	Status         string // pending | retrying | success | failed
	Attempts       int
	NextAttemptAt  *time.Time
	ResponseStatus *int
	LatencyMS      *int
	ResponseBody   string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const deliveryColumns = `id, webhook_id, event, event_id, payload, status, attempts, next_attempt_at, response_status, latency_ms, response_body, error, created_at, updated_at`

func scanDelivery(row pgx.Row, d *WebhookDelivery) error {
	return row.Scan(&d.ID, &d.WebhookID, &d.Event, &d.EventID, &d.Payload, &d.Status, &d.Attempts, &d.NextAttemptAt,
		&d.ResponseStatus, &d.LatencyMS, &d.ResponseBody, &d.Error, &d.CreatedAt, &d.UpdatedAt)
}

// EnqueueDelivery adds a pending delivery that is due immediately.
func (s *Store) EnqueueDelivery(ctx context.Context, webhookID, event, eventID, payload string) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := scanDelivery(s.pool.QueryRow(ctx, `
		INSERT INTO webhook_deliveries (webhook_id, event, event_id, payload, next_attempt_at)
		VALUES ($1, $2, $3, $4, now()) RETURNING `+deliveryColumns, webhookID, event, eventID, payload), &d)
	return d, err
}

// ClaimDueDeliveries atomically takes up to limit due deliveries and pushes their
// next_attempt_at out by lease, so a crashed worker's claims become due again and two
// workers never take the same row.
func (s *Store) ClaimDueDeliveries(ctx context.Context, limit int, lease time.Duration) ([]WebhookDelivery, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE webhook_deliveries SET next_attempt_at = now() + make_interval(secs => $2)
		WHERE id IN (
			SELECT id FROM webhook_deliveries
			WHERE status IN ('pending', 'retrying') AND next_attempt_at <= now()
			ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+deliveryColumns, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := scanDelivery(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryAttempt is the outcome of one HTTP attempt.
type DeliveryAttempt struct {
	ResponseStatus *int
	LatencyMS      int
	ResponseBody   string
	Error          string
	Success        bool
	// RetryIn > 0 schedules another attempt; zero with !Success marks it failed.
	RetryIn time.Duration
}

func (s *Store) RecordDeliveryAttempt(ctx context.Context, id string, a DeliveryAttempt) error {
	status := "success"
	var next *time.Time
	if !a.Success {
		status = "failed"
		if a.RetryIn > 0 {
			status = "retrying"
			t := time.Now().Add(a.RetryIn)
			next = &t
		}
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries SET attempts = attempts + 1, status = $2, next_attempt_at = $3,
			response_status = $4, latency_ms = $5, response_body = $6, error = $7, updated_at = now()
		WHERE id::text = $1`, id, status, next, a.ResponseStatus, a.LatencyMS, a.ResponseBody, a.Error)
	return err
}

func (s *Store) GetDelivery(ctx context.Context, callerNS *string, webhookID, id string) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := scanDelivery(s.pool.QueryRow(ctx, `
		SELECT `+deliveryColumns+` FROM webhook_deliveries d
		WHERE d.id::text = $3 AND d.webhook_id::text = $2
		  AND EXISTS (SELECT 1 FROM webhooks w WHERE w.id = d.webhook_id AND (`+`$1::text IS NULL OR w.namespace = $1))`,
		callerNS, webhookID, id), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrDeliveryNotFound
	}
	return d, err
}

func (s *Store) GetDeliveryByID(ctx context.Context, id string) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := scanDelivery(s.pool.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE id::text = $1`, id), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrDeliveryNotFound
	}
	return d, err
}

// GetWebhookByID is the unscoped lookup the delivery worker uses.
func (s *Store) GetWebhookByID(ctx context.Context, id string) (Webhook, error) {
	return s.GetWebhook(ctx, nil, id)
}

func (s *Store) ListDeliveries(ctx context.Context, callerNS *string, webhookID string, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryColumns+` FROM webhook_deliveries d
		WHERE d.webhook_id::text = $2
		  AND EXISTS (SELECT 1 FROM webhooks w WHERE w.id = d.webhook_id AND ($1::text IS NULL OR w.namespace = $1))
		ORDER BY d.created_at DESC, d.id LIMIT $3`, callerNS, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := scanDelivery(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) PruneDeliveries(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE status IN ('success', 'failed') AND created_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return err
}

// ---- settings -------------------------------------------------------------------

func (s *Store) AllowPrivateWebhookTargets(ctx context.Context) (bool, error) {
	var v bool
	err := s.pool.QueryRow(ctx, `SELECT allow_private_webhook_targets FROM notify_settings WHERE id = 1`).Scan(&v)
	return v, err
}

func (s *Store) SetAllowPrivateWebhookTargets(ctx context.Context, v bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE notify_settings SET allow_private_webhook_targets = $1 WHERE id = 1`, v)
	return err
}

// PushDeliveryLease postpones a delivery's due time (used when it is attempted inline).
func (s *Store) PushDeliveryLease(ctx context.Context, id string, lease time.Duration) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhook_deliveries SET next_attempt_at = now() + make_interval(secs => $2) WHERE id::text = $1`, id, lease.Seconds())
	return err
}
