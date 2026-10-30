package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AbuseSettings is the singleton configuration row of migration 0033: auto-cleanup,
// the device-limit counting mode, the sharing heuristic and torrent blocking. It lives
// in its own table (not panel_settings) so this feature set never contends with the
// panel's general settings row.
type AbuseSettings struct {
	AutoCleanupEnabled bool
	AutoCleanupDays    int

	DeviceLimitMode     string // DeviceModeEndpoint | DeviceModeIP
	DeviceWindowMinutes int

	SharingDetectionEnabled bool
	SharingMinNetworks      int
	SharingWindowMinutes    int

	TorrentBlockEnabled         bool
	TorrentSuspendEnabled       bool
	TorrentSuspendHits          int
	TorrentSuspendWindowMinutes int
}

const (
	DeviceModeEndpoint = "endpoint"
	DeviceModeIP       = "ip"
)

const abuseSettingsColumns = `auto_cleanup_enabled, auto_cleanup_days, device_limit_mode, device_window_minutes,
	sharing_detection_enabled, sharing_min_networks, sharing_window_minutes,
	torrent_block_enabled, torrent_suspend_enabled, torrent_suspend_hits, torrent_suspend_window_minutes`

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getAbuseSettings(ctx context.Context, q rowQuerier) (AbuseSettings, error) {
	var a AbuseSettings
	err := q.QueryRow(ctx, `SELECT `+abuseSettingsColumns+` FROM abuse_settings WHERE id = 1`).Scan(
		&a.AutoCleanupEnabled, &a.AutoCleanupDays, &a.DeviceLimitMode, &a.DeviceWindowMinutes,
		&a.SharingDetectionEnabled, &a.SharingMinNetworks, &a.SharingWindowMinutes,
		&a.TorrentBlockEnabled, &a.TorrentSuspendEnabled, &a.TorrentSuspendHits, &a.TorrentSuspendWindowMinutes,
	)
	return a, err
}

// GetAbuseSettings always finds the row (seeded by the migration).
func (s *Store) GetAbuseSettings(ctx context.Context) (AbuseSettings, error) {
	return getAbuseSettings(ctx, s.pool)
}

// UpdateAbuseSettingsParams: a nil field is left unchanged (the PATCH convention used
// by every other settings endpoint).
type UpdateAbuseSettingsParams struct {
	AutoCleanupEnabled          *bool
	AutoCleanupDays             *int
	DeviceLimitMode             *string
	DeviceWindowMinutes         *int
	SharingDetectionEnabled     *bool
	SharingMinNetworks          *int
	SharingWindowMinutes        *int
	TorrentBlockEnabled         *bool
	TorrentSuspendEnabled       *bool
	TorrentSuspendHits          *int
	TorrentSuspendWindowMinutes *int
}

func (s *Store) UpdateAbuseSettings(ctx context.Context, p UpdateAbuseSettingsParams) (AbuseSettings, error) {
	_, err := s.pool.Exec(ctx, `
		UPDATE abuse_settings SET
			auto_cleanup_enabled = COALESCE($1, auto_cleanup_enabled),
			auto_cleanup_days = COALESCE($2, auto_cleanup_days),
			device_limit_mode = COALESCE($3, device_limit_mode),
			device_window_minutes = COALESCE($4, device_window_minutes),
			sharing_detection_enabled = COALESCE($5, sharing_detection_enabled),
			sharing_min_networks = COALESCE($6, sharing_min_networks),
			sharing_window_minutes = COALESCE($7, sharing_window_minutes),
			torrent_block_enabled = COALESCE($8, torrent_block_enabled),
			torrent_suspend_enabled = COALESCE($9, torrent_suspend_enabled),
			torrent_suspend_hits = COALESCE($10, torrent_suspend_hits),
			torrent_suspend_window_minutes = COALESCE($11, torrent_suspend_window_minutes),
			updated_at = now()
		WHERE id = 1
	`, p.AutoCleanupEnabled, p.AutoCleanupDays, p.DeviceLimitMode, p.DeviceWindowMinutes,
		p.SharingDetectionEnabled, p.SharingMinNetworks, p.SharingWindowMinutes,
		p.TorrentBlockEnabled, p.TorrentSuspendEnabled, p.TorrentSuspendHits, p.TorrentSuspendWindowMinutes)
	if err != nil {
		return AbuseSettings{}, err
	}
	return s.GetAbuseSettings(ctx)
}

// NodeTorrentOverride is one node's torrent-block setting: Override nil = inherit the
// global default, otherwise the node's own on/off.
type NodeTorrentOverride struct {
	NodeID   string
	NodeName string
	Override *bool
	// Effective is what the node is told on its next heartbeat.
	Effective bool
}

// ListNodeTorrentOverrides returns every node with its override and effective value.
func (s *Store) ListNodeTorrentOverrides(ctx context.Context) ([]NodeTorrentOverride, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.name, o.torrent_block,
		       COALESCE(o.torrent_block, (SELECT torrent_block_enabled FROM abuse_settings WHERE id = 1))
		FROM nodes n
		LEFT JOIN node_abuse_overrides o ON o.node_id = n.id
		ORDER BY n.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeTorrentOverride
	for rows.Next() {
		var n NodeTorrentOverride
		if err := rows.Scan(&n.NodeID, &n.NodeName, &n.Override, &n.Effective); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNodeTorrentOverride sets (non-nil) or clears (nil = inherit) a node's override.
func (s *Store) SetNodeTorrentOverride(ctx context.Context, nodeID string, v *bool) error {
	if v == nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM node_abuse_overrides WHERE node_id = $1`, nodeID)
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO node_abuse_overrides (node_id, torrent_block)
		SELECT id, $2 FROM nodes WHERE id = $1
		ON CONFLICT (node_id) DO UPDATE SET torrent_block = EXCLUDED.torrent_block
	`, nodeID, *v)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// EffectiveTorrentBlock is the value a node's heartbeat response carries.
func (s *Store) EffectiveTorrentBlock(ctx context.Context, nodeID string) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(o.torrent_block, a.torrent_block_enabled)
		FROM abuse_settings a LEFT JOIN node_abuse_overrides o ON o.node_id = $1
		WHERE a.id = 1
	`, nodeID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// FireDebounced atomically claims the right to emit the event identified by key: true
// when nothing fired for key within window (and records now as the firing). The
// event_state table is the same one the threshold sweep uses; the "abuse:" key prefix
// keeps these rows apart; active stays false so PruneEventState reaps them once quiet. Atomic in one statement so two concurrent heartbeats cannot
// both emit.
func (s *Store) FireDebounced(ctx context.Context, key string, window time.Duration) (bool, error) {
	var fired bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO event_state (key, active, last_fired_at) VALUES ($1, false, now())
		ON CONFLICT (key) DO UPDATE SET last_fired_at = now()
			WHERE event_state.last_fired_at IS NULL OR event_state.last_fired_at < now() - make_interval(secs => $2)
		RETURNING true
	`, key, window.Seconds()).Scan(&fired)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return fired, err
}
