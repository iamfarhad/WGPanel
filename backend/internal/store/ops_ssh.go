package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// SSHJob is the persisted record of an SSH provisioning run: never any credentials,
// and a redacted, size-capped log.
type SSHJob struct {
	ID           string
	NodeID       *string
	Host         string
	Port         int
	Username     string
	Fingerprint  string
	Status       string // running | succeeded | failed | cancelled
	Phase        string
	ErrorCode    string
	ErrorMessage string
	Log          string
	CreatedBy    string
	CreatedAt    time.Time
	FinishedAt   *time.Time
}

var ErrSSHJobNotFound = errors.New("ssh job not found")

func (s *Store) InsertSSHJob(ctx context.Context, j SSHJob) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ssh_provision_jobs (id, node_id, host, port, username, fingerprint, status, phase, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, j.ID, j.NodeID, j.Host, j.Port, j.Username, j.Fingerprint, j.Status, j.Phase, j.CreatedBy)
	return err
}

func (s *Store) FinishSSHJob(ctx context.Context, id, status, phase, errCode, errMsg, log string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ssh_provision_jobs SET status = $2, phase = $3, error_code = $4, error_message = $5, log = $6, finished_at = now()
		WHERE id = $1
	`, id, status, phase, errCode, errMsg, log)
	return err
}

func (s *Store) GetSSHJob(ctx context.Context, id string) (SSHJob, error) {
	var j SSHJob
	err := s.pool.QueryRow(ctx, `
		SELECT id, node_id::text, host, port, username, fingerprint, status, phase, error_code, error_message, log, created_by, created_at, finished_at
		FROM ssh_provision_jobs WHERE id = $1
	`, id).Scan(&j.ID, &j.NodeID, &j.Host, &j.Port, &j.Username, &j.Fingerprint, &j.Status, &j.Phase, &j.ErrorCode, &j.ErrorMessage, &j.Log, &j.CreatedBy, &j.CreatedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SSHJob{}, ErrSSHJobNotFound
	}
	return j, err
}
