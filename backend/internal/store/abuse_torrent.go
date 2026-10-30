package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Torrent-signature hits reported by node agents (cmd/agent/torrent.go) and the optional
// auto-suspend they can trigger. A "hit" is one forwarded packet from a client's tunnel
// address that matched a BitTorrent signature - not one attempt: a blocked TCP handshake
// is retransmitted, and a DHT-enabled client sends many UDP queries. The suspend
// threshold is therefore expressed in hits and documented as packets.

// torrentEventDebounce stops a client that keeps retrying from emitting
// account.torrent_detected on every heartbeat.
const torrentEventDebounce = 30 * time.Minute

// TorrentHitReport is one tunnel IP's new hits since the agent's last acknowledged
// heartbeat.
type TorrentHitReport struct {
	IP   string
	Hits int64
}

// TorrentEvent describes an account that showed torrent traffic in an ingest, for the
// audit row and the event the caller emits.
type TorrentEvent struct {
	AccountID   string
	Label       string
	ExternalRef *string
	Namespace   string
	NodeName    string
	Hits        int64 // hits in this report
	WindowHits  int64 // hits counted toward the suspend threshold (0 when auto-suspend is off)
	Suspended   bool  // true when this ingest suspended the account
	// Announce is false when the debounce suppressed the event (the audit row and the
	// suspension still happen).
	Announce bool
}

// IngestTorrentHits attributes a node's per-tunnel-IP hit deltas to accounts (via the
// node's account_peers), records them, and - when torrent auto-suspend is on - suspends
// active accounts whose un-acted hits within the window reach the threshold.
//
// One transaction: the hit rows and the suspension commit together. Hits for an IP that
// maps to no live account (a peer removed meanwhile) are dropped.
func (s *Store) IngestTorrentHits(ctx context.Context, nodeID string, reports []TorrentHitReport) ([]TorrentEvent, error) {
	if len(reports) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	cfg, err := getAbuseSettings(ctx, tx)
	if err != nil {
		return nil, err
	}

	ips := make([]string, 0, len(reports))
	for _, r := range reports {
		if r.Hits > 0 {
			ips = append(ips, r.IP)
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT ap.assigned_ip, a.id, a.label, a.external_ref, COALESCE(a.owner_key_namespace, ''), n.name
		FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id AND a.status != 'deleted'
		JOIN nodes n ON n.id = ap.node_id
		WHERE ap.node_id = $1 AND ap.assigned_ip = ANY($2::text[])
	`, nodeID, ips)
	if err != nil {
		return nil, err
	}
	byIP := map[string]TorrentEvent{}
	for rows.Next() {
		var ip string
		var e TorrentEvent
		if err := rows.Scan(&ip, &e.AccountID, &e.Label, &e.ExternalRef, &e.Namespace, &e.NodeName); err != nil {
			rows.Close()
			return nil, err
		}
		byIP[ip] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Several tunnel IPs never map to one account on a single node (UNIQUE(account,node)),
	// but sum defensively anyway.
	perAccount := map[string]*TorrentEvent{}
	var order []string
	for _, r := range reports {
		e, ok := byIP[r.IP]
		if !ok || r.Hits <= 0 {
			continue
		}
		if cur, seen := perAccount[e.AccountID]; seen {
			cur.Hits += r.Hits
			continue
		}
		e.Hits = r.Hits
		perAccount[e.AccountID] = &e
		order = append(order, e.AccountID)
	}

	var events []TorrentEvent
	for _, id := range order {
		e := perAccount[id]
		if _, err := tx.Exec(ctx,
			`INSERT INTO account_torrent_hits (account_id, node_id, hits) VALUES ($1, $2, $3)`,
			id, nodeID, int32(min(e.Hits, 1<<30))); err != nil {
			return nil, err
		}
		if cfg.TorrentSuspendEnabled {
			if err := applyTorrentThresholdTx(ctx, tx, cfg, e); err != nil {
				return nil, err
			}
		}
		events = append(events, *e)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Debounce outside the transaction: it only gates the announcement.
	for i := range events {
		fire, err := s.FireDebounced(ctx, "abuse:torrent:"+events[i].AccountID, torrentEventDebounce)
		if err != nil {
			return events, err
		}
		// A suspension is always announced - it is a state change the seller must see.
		events[i].Announce = fire || events[i].Suspended
	}
	return events, nil
}

// applyTorrentThresholdTx suspends the account (reason 'torrent_abuse') when its
// un-acted hits inside the window reach the threshold, and marks those hits acted so a
// later manual enable starts from zero. Only 'active' accounts are suspended; a
// manually suspended account keeps its reason.
func applyTorrentThresholdTx(ctx context.Context, tx pgx.Tx, cfg AbuseSettings, e *TorrentEvent) error {
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(hits), 0)::bigint FROM account_torrent_hits
		WHERE account_id = $1 AND NOT acted AND ts > now() - make_interval(mins => $2)
	`, e.AccountID, cfg.TorrentSuspendWindowMinutes).Scan(&e.WindowHits); err != nil {
		return err
	}
	if e.WindowHits < int64(cfg.TorrentSuspendHits) {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE accounts SET status = 'suspended', suspend_reason = 'torrent_abuse', updated_at = now()
		WHERE id = $1 AND status = 'active'
	`, e.AccountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	e.Suspended = true
	_, err = tx.Exec(ctx, `UPDATE account_torrent_hits SET acted = true WHERE account_id = $1 AND NOT acted`, e.AccountID)
	return err
}

// TorrentHitRecord is one recorded report, for the account detail view.
type TorrentHitRecord struct {
	TS       time.Time
	NodeName string
	Hits     int
}

// AccountTorrentSummary is what the account detail shows: recent hit reports and totals.
type AccountTorrentSummary struct {
	Hits24h int64
	Hits30d int64
	LastHit *time.Time
	Recent  []TorrentHitRecord
}

// GetAccountTorrentSummary reads the account's torrent history, scoped to callerNamespace.
func (s *Store) GetAccountTorrentSummary(ctx context.Context, accountID string, callerNamespace *string) (AccountTorrentSummary, error) {
	var sum AccountTorrentSummary
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(h.hits) FILTER (WHERE h.ts > now() - interval '24 hours'), 0)::bigint,
		       COALESCE(sum(h.hits), 0)::bigint, max(h.ts)
		FROM account_torrent_hits h
		JOIN accounts ON accounts.id = h.account_id
		WHERE h.account_id = $2 AND `+namespaceFilter+` AND h.ts > now() - interval '30 days'
	`, callerNamespace, accountID).Scan(&sum.Hits24h, &sum.Hits30d, &sum.LastHit)
	if err != nil {
		return sum, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.ts, n.name, h.hits
		FROM account_torrent_hits h
		JOIN accounts ON accounts.id = h.account_id
		JOIN nodes n ON n.id = h.node_id
		WHERE h.account_id = $2 AND `+namespaceFilter+`
		ORDER BY h.ts DESC LIMIT 20
	`, callerNamespace, accountID)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var r TorrentHitRecord
		if err := rows.Scan(&r.TS, &r.NodeName, &r.Hits); err != nil {
			return sum, err
		}
		sum.Recent = append(sum.Recent, r)
	}
	return sum, rows.Err()
}

// PruneTorrentHits drops hit rows older than the retention (the detail view shows 30
// days; the suspend window is at most 7).
func (s *Store) PruneTorrentHits(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM account_torrent_hits WHERE ts < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return err
}
