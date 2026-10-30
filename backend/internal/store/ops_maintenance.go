package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNodeInMaintenance is returned when an account is pinned to a node that is in
// maintenance. Automatic placement just skips such nodes.
var ErrNodeInMaintenance = errors.New("node is in maintenance")

// SetNodeMaintenance switches maintenance mode and reports whether the state actually
// changed (so repeated requests neither re-emit events nor spam the audit log). The
// timestamp is kept from the first switch-on, so "since when" stays truthful.
func (s *Store) SetNodeMaintenance(ctx context.Context, nodeID string, on bool) (name string, changed bool, since *time.Time, err error) {
	var wasOn bool
	err = s.pool.QueryRow(ctx, `
		WITH old AS (SELECT id, name, maintenance_since FROM nodes WHERE id = $1 FOR UPDATE)
		UPDATE nodes n SET maintenance_since = CASE WHEN $2::bool THEN COALESCE(old.maintenance_since, now()) ELSE NULL END
		FROM old WHERE n.id = old.id
		RETURNING n.name, old.maintenance_since IS NOT NULL, n.maintenance_since
	`, nodeID, on).Scan(&name, &wasOn, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil, ErrNodeNotFound
	}
	if err != nil {
		return "", false, nil, err
	}
	return name, wasOn != on, since, nil
}

// NodeMaintenanceSince returns node id -> maintenance start for nodes in maintenance.
// Kept out of the Node struct/nodeColumns so that hot path stays untouched.
func (s *Store) NodeMaintenanceSince(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, maintenance_since FROM nodes WHERE maintenance_since IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}
