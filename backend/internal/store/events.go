package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- edge-triggered event state -------------------------------------------------

// ActiveEventKeys returns every event_state key whose condition currently holds, so a
// sweep only has to call EventTransition for keys that actually changed.
func (s *Store) ActiveEventKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT key FROM event_state WHERE active`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// EventTransition records the current truth of condition `key` and reports edges.
// rose is true when the condition became true AND the last firing is older than
// window (so a flapping condition fires at most once per window); fell is true when a
// previously-active condition became false. A condition that stays true never fires
// again - one event per crossing.
func (s *Store) EventTransition(ctx context.Context, key string, cond bool, window time.Duration) (rose, fell bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO event_state (key) VALUES ($1) ON CONFLICT DO NOTHING`, key); err != nil {
		return false, false, err
	}
	var active bool
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT active, last_fired_at FROM event_state WHERE key = $1 FOR UPDATE`, key).Scan(&active, &last); err != nil {
		return false, false, err
	}
	switch {
	case cond && !active:
		rose = last == nil || time.Since(*last) >= window
		if rose {
			_, err = tx.Exec(ctx, `UPDATE event_state SET active = true, last_fired_at = now() WHERE key = $1`, key)
		} else {
			_, err = tx.Exec(ctx, `UPDATE event_state SET active = true WHERE key = $1`, key)
		}
	case !cond && active:
		fell = true
		_, err = tx.Exec(ctx, `UPDATE event_state SET active = false WHERE key = $1`, key)
	}
	if err != nil {
		return false, false, err
	}
	return rose, fell, tx.Commit(ctx)
}

// PruneEventState drops state rows for conditions that are inactive and long quiet
// (deleted accounts etc.) so the table cannot grow forever.
func (s *Store) PruneEventState(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM event_state WHERE NOT active AND (last_fired_at IS NULL OR last_fired_at < now() - make_interval(secs => $1))`, olderThan.Seconds())
	return err
}

// ---- sweep inputs ---------------------------------------------------------------

type NodeHealth struct {
	ID     string
	Name   string
	Status string
}

// ListNodeHealth returns nodes that have ever reported (online/offline).
func (s *Store) ListNodeHealth(ctx context.Context) ([]NodeHealth, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, status FROM nodes WHERE status IN ('online', 'offline')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeHealth
	for rows.Next() {
		var n NodeHealth
		if err := rows.Scan(&n.ID, &n.Name, &n.Status); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type AccountThresholdRow struct {
	ID             string
	Label          string
	Namespace      string // owner_key_namespace, "" for admin-created
	Status         string
	DataQuotaBytes *int64
	DataUsedBytes  int64
	ExpiryAt       *time.Time
	ExternalRef    *string
}

// ListAccountThresholdRows returns every non-deleted account that has a quota or an
// expiry, for the threshold sweep.
func (s *Store) ListAccountThresholdRows(ctx context.Context) ([]AccountThresholdRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, COALESCE(owner_key_namespace, ''), status, data_quota_bytes, data_used_bytes, expiry_at, external_ref
		FROM accounts
		WHERE status != 'deleted' AND (data_quota_bytes IS NOT NULL OR expiry_at IS NOT NULL)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountThresholdRow
	for rows.Next() {
		var a AccountThresholdRow
		if err := rows.Scan(&a.ID, &a.Label, &a.Namespace, &a.Status, &a.DataQuotaBytes, &a.DataUsedBytes, &a.ExpiryAt, &a.ExternalRef); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- in-panel notifications -----------------------------------------------------

type Notification struct {
	ID        int64
	Event     string
	Severity  string
	Namespace string
	Title     string
	Body      string
	Data      json.RawMessage
	CreatedAt time.Time
	Read      bool
}

func (s *Store) InsertNotification(ctx context.Context, n Notification) (int64, error) {
	data := n.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notifications (event, severity, namespace, title, body, data)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		n.Event, n.Severity, n.Namespace, n.Title, n.Body, data).Scan(&id)
	return id, err
}

// notificationVisible scopes notifications to a caller: $2 NULL = staff (everything),
// otherwise only that namespace (a reseller).
const notificationVisible = `($2::text IS NULL OR n.namespace = $2)`

// ListNotifications returns the newest notifications visible to the caller with the
// per-admin read flag; resellerNS nil means staff.
func (s *Store) ListNotifications(ctx context.Context, adminID string, resellerNS *string, limit int, unreadOnly bool) ([]Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.event, n.severity, n.namespace, n.title, n.body, n.data, n.created_at,
		       (n.id <= COALESCE(rs.read_up_to, 0) OR r.notification_id IS NOT NULL) AS is_read
		FROM notifications n
		LEFT JOIN notification_read_state rs ON rs.admin_id = $1
		LEFT JOIN notification_reads r ON r.admin_id = $1 AND r.notification_id = n.id
		WHERE `+notificationVisible+`
		  AND (NOT $4 OR NOT (n.id <= COALESCE(rs.read_up_to, 0) OR r.notification_id IS NOT NULL))
		ORDER BY n.id DESC LIMIT $3`, adminID, resellerNS, limit, unreadOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Event, &n.Severity, &n.Namespace, &n.Title, &n.Body, &n.Data, &n.CreatedAt, &n.Read); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CountUnreadNotifications(ctx context.Context, adminID string, resellerNS *string) (int, error) {
	var c int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM notifications n
		LEFT JOIN notification_read_state rs ON rs.admin_id = $1
		LEFT JOIN notification_reads r ON r.admin_id = $1 AND r.notification_id = n.id
		WHERE `+notificationVisible+`
		  AND n.id > COALESCE(rs.read_up_to, 0) AND r.notification_id IS NULL`, adminID, resellerNS).Scan(&c)
	return c, err
}

// MarkNotificationRead returns false when the notification does not exist or is not
// visible to the caller.
func (s *Store) MarkNotificationRead(ctx context.Context, adminID string, resellerNS *string, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO notification_reads (admin_id, notification_id)
		SELECT $1, n.id FROM notifications n WHERE n.id = $3 AND `+notificationVisible+`
		ON CONFLICT DO NOTHING`, adminID, resellerNS, id)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_reads WHERE admin_id = $1 AND notification_id = $2)`, adminID, id).Scan(&exists)
		return exists, err
	}
	return true, nil
}

func (s *Store) MarkAllNotificationsRead(ctx context.Context, adminID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notification_read_state (admin_id, read_up_to)
		VALUES ($1, COALESCE((SELECT max(id) FROM notifications), 0))
		ON CONFLICT (admin_id) DO UPDATE SET read_up_to = EXCLUDED.read_up_to`, adminID)
	return err
}

func (s *Store) PruneNotifications(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notifications WHERE created_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return err
}

var _ = pgx.ErrNoRows
