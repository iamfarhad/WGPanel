package store

import (
	"context"
	"time"
)

// BackupSchedule is the singleton scheduled-backup configuration. *Encrypted fields
// hold wgkeys ciphertext; callers never expose them.
type BackupSchedule struct {
	Enabled                bool
	IntervalHours          int
	RetentionCount         int
	PasswordEncrypted      string
	TelegramEnabled        bool
	TelegramTokenEncrypted string
	TelegramChatID         string
	S3Enabled              bool
	S3Endpoint             string
	S3Region               string
	S3Bucket               string
	S3Prefix               string
	S3AccessKey            string
	S3SecretEncrypted      string
	S3PathStyle            bool
	LastRunAt              *time.Time
}

const backupScheduleColumns = `enabled, interval_hours, retention_count, password_encrypted, telegram_enabled, telegram_token_encrypted, telegram_chat_id,
	s3_enabled, s3_endpoint, s3_region, s3_bucket, s3_prefix, s3_access_key, s3_secret_encrypted, s3_path_style, last_run_at`

func (s *Store) GetBackupSchedule(ctx context.Context) (BackupSchedule, error) {
	var b BackupSchedule
	err := s.pool.QueryRow(ctx, `SELECT `+backupScheduleColumns+` FROM backup_schedule WHERE id = 1`).Scan(
		&b.Enabled, &b.IntervalHours, &b.RetentionCount, &b.PasswordEncrypted, &b.TelegramEnabled, &b.TelegramTokenEncrypted, &b.TelegramChatID,
		&b.S3Enabled, &b.S3Endpoint, &b.S3Region, &b.S3Bucket, &b.S3Prefix, &b.S3AccessKey, &b.S3SecretEncrypted, &b.S3PathStyle, &b.LastRunAt)
	return b, err
}

// SaveBackupSchedule overwrites every configuration column (not last_run_at).
func (s *Store) SaveBackupSchedule(ctx context.Context, b BackupSchedule) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE backup_schedule SET enabled = $1, interval_hours = $2, retention_count = $3, password_encrypted = $4,
			telegram_enabled = $5, telegram_token_encrypted = $6, telegram_chat_id = $7,
			s3_enabled = $8, s3_endpoint = $9, s3_region = $10, s3_bucket = $11, s3_prefix = $12,
			s3_access_key = $13, s3_secret_encrypted = $14, s3_path_style = $15
		WHERE id = 1`,
		b.Enabled, b.IntervalHours, b.RetentionCount, b.PasswordEncrypted, b.TelegramEnabled, b.TelegramTokenEncrypted, b.TelegramChatID,
		b.S3Enabled, b.S3Endpoint, b.S3Region, b.S3Bucket, b.S3Prefix, b.S3AccessKey, b.S3SecretEncrypted, b.S3PathStyle)
	return err
}

// ClaimScheduledBackup atomically marks the schedule as run-now when it is enabled and
// due, so only one API instance performs a given run. Returns true when claimed.
func (s *Store) ClaimScheduledBackup(ctx context.Context) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE backup_schedule SET last_run_at = now()
		WHERE id = 1 AND enabled AND (last_run_at IS NULL OR last_run_at <= now() - make_interval(hours => interval_hours))`)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) TouchBackupRun(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE backup_schedule SET last_run_at = now() WHERE id = 1`)
	return err
}

type BackupRun struct {
	ID          int64
	RunID       string
	Destination string // telegram | s3
	Trigger     string // schedule | manual
	Status      string // success | failed
	Error       string
	Filename    string
	SizeBytes   int64
	CreatedAt   time.Time
}

func (s *Store) InsertBackupRun(ctx context.Context, r BackupRun) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backup_runs (run_id, destination, trigger, status, error, filename, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		r.RunID, r.Destination, r.Trigger, r.Status, r.Error, r.Filename, r.SizeBytes)
	return err
}

// ListBackupRuns returns the newest runs, newest first.
func (s *Store) ListBackupRuns(ctx context.Context, limit int) ([]BackupRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id::text, destination, trigger, status, error, filename, size_bytes, created_at
		FROM backup_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupRun
	for rows.Next() {
		var r BackupRun
		if err := rows.Scan(&r.ID, &r.RunID, &r.Destination, &r.Trigger, &r.Status, &r.Error, &r.Filename, &r.SizeBytes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastBackupRunPerDestination returns the newest run of each destination.
func (s *Store) LastBackupRunPerDestination(ctx context.Context) (map[string]BackupRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (destination) id, run_id::text, destination, trigger, status, error, filename, size_bytes, created_at
		FROM backup_runs ORDER BY destination, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BackupRun{}
	for rows.Next() {
		var r BackupRun
		if err := rows.Scan(&r.ID, &r.RunID, &r.Destination, &r.Trigger, &r.Status, &r.Error, &r.Filename, &r.SizeBytes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.Destination] = r
	}
	return out, rows.Err()
}
