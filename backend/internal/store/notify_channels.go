package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrChannelNotFound = errors.New("notification channel not found")

// ChannelBrokenAfter consecutive delivery failures flags a channel as broken.
const ChannelBrokenAfter = 3

type NotificationChannel struct {
	ID                  string
	Kind                string // telegram | email
	Name                string
	Enabled             bool
	Config              json.RawMessage
	SecretEncrypted     string
	Events              []string
	Lang                string
	ConsecutiveFailures int
	LastError           string
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (c NotificationChannel) Broken() bool { return c.ConsecutiveFailures >= ChannelBrokenAfter }

const channelColumns = `id, kind, name, enabled, config, secret_encrypted, events, lang, consecutive_failures, last_error, last_success_at, last_failure_at, created_at, updated_at`

func scanChannel(row pgx.Row, c *NotificationChannel) error {
	return row.Scan(&c.ID, &c.Kind, &c.Name, &c.Enabled, &c.Config, &c.SecretEncrypted, &c.Events, &c.Lang,
		&c.ConsecutiveFailures, &c.LastError, &c.LastSuccessAt, &c.LastFailureAt, &c.CreatedAt, &c.UpdatedAt)
}

func (s *Store) ListChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+` FROM notification_channels ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		if err := scanChannel(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, id string) (NotificationChannel, error) {
	var c NotificationChannel
	err := scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE id::text = $1`, id), &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrChannelNotFound
	}
	return c, err
}

func (s *Store) CreateChannel(ctx context.Context, c NotificationChannel) (NotificationChannel, error) {
	var out NotificationChannel
	err := scanChannel(s.pool.QueryRow(ctx, `
		INSERT INTO notification_channels (kind, name, enabled, config, secret_encrypted, events, lang)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+channelColumns,
		c.Kind, c.Name, c.Enabled, c.Config, c.SecretEncrypted, nonNilStrings(c.Events), c.Lang), &out)
	return out, err
}

// UpdateChannel overwrites every editable column and clears the failure streak - an
// admin editing a channel is the repair action for a broken one.
func (s *Store) UpdateChannel(ctx context.Context, c NotificationChannel) (NotificationChannel, error) {
	var out NotificationChannel
	err := scanChannel(s.pool.QueryRow(ctx, `
		UPDATE notification_channels SET name = $2, enabled = $3, config = $4, secret_encrypted = $5,
			events = $6, lang = $7, consecutive_failures = 0, last_error = '', updated_at = now()
		WHERE id::text = $1 RETURNING `+channelColumns,
		c.ID, c.Name, c.Enabled, c.Config, c.SecretEncrypted, nonNilStrings(c.Events), c.Lang), &out)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrChannelNotFound
	}
	return out, err
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrChannelNotFound
	}
	return nil
}

// RecordChannelResult updates the failure streak after a delivery attempt.
func (s *Store) RecordChannelResult(ctx context.Context, id string, sendErr error) error {
	if sendErr == nil {
		_, err := s.pool.Exec(ctx, `UPDATE notification_channels SET consecutive_failures = 0, last_error = '', last_success_at = now() WHERE id::text = $1`, id)
		return err
	}
	msg := sendErr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err := s.pool.Exec(ctx, `UPDATE notification_channels SET consecutive_failures = consecutive_failures + 1, last_error = $2, last_failure_at = now() WHERE id::text = $1`, id, msg)
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
