package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Split-tunnel routing, per-account PresharedKey and per-node IPv6 egress (migration
// 0029). Kept in its own file, and read through its own queries instead of
// accountColumns, so the rest of the account code is untouched by this feature.

// AccountRouting is an account's routing override. Mode nil = default (full tunnel).
type AccountRouting struct {
	Mode          *string
	ExcludedCIDRs []string
	Precision     *string // nil = compact
}

// applyNewAccountRoutingTx stores the routing settings and the encrypted PSK on the row
// CreateAccount just inserted (same transaction, so an account never exists half-set).
func applyNewAccountRoutingTx(ctx context.Context, tx pgx.Tx, accountID string, p CreateAccountParams) error {
	if p.RoutingMode == nil && len(p.RoutingExcludedCIDRs) == 0 && p.RoutingPrecision == nil && p.PresharedKeyEncrypted == nil {
		return nil
	}
	cidrs := p.RoutingExcludedCIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	_, err := tx.Exec(ctx, `
		UPDATE accounts SET routing_mode = $2, routing_excluded_cidrs = $3, routing_precision = $4, preshared_key_encrypted = $5
		WHERE id = $1`,
		accountID, p.RoutingMode, cidrs, p.RoutingPrecision, p.PresharedKeyEncrypted)
	return err
}

// GetAccountRouting reads one account's routing override. Not namespace-scoped: callers
// already resolved the account through a scoped lookup.
func (s *Store) GetAccountRouting(ctx context.Context, accountID string) (AccountRouting, error) {
	var r AccountRouting
	err := s.pool.QueryRow(ctx,
		`SELECT routing_mode, routing_excluded_cidrs, routing_precision FROM accounts WHERE id = $1`, accountID,
	).Scan(&r.Mode, &r.ExcludedCIDRs, &r.Precision)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountRouting{}, ErrAccountNotFound
	}
	return r, err
}

// ListAccountRouting is GetAccountRouting for many accounts in one query, so the account
// list pays one extra round trip rather than one per row.
func (s *Store) ListAccountRouting(ctx context.Context, accountIDs []string) (map[string]AccountRouting, error) {
	out := make(map[string]AccountRouting, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, routing_mode, routing_excluded_cidrs, routing_precision FROM accounts WHERE id::text = ANY($1::text[])`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r AccountRouting
		if err := rows.Scan(&id, &r.Mode, &r.ExcludedCIDRs, &r.Precision); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// RoutingUpdate is a PATCH of an account's routing. A nil field is unchanged.
// ClearMode returns the account to the default (NULL) mode - the only way to do that,
// as a nil Mode means "unchanged". ClearPrecision likewise.
type RoutingUpdate struct {
	Mode           *string
	ClearMode      bool
	ExcludedCIDRs  *[]string
	Precision      *string
	ClearPrecision bool
}

// UpdateAccountRouting applies a RoutingUpdate, scoped to callerNamespace like every
// account mutation (a foreign or missing id is ErrAccountNotFound).
func (s *Store) UpdateAccountRouting(ctx context.Context, id string, callerNamespace *string, u RoutingUpdate) error {
	var cidrs []string
	setCIDRs := u.ExcludedCIDRs != nil
	if setCIDRs {
		cidrs = *u.ExcludedCIDRs
		if cidrs == nil {
			cidrs = []string{}
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET
			routing_mode = CASE WHEN $3::bool THEN NULL ELSE COALESCE($4::text, routing_mode) END,
			routing_excluded_cidrs = CASE WHEN $5::bool THEN $6::text[] ELSE routing_excluded_cidrs END,
			routing_precision = CASE WHEN $7::bool THEN NULL ELSE COALESCE($8::text, routing_precision) END,
			updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter,
		callerNamespace, id, u.ClearMode, u.Mode, setCIDRs, cidrs, u.ClearPrecision, u.Precision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// GetAccountPSK returns the encrypted PresharedKey, or nil when the account has none
// yet (created before migration 0029 and not backfilled).
func (s *Store) GetAccountPSK(ctx context.Context, accountID string) (*string, error) {
	var enc *string
	err := s.pool.QueryRow(ctx, `SELECT preshared_key_encrypted FROM accounts WHERE id = $1`, accountID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	return enc, err
}

// ListAccountsMissingPSK returns up to limit accounts with no PresharedKey (deleted ones
// included: a restore or undelete should not find a hole).
func (s *Store) ListAccountsMissingPSK(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM accounts WHERE preshared_key_encrypted IS NULL ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetAccountPSKIfMissing stores the encrypted PSK only while the column is still NULL,
// so two API replicas backfilling concurrently (or a backfill racing a create) can never
// overwrite a key a client may already hold. Reports whether this call wrote it.
func (s *Store) SetAccountPSKIfMissing(ctx context.Context, accountID, encrypted string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET preshared_key_encrypted = $2 WHERE id = $1 AND preshared_key_encrypted IS NULL`, accountID, encrypted)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ServeConfigPSK decides, atomically, whether the client config being served for this
// (account, node) may carry the PSK, and records the decision. wantPSK says the caller
// could include it (the node's agent supports it and the account has one).
//
// The rule: a node peer requires its PSK only if EVERY config ever served for it carried
// it. So a config without the PSK permanently pins the peer to plain mode
// (plain_served), and a config with the PSK is only allowed while no plain one was ever
// served - otherwise an older, PSK-less copy on another device would stop working the
// moment the node started requiring the key. Returns whether to include the PSK.
func (s *Store) ServeConfigPSK(ctx context.Context, accountID, nodeID string, wantPSK bool) (bool, error) {
	if !wantPSK {
		_, err := s.pool.Exec(ctx,
			`UPDATE account_peers SET plain_served = true WHERE account_id = $1 AND node_id = $2 AND NOT plain_served`,
			accountID, nodeID)
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE account_peers SET psk_issued_at = COALESCE(psk_issued_at, now())
		WHERE account_id = $1 AND node_id = $2 AND NOT plain_served`, accountID, nodeID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PeerPSK is what the heartbeat needs per peer: its encrypted PSK, set only for peers
// that qualify under the rule on ServeConfigPSK.
type PeerPSK struct {
	PublicKey    string
	PSKEncrypted string
}

// ListIssuedPSKsForNode returns the PSKs of every active peer on the node that must
// require one (PSK issued and no PSK-less config ever served),
// keyed by the peer's WireGuard public key.
func (s *Store) ListIssuedPSKsForNode(ctx context.Context, nodeID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.public_key, a.preshared_key_encrypted
		FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.node_id = $1 AND a.status = 'active'
		  AND ap.psk_issued_at IS NOT NULL AND NOT ap.plain_served AND a.preshared_key_encrypted IS NOT NULL`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var pub, enc string
		if err := rows.Scan(&pub, &enc); err != nil {
			return nil, err
		}
		out[pub] = enc
	}
	return out, rows.Err()
}

// NodeRouting is a node's PSK/IPv6 state: what the admin set and what the agent
// reported on its last heartbeat.
type NodeRouting struct {
	NodeID         string
	PSKSupported   bool
	IPv6Enabled    bool
	IPv6ULAPrefix  *string
	IPv6HostGlobal bool // agent: the host has a global IPv6 route
	IPv6Ready      bool // agent: forwarding, NAT66 and the wg address are applied
}

// IPv6Active is the single condition under which the panel renders IPv6 for the node:
// the admin enabled it AND the agent reported it working. Never one without the other.
func (n NodeRouting) IPv6Active() bool {
	return n.IPv6Enabled && n.IPv6Ready && n.IPv6ULAPrefix != nil && *n.IPv6ULAPrefix != ""
}

const nodeRoutingColumns = `id, psk_supported, ipv6_enabled, ipv6_ula_prefix, ipv6_host_global, ipv6_ready`

func scanNodeRouting(row pgx.Row, n *NodeRouting) error {
	return row.Scan(&n.NodeID, &n.PSKSupported, &n.IPv6Enabled, &n.IPv6ULAPrefix, &n.IPv6HostGlobal, &n.IPv6Ready)
}

func (s *Store) GetNodeRouting(ctx context.Context, nodeID string) (NodeRouting, error) {
	var n NodeRouting
	err := scanNodeRouting(s.pool.QueryRow(ctx, `SELECT `+nodeRoutingColumns+` FROM nodes WHERE id = $1`, nodeID), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeRouting{}, ErrNodeNotFound
	}
	return n, err
}

func (s *Store) ListNodeRouting(ctx context.Context) ([]NodeRouting, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeRoutingColumns+` FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRouting
	for rows.Next() {
		var n NodeRouting
		if err := scanNodeRouting(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNodeIPv6 sets the admin's side of the IPv6 opt-in. The prefix is stored as given
// (already validated and normalised by the caller); a nil prefix keeps the stored one.
// Turning IPv6 off also clears the agent-reported "ready", so a later re-enable cannot
// render v6 on a stale report before the agent has re-applied it.
func (s *Store) SetNodeIPv6(ctx context.Context, nodeID string, enabled bool, prefix *string) (NodeRouting, error) {
	var n NodeRouting
	err := scanNodeRouting(s.pool.QueryRow(ctx, `
		UPDATE nodes SET ipv6_enabled = $2,
			ipv6_ula_prefix = COALESCE($3::text, ipv6_ula_prefix),
			ipv6_ready = CASE WHEN $2 THEN ipv6_ready ELSE false END
		WHERE id = $1
		RETURNING `+nodeRoutingColumns, nodeID, enabled, prefix), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeRouting{}, ErrNodeNotFound
	}
	return n, err
}

// RecordNodeRoutingState stores the capabilities an agent reported on its heartbeat.
// A separate statement from RecordHeartbeat on purpose: it keeps this feature out of
// that signature, and an old agent (which reports none of this) simply writes false.
func (s *Store) RecordNodeRoutingState(ctx context.Context, nodeID string, pskSupported, ipv6HostGlobal, ipv6Ready bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE nodes SET psk_supported = $2, ipv6_host_global = $3, ipv6_ready = $4 WHERE id = $1`,
		nodeID, pskSupported, ipv6HostGlobal, ipv6Ready)
	return err
}
