package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrEndpointNotFound   = errors.New("node endpoint not found")
	ErrEndpointLabelTaken = errors.New("an endpoint with this label already exists on the node")
)

// NodeEndpoint is one extra client entry point of a node (migration 0027).
type NodeEndpoint struct {
	ID            string
	NodeID        string
	Label         string
	Address       string
	Kind          string // direct | relay
	Priority      int    // higher wins
	Enabled       bool
	MTU           *int   // per-endpoint client MTU override
	HealthCheck   string // none | tcp
	LastStatus    string // unknown | up | down
	LastCheckedAt *time.Time
	LastLatencyMs *int
	CreatedAt     time.Time
}

const endpointColumns = `id, node_id, label, address, kind, priority, enabled, mtu, health_check, last_status, last_checked_at, last_latency_ms, created_at`

func scanEndpoint(row pgx.Row, e *NodeEndpoint) error {
	return row.Scan(&e.ID, &e.NodeID, &e.Label, &e.Address, &e.Kind, &e.Priority, &e.Enabled, &e.MTU,
		&e.HealthCheck, &e.LastStatus, &e.LastCheckedAt, &e.LastLatencyMs, &e.CreatedAt)
}

func collectEndpoints(rows pgx.Rows) ([]NodeEndpoint, error) {
	defer rows.Close()
	var out []NodeEndpoint
	for rows.Next() {
		var e NodeEndpoint
		if err := scanEndpoint(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListNodeEndpoints(ctx context.Context, nodeID string) ([]NodeEndpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+endpointColumns+` FROM node_endpoints WHERE node_id = $1 ORDER BY priority DESC, created_at, id`, nodeID)
	if err != nil {
		return nil, err
	}
	return collectEndpoints(rows)
}

func (s *Store) GetNodeEndpoint(ctx context.Context, nodeID, id string) (NodeEndpoint, error) {
	var e NodeEndpoint
	err := scanEndpoint(s.pool.QueryRow(ctx, `SELECT `+endpointColumns+` FROM node_endpoints WHERE id = $1 AND node_id = $2`, id, nodeID), &e)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeEndpoint{}, ErrEndpointNotFound
	}
	return e, err
}

type CreateNodeEndpointParams struct {
	NodeID      string
	Label       string
	Address     string
	Kind        string
	Priority    int
	Enabled     bool
	MTU         *int
	HealthCheck string
}

func (s *Store) CreateNodeEndpoint(ctx context.Context, p CreateNodeEndpointParams) (NodeEndpoint, error) {
	// Check the node first so a bad id is a clean ErrNodeNotFound rather than an FK error.
	if _, err := s.GetNode(ctx, p.NodeID); err != nil {
		return NodeEndpoint{}, err
	}
	var e NodeEndpoint
	err := scanEndpoint(s.pool.QueryRow(ctx, `
		INSERT INTO node_endpoints (node_id, label, address, kind, priority, enabled, mtu, health_check)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+endpointColumns,
		p.NodeID, p.Label, p.Address, p.Kind, p.Priority, p.Enabled, p.MTU, p.HealthCheck), &e)
	if err != nil {
		if isUniqueViolation(err) {
			return NodeEndpoint{}, ErrEndpointLabelTaken
		}
		return NodeEndpoint{}, err
	}
	return e, nil
}

// UpdateNodeEndpointParams: nil = unchanged. ClearMTU removes the override.
type UpdateNodeEndpointParams struct {
	Label       *string
	Address     *string
	Kind        *string
	Priority    *int
	Enabled     *bool
	MTU         *int
	ClearMTU    bool
	HealthCheck *string
}

func (s *Store) UpdateNodeEndpoint(ctx context.Context, nodeID, id string, p UpdateNodeEndpointParams) (NodeEndpoint, error) {
	var e NodeEndpoint
	// Changing the address or health mode invalidates the previous health verdict.
	err := scanEndpoint(s.pool.QueryRow(ctx, `
		UPDATE node_endpoints SET
			label = COALESCE($3, label),
			address = COALESCE($4, address),
			kind = COALESCE($5, kind),
			priority = COALESCE($6, priority),
			enabled = COALESCE($7, enabled),
			mtu = CASE WHEN $9 THEN NULL WHEN $8::int IS NULL THEN mtu ELSE $8 END,
			health_check = COALESCE($10, health_check),
			last_status = CASE WHEN ($4::text IS NOT NULL AND $4 <> address) OR ($10::text IS NOT NULL AND $10 <> health_check) THEN 'unknown' ELSE last_status END,
			last_checked_at = CASE WHEN ($4::text IS NOT NULL AND $4 <> address) OR ($10::text IS NOT NULL AND $10 <> health_check) THEN NULL ELSE last_checked_at END,
			last_latency_ms = CASE WHEN ($4::text IS NOT NULL AND $4 <> address) OR ($10::text IS NOT NULL AND $10 <> health_check) THEN NULL ELSE last_latency_ms END
		WHERE id = $2 AND node_id = $1 RETURNING `+endpointColumns,
		nodeID, id, p.Label, p.Address, p.Kind, p.Priority, p.Enabled, p.MTU, p.ClearMTU, p.HealthCheck), &e)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeEndpoint{}, ErrEndpointNotFound
	}
	if err != nil && isUniqueViolation(err) {
		return NodeEndpoint{}, ErrEndpointLabelTaken
	}
	return e, err
}

func (s *Store) DeleteNodeEndpoint(ctx context.Context, nodeID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM node_endpoints WHERE id = $1 AND node_id = $2`, id, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEndpointNotFound
	}
	return nil
}

// ListEndpointsToCheck returns enabled endpoints with a health check configured whose
// last check is older than minAge (or never ran) - the sweep's work list.
func (s *Store) ListEndpointsToCheck(ctx context.Context, minAge time.Duration) ([]NodeEndpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+endpointColumns+` FROM node_endpoints
		WHERE enabled AND health_check <> 'none' AND (last_checked_at IS NULL OR last_checked_at < now() - make_interval(secs => $1))
		ORDER BY last_checked_at NULLS FIRST LIMIT 200`, minAge.Seconds())
	if err != nil {
		return nil, err
	}
	return collectEndpoints(rows)
}

// RecordEndpointHealth stores a check result, but only when the endpoint still has the
// address that was checked (an edit mid-check must not be overwritten by a stale verdict).
func (s *Store) RecordEndpointHealth(ctx context.Context, id, address, status string, latencyMs *int) error {
	_, err := s.pool.Exec(ctx, `UPDATE node_endpoints SET last_status = $3, last_checked_at = now(), last_latency_ms = $4
		WHERE id = $1 AND address = $2`, id, address, status, latencyMs)
	return err
}
