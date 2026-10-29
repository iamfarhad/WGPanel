package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/ipalloc"
)

var (
	ErrExternalRefTaken     = errors.New("external_ref already exists")
	ErrAccountNotFound      = errors.New("account not found")
	ErrNodeNotRegistered    = errors.New("node is not registered")
	ErrNodeCapacityExceeded = errors.New("node has no remaining capacity")
	ErrNoAvailableNode      = errors.New("no registered node has remaining capacity")
)

const ipHoldAfterDelete = 24 * time.Hour

type Account struct {
	ID             string
	ExternalRef    *string
	Label          string
	PublicKey      string
	DataQuotaBytes *int64
	DataUsedBytes  int64
	ExpiryAt       *time.Time
	DeviceLimit    *int
	// DeviceLimitHardEnforce upgrades device-limit detection to an automatic suspend
	// (PRD-account-management.md §6.4's per-account toggle); DeviceLimitExceededAt is
	// the standing "currently over the limit" flag, set/cleared by heartbeat ingest.
	DeviceLimitHardEnforce bool
	DeviceLimitExceededAt  *time.Time
	// BandwidthLimitMbps is enforced by the node agent via tc; nil = unshaped.
	BandwidthLimitMbps *int
	// SubscriptionToken is the capability for GET /api/v1/sub/{token} (migration 0016).
	SubscriptionToken string
	Status            string
	SuspendReason     *string
	IPReleaseAt       *time.Time
	OwnerKeyNamespace *string // the api_keys.key_id that created this account, or nil if created by an admin
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// LastHandshakeAt is max(account_peers.last_handshake_at) over the account's peers,
	// nil if no peer has ever completed a handshake. Computed by accountColumns's
	// scalar subquery, so every query that returns an Account carries it (list
	// included) without a per-account follow-up query.
	LastHandshakeAt *time.Time

	// On hold (migration 0025): HoldDurationDays != nil means the validity clock has not
	// started (ExpiryAt is nil). ActivatedAt is when it did.
	HoldDurationDays *int
	HoldDeadlineAt   *time.Time
	ActivatedAt      *time.Time
	// Scheduled usage reset: ResetStrategy is none|day|week|month|year.
	ResetStrategy string
	NextResetAt   *time.Time
	LastResetAt   *time.Time
}

// OnHold reports whether the account is waiting for its first handshake.
func (a Account) OnHold() bool { return a.HoldDurationDays != nil && a.Status != "deleted" }

// AccountPeer is one (account, node) membership - an account can have a peer on
// several nodes simultaneously (see migration 0010). The account's WireGuard keypair
// itself lives on Account, not here - it's shared across every node this account has
// a peer on, since WireGuard peers are identified by public key and the same client
// key is valid as a peer on any number of independent servers at once.
//
// LastReceiveBytes/LastTransmitBytes (see migration 0011) are nil until this peer's
// first heartbeat observation - that first observation only seeds these columns, it
// never itself produces a traffic sample (see ingestBatchedTraffic's doc comment for
// why: without that distinction, the very first heartbeat after a peer starts being
// observed would attribute its entire lifetime WireGuard counter to one ~10s tick).
type AccountPeer struct {
	ID                string
	AccountID         string
	NodeID            string
	AssignedIP        string
	LastReceiveBytes  *int64
	LastTransmitBytes *int64
	LastHandshakeAt   *time.Time
	CreatedAt         time.Time
}

// AccountPeerWithNode adds the node identity fields wg-quick config generation needs
// (docs/STORY-09), fetched via ListAccountPeersWithNode's join.
type AccountPeerWithNode struct {
	AccountPeer
	NodeName           string
	NodePublicEndpoint string
	NodePublicKey      *string
	NodeWGSubnet       string
	NodeDNSForwarder   bool // see migration 0020
}

// The trailing subquery is the account's most recent peer handshake (see
// Account.LastHandshakeAt). It names accounts.id explicitly, so accountColumns is only
// valid in statements whose target/FROM table is `accounts` un-aliased - which is every
// use today, including INSERT/UPDATE ... RETURNING. idx_account_peers_account_id makes
// it an index lookup per returned row.
const accountColumns = `id, external_ref, label, public_key, data_quota_bytes, data_used_bytes, expiry_at, device_limit, device_limit_hard_enforce, device_limit_exceeded_at, bandwidth_limit_mbps, subscription_token, status, suspend_reason, ip_release_at, owner_key_namespace, created_at, updated_at,
	hold_duration_days, hold_deadline_at, activated_at, reset_strategy, next_reset_at, last_reset_at,
	(SELECT max(ap.last_handshake_at) FROM account_peers ap WHERE ap.account_id = accounts.id)`

func scanAccount(row pgx.Row, a *Account) error {
	return row.Scan(
		&a.ID, &a.ExternalRef, &a.Label, &a.PublicKey,
		&a.DataQuotaBytes, &a.DataUsedBytes, &a.ExpiryAt, &a.DeviceLimit,
		&a.DeviceLimitHardEnforce, &a.DeviceLimitExceededAt, &a.BandwidthLimitMbps, &a.SubscriptionToken,
		&a.Status, &a.SuspendReason, &a.IPReleaseAt, &a.OwnerKeyNamespace, &a.CreatedAt, &a.UpdatedAt,
		&a.HoldDurationDays, &a.HoldDeadlineAt, &a.ActivatedAt, &a.ResetStrategy, &a.NextResetAt, &a.LastResetAt,
		&a.LastHandshakeAt,
	)
}

// namespaceFilter is the SQL fragment scoping a query to $1 (the caller's namespace).
// $1 == nil means "admin - see everything," so the fragment becomes a no-op rather
// than an actual filter. Deliberately used everywhere an account id is looked up (not
// just list/search) so an API key can't act on another namespace's account even if
// it guesses a valid id - the row simply won't match, resulting in ErrAccountNotFound
// (404), never a separate "403 wrong namespace" that would leak the account's
// existence (PRD-telegram-bot-api.md §5.2).
const namespaceFilter = `($1::text IS NULL OR owner_key_namespace = $1)`

type CreateAccountParams struct {
	ExternalRef         *string
	Label               string
	NodeIDOrAuto        string // "" or "auto" = every eligible node; a real node UUID = pin to just that one
	PublicKey           string
	PrivateKeyEncrypted string
	DataQuotaBytes      *int64
	ExpiryAt            *time.Time
	DeviceLimit         *int
	BandwidthLimitMbps  *int
	SubscriptionToken   string   // generated by the handler (crypto/rand), never here
	OwnerKeyNamespace   *string  // nil when created by an admin
	AllowedNodeGroups   []string // nil/empty from an admin caller means "no restriction"; from an API key it's that key's node_groups

	// HoldDurationDays != nil creates the account on hold: ExpiryAt must then be nil
	// (the clock starts at the first handshake, or at HoldDeadlineAt if it is set and
	// passes first). ResetStrategy "" = none.
	HoldDurationDays *int
	HoldDeadlineAt   *time.Time
	ResetStrategy    string
}

var ErrNodeGroupNotAllowed = errors.New("this API key is not allowed to provision accounts on that node's group")

type eligibleNode struct {
	id       string
	wgSubnet string
	capacity int
}

// CreateAccount inserts the account row, then gives it a WireGuard peer on every
// currently eligible node (status registered/online, node_group-restricted if the
// caller is a scoped API key) - or, if NodeIDOrAuto names a specific node, just that
// one. Node rows are locked in a deterministic order (ORDER BY id) so two concurrent
// fan-out creates can never deadlock against each other. A single full node is
// skipped, not fatal - only zero successfully provisioned nodes fails the whole
// call (see docs/STORY-09-multi-node-accounts.md).
func (s *Store) CreateAccount(ctx context.Context, p CreateAccountParams) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	// A reseller's limits row is locked FIRST (before any node row) so concurrent
	// creates for one reseller serialise here and cannot overshoot max_accounts or the
	// data credit; consistent lock order also rules out deadlocks against node locks.
	groupErr := ErrNodeGroupNotAllowed
	lim, err := lockResellerForNamespace(ctx, tx, p.OwnerKeyNamespace)
	if err != nil {
		return Account{}, err
	}
	if lim != nil {
		groupErr = ErrResellerNodeGroupNotAllowed
		// The reseller's groups come from the locked row (authoritative). A
		// reseller-owned API key may narrow them further via p.AllowedNodeGroups; the
		// effective set is the intersection, and a disjoint one refuses outright (an
		// empty result must not fall through to "no restriction").
		eff, ok := intersectGroups(lim.NodeGroups, p.AllowedNodeGroups)
		if !ok {
			return Account{}, ErrResellerNodeGroupNotAllowed
		}
		p.AllowedNodeGroups = eff
		if p.DataQuotaBytes != nil && *p.DataQuotaBytes < 0 {
			return Account{}, ErrInvalidQuota
		}
		if lim.DataLimitBytes != nil && p.DataQuotaBytes == nil {
			return Account{}, ErrResellerUnlimitedNotAllowed
		}
		// An on-hold account has no expiry yet; judge the plan limit on the latest date
		// it could end (now + its duration) - activation clamps to the plan expiry
		// again, see activateHeldSQL.
		effExpiry := p.ExpiryAt
		if p.HoldDurationDays != nil {
			t := time.Now().Add(time.Duration(*p.HoldDurationDays) * 24 * time.Hour)
			effExpiry = &t
		}
		if err := lim.checkExpiry(effExpiry); err != nil {
			return Account{}, err
		}
		count, allocated, err := resellerAllocatedTx(ctx, tx, *p.OwnerKeyNamespace, "")
		if err != nil {
			return Account{}, err
		}
		if lim.MaxAccounts != nil && count >= *lim.MaxAccounts {
			return Account{}, ErrResellerAccountLimit
		}
		if p.DataQuotaBytes != nil {
			if err := lim.checkCredit(allocated, *p.DataQuotaBytes); err != nil {
				return Account{}, err
			}
		}
	}

	restrictGroups := len(p.AllowedNodeGroups) > 0
	pinned := p.NodeIDOrAuto != "" && p.NodeIDOrAuto != "auto"

	var nodes []eligibleNode
	if pinned {
		var n eligibleNode
		var status, nodeGroup string
		err := tx.QueryRow(ctx, `
			SELECT id, wg_subnet, status, capacity_max_peers, node_group FROM nodes WHERE id = $1 FOR UPDATE
		`, p.NodeIDOrAuto).Scan(&n.id, &n.wgSubnet, &status, &n.capacity, &nodeGroup)
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrNodeNotFound
		}
		if err != nil {
			return Account{}, err
		}
		// "online" is what RecordHeartbeat overwrites status to the moment the
		// agent's first heartbeat lands after redeeming its join token - by then
		// the node has, by definition, already completed registration, so it's
		// an implicitly-registered superstate of "registered", not a rejection.
		if status != "registered" && status != "online" {
			return Account{}, ErrNodeNotRegistered
		}
		if restrictGroups && !contains(p.AllowedNodeGroups, nodeGroup) {
			return Account{}, groupErr
		}
		nodes = append(nodes, n)
	} else {
		rows, err := tx.Query(ctx, `
			SELECT id, wg_subnet, capacity_max_peers
			FROM nodes
			WHERE status IN ('registered', 'online')
			  AND (NOT $1::bool OR node_group = ANY($2::text[]))
			ORDER BY id
			FOR UPDATE
		`, restrictGroups, p.AllowedNodeGroups)
		if err != nil {
			return Account{}, err
		}
		for rows.Next() {
			var n eligibleNode
			if err := rows.Scan(&n.id, &n.wgSubnet, &n.capacity); err != nil {
				rows.Close()
				return Account{}, err
			}
			nodes = append(nodes, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Account{}, err
		}
		if len(nodes) == 0 {
			return Account{}, ErrNoAvailableNode
		}
	}

	strategy := p.ResetStrategy
	if strategy == "" {
		strategy = ResetNone
	}
	if !ValidResetStrategy(strategy) {
		return Account{}, ErrInvalidResetStrategy
	}
	if p.HoldDurationDays != nil && p.ExpiryAt != nil {
		return Account{}, ErrHoldWithExpiry
	}
	var a Account
	err = scanAccount(tx.QueryRow(ctx, `
		INSERT INTO accounts (external_ref, label, public_key, private_key_encrypted, data_quota_bytes, expiry_at, device_limit, bandwidth_limit_mbps, subscription_token, owner_key_namespace,
		                      hold_duration_days, hold_deadline_at, reset_strategy, next_reset_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::text,
		        CASE WHEN $13::text = 'none' THEN NULL ELSE `+nextResetSQL("$13", "now()")+` END)
		RETURNING `+accountColumns,
		p.ExternalRef, p.Label, p.PublicKey, p.PrivateKeyEncrypted, p.DataQuotaBytes, p.ExpiryAt, p.DeviceLimit, p.BandwidthLimitMbps, p.SubscriptionToken, p.OwnerKeyNamespace,
		p.HoldDurationDays, p.HoldDeadlineAt, strategy,
	), &a)
	if err != nil {
		if isUniqueViolation(err) {
			return Account{}, ErrExternalRefTaken
		}
		return Account{}, err
	}

	provisioned := 0
	for _, n := range nodes {
		ok, err := createPeerForNodeTx(ctx, tx, a.ID, n.id, n.wgSubnet, n.capacity)
		if err != nil {
			return Account{}, err
		}
		if ok {
			provisioned++
		}
	}
	if provisioned == 0 {
		if pinned {
			return Account{}, ErrNodeCapacityExceeded
		}
		return Account{}, ErrNoAvailableNode
	}

	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return a, nil
}

// createPeerForNodeTx allocates an IP and inserts one account_peers row for a single
// node, within an already-open transaction. Returns ok=false (not an error) if the
// node has no room - callers decide whether that's fatal (a single pinned node) or
// just means "skip this one, try the rest" (the fan-out case).
func createPeerForNodeTx(ctx context.Context, tx pgx.Tx, accountID, nodeID, wgSubnet string, capacity int) (bool, error) {
	var currentCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.node_id = $1 AND a.status != 'deleted'
	`, nodeID).Scan(&currentCount); err != nil {
		return false, err
	}
	if currentCount >= capacity {
		return false, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT ap.assigned_ip FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.node_id = $1 AND (a.status != 'deleted' OR a.ip_release_at > now())
	`, nodeID)
	if err != nil {
		return false, err
	}
	var allocated []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return false, err
		}
		allocated = append(allocated, ip)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	ip, err := ipalloc.NextFree(wgSubnet, allocated)
	if errors.Is(err, ipalloc.ErrSubnetExhausted) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO account_peers (account_id, node_id, assigned_ip) VALUES ($1, $2, $3)
	`, accountID, nodeID, ip); err != nil {
		return false, err
	}
	return true, nil
}

// BackfillAccountPeersForNode gives every currently-active account a peer on a node
// that just finished registration (called from RedeemJoinToken's success path) - so
// accounts provisioned before this node existed still end up synced to it, not just
// accounts created afterward. Unconditional across all active accounts regardless of
// which API key/admin originally created them or what node_groups restriction was
// applied at their creation time - node_group scoping restricts explicit node
// selection at creation, it isn't a standing partition promise for future nodes.
func (s *Store) BackfillAccountPeersForNode(ctx context.Context, nodeID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var wgSubnet string
	var capacity int
	err = tx.QueryRow(ctx, `SELECT wg_subnet, capacity_max_peers FROM nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&wgSubnet, &capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNodeNotFound
	}
	if err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `
		SELECT a.id FROM accounts a
		WHERE a.status = 'active'
		  AND NOT EXISTS (SELECT 1 FROM account_peers ap WHERE ap.account_id = a.id AND ap.node_id = $1)
	`, nodeID)
	if err != nil {
		return err
	}
	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		accountIDs = append(accountIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, accountID := range accountIDs {
		if _, err := createPeerForNodeTx(ctx, tx, accountID, nodeID, wgSubnet, capacity); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ListAccountPeersWithNode returns every node this account currently has a peer on,
// joined with that node's public identity (needed for wg-quick config generation).
// Scoped to callerNamespace the same way GetAccount is - callers should already have
// 404'd via GetAccount before calling this, so a namespace mismatch here just yields
// an empty slice rather than needing its own not-found handling.
func (s *Store) ListAccountPeersWithNode(ctx context.Context, accountID string, callerNamespace *string) ([]AccountPeerWithNode, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ap.id, ap.account_id, ap.node_id, ap.assigned_ip,
		       ap.last_receive_bytes, ap.last_transmit_bytes, ap.last_handshake_at, ap.created_at,
		       n.name, n.public_endpoint, n.public_key, n.wg_subnet, n.dns_forwarder
		FROM account_peers ap
		JOIN nodes n ON n.id = ap.node_id
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.account_id = $2 AND `+namespaceFilter+`
		ORDER BY ap.created_at
	`, callerNamespace, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []AccountPeerWithNode
	for rows.Next() {
		var p AccountPeerWithNode
		if err := rows.Scan(
			&p.ID, &p.AccountID, &p.NodeID, &p.AssignedIP,
			&p.LastReceiveBytes, &p.LastTransmitBytes, &p.LastHandshakeAt, &p.CreatedAt,
			&p.NodeName, &p.NodePublicEndpoint, &p.NodePublicKey, &p.NodeWGSubnet, &p.NodeDNSForwarder,
		); err != nil {
			return nil, err
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// DesiredPeer is what a node's WireGuard interface should have configured for one
// account - enough for cmd/agent to build a wgtypes.PeerConfig plus the tc traffic
// shaping (BandwidthLimitMbps, nil = unshaped) that rides along with it.
type DesiredPeer struct {
	PublicKey          string
	AssignedIP         string
	BandwidthLimitMbps *int
}

// ListDesiredPeersForNode is what handleAgentHeartbeat sends back to the agent every
// ~10s so it can reconcile the node's real WireGuard interface (docs/STORY-09-
// multi-node-accounts.md). Only 'active' accounts are included - suspending or
// deleting an account removes it from this list on the very next call, which is how
// enforcement actually happens: the agent's next ConfigureDevice simply stops
// including that peer, no separate "tear down" call needed anywhere.
func (s *Store) ListDesiredPeersForNode(ctx context.Context, nodeID string) ([]DesiredPeer, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.public_key, ap.assigned_ip, a.bandwidth_limit_mbps
		FROM account_peers ap
		JOIN accounts a ON a.id = ap.account_id
		WHERE ap.node_id = $1 AND a.status = 'active'
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []DesiredPeer
	for rows.Next() {
		var p DesiredPeer
		if err := rows.Scan(&p.PublicKey, &p.AssignedIP, &p.BandwidthLimitMbps); err != nil {
			return nil, err
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// GetAccountBySubscriptionToken resolves a subscription capability URL's token to its
// account. Runs the same lazy expiry reconciliation as GetAccount so a subscription
// fetch can't hand out a config for an account that should already be suspended.
// Never namespace-scoped: possession of the token IS the authorization.
func (s *Store) GetAccountBySubscriptionToken(ctx context.Context, token string) (Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return Account{}, err
	}
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE subscription_token = $1 AND status != 'deleted'`,
		token,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// RotateSubscriptionToken replaces the account's subscription token, immediately
// invalidating the old URL - the recovery path for a leaked/shared subscription link,
// deliberately independent of the WireGuard keypair (rotating the URL shouldn't force
// every legitimate device to re-import its tunnel config).
func (s *Store) RotateSubscriptionToken(ctx context.Context, id string, callerNamespace *string, newToken string) (Account, error) {
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx, `
		UPDATE accounts SET subscription_token = $3, updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id, newToken,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// GetAccount looks up an account, scoped to callerNamespace (nil = admin, sees
// everything). A mismatched namespace and a genuinely missing id are indistinguishable
// on purpose - both return ErrAccountNotFound.
func (s *Store) GetAccount(ctx context.Context, id string, callerNamespace *string) (Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return Account{}, err
	}
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $2 AND `+namespaceFilter,
		callerNamespace, id,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// ListAccounts' ownerFilter is an admin-only narrowing on top of the caller
// namespace: "" = no filter, "admin" = accounts created by admins (no owner),
// anything else = that exact owner_key_namespace (e.g. "reseller:<id>"). Deleted
// accounts are excluded. The order is (created_at, id) - id is the tiebreak that keeps
// limit/offset paging stable when several accounts share a created_at.
func (s *Store) ListAccounts(ctx context.Context, limit, offset int, callerNamespace *string, ownerFilter string) ([]Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE status != 'deleted' AND `+namespaceFilter+`
		 AND ($3 = '' OR ($3 = 'admin' AND owner_key_namespace IS NULL) OR owner_key_namespace = $3)
		 ORDER BY created_at, id LIMIT $2 OFFSET $4`,
		callerNamespace, limit, ownerFilter, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := scanAccount(rows, &a); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// FindAccountsByExternalRef returns the account(s) in callerNamespace whose
// external_ref equals ref exactly. Unlike every other read it INCLUDES status
// 'deleted': external_ref stays unique after a soft delete (the row is kept), so a
// client that wants to know "is this ref taken?" has to see the tombstone too. The
// column is globally UNIQUE, so at most one row can match; a slice is returned only to
// keep the list-shaped API response. Empty (nil) when nothing matches or the match is
// in another namespace.
func (s *Store) FindAccountsByExternalRef(ctx context.Context, ref string, callerNamespace *string) ([]Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE external_ref = $2 AND `+namespaceFilter+` ORDER BY created_at, id`,
		callerNamespace, ref,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := scanAccount(rows, &a); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

type UpdateAccountParams struct {
	Label          *string
	DataQuotaBytes *int64
	ExpiryAt       *time.Time
	DeviceLimit    *int
	// BandwidthLimitMbps follows the same nil-means-unchanged convention, with one
	// extension: 0 explicitly clears the limit back to NULL (unshaped). "Remove the
	// rate limit" is a real operation in a way "remove the quota" hasn't needed to be,
	// and 0 Mbps is meaningless as an actual limit, so the sentinel is unambiguous.
	BandwidthLimitMbps     *int
	DeviceLimitHardEnforce *bool
	// ClearDataQuota / ClearDeviceLimit set the column back to NULL (unlimited). They are
	// the only way to do that - a nil DataQuotaBytes/DeviceLimit means "unchanged". The
	// HTTP layer rejects combining a clear flag with a value for the same field; if both
	// reach here the clear wins.
	ClearDataQuota   bool
	ClearDeviceLimit bool
	// ResetStrategy nil = unchanged; a value (none|day|week|month|year) replaces it and
	// restarts the schedule from now. HoldDurationDays changes the duration of an account
	// that is still on hold (ignored once active). Setting ExpiryAt on an on-hold account
	// activates it with that fixed date.
	ResetStrategy    *string
	HoldDurationDays *int
}

// UpdateAccount only touches fields explicitly provided (nil = leave unchanged). For a
// reseller caller the new quota/expiry are validated against its limits inside the
// same transaction, under the reseller row lock.
//
// ClearDataQuota makes the account unlimited, so a reseller with a data credit is
// refused with ErrResellerUnlimitedNotAllowed - the same rule CreateAccount applies.
//
// Data-cap enforcement (limits.go) is applied to the result:
//   - a PATCH that changes the quota (a new value, or ClearDataQuota) on an account
//     suspended for quota_exceeded reactivates it when the new cap is above its usage
//     (a cleared quota always is) and it has not expired; a PATCH that does not touch
//     the quota never lifts anything, and neither does any other suspension reason;
//   - a PATCH that leaves an active account at or over its (new) cap suspends it as
//     quota_exceeded - or as expired, which wins - and the returned account says so.
//
// ClearDeviceLimit also resets the standing device_limit_exceeded_at flag:
// reconcileDeviceLimitsTx skips accounts whose limit is NULL, so a stale flag would
// otherwise never clear.
func (s *Store) UpdateAccount(ctx context.Context, id string, callerNamespace *string, p UpdateAccountParams) (Account, error) {
	if p.ResetStrategy != nil && !ValidResetStrategy(*p.ResetStrategy) {
		return Account{}, ErrInvalidResetStrategy
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	lim, err := lockResellerForNamespace(ctx, tx, callerNamespace)
	if err != nil {
		return Account{}, err
	}
	if lim != nil {
		if p.ExpiryAt != nil {
			if err := lim.checkExpiry(p.ExpiryAt); err != nil {
				return Account{}, err
			}
		}
		if p.ClearDataQuota && lim.DataLimitBytes != nil {
			return Account{}, ErrResellerUnlimitedNotAllowed
		}
		if p.DataQuotaBytes != nil && !p.ClearDataQuota {
			if *p.DataQuotaBytes < 0 {
				return Account{}, ErrInvalidQuota
			}
			_, allocated, err := resellerAllocatedTx(ctx, tx, *callerNamespace, id)
			if err != nil {
				return Account{}, err
			}
			if err := lim.checkCredit(allocated, *p.DataQuotaBytes); err != nil {
				return Account{}, err
			}
		}
	}

	// $9 = clear quota, $10 = clear device limit. Every RHS in an UPDATE sees the
	// pre-update row, so newQuota/newExpiry spell out the values the row is about to get;
	// liftQuota is judged on those (a PATCH that also moves expiry_at is judged on the
	// date the account will actually carry) but on the OLD status/suspend_reason/usage.
	const (
		newQuota  = `(CASE WHEN $9::bool THEN NULL ELSE COALESCE($4::bigint, data_quota_bytes) END)`
		newExpiry = `COALESCE($5::timestamptz, expiry_at)`
		liftQuota = `(($9::bool OR $4::bigint IS NOT NULL)
			AND status = 'suspended' AND suspend_reason = 'quota_exceeded'
			AND (` + newExpiry + ` IS NULL OR ` + newExpiry + ` > now())
			AND (` + newQuota + ` IS NULL OR data_used_bytes < ` + newQuota + `))`
	)
	var a Account
	err = scanAccount(tx.QueryRow(ctx, `
		UPDATE accounts SET
			label = COALESCE($3, label),
			data_quota_bytes = `+newQuota+`,
			expiry_at = COALESCE($5, expiry_at),
			device_limit = CASE WHEN $10::bool THEN NULL ELSE COALESCE($6, device_limit) END,
			device_limit_exceeded_at = CASE WHEN $10::bool THEN NULL ELSE device_limit_exceeded_at END,
			bandwidth_limit_mbps = CASE WHEN $7::int IS NULL THEN bandwidth_limit_mbps
			                            WHEN $7 = 0 THEN NULL
			                            ELSE $7 END,
			device_limit_hard_enforce = COALESCE($8, device_limit_hard_enforce),
			hold_duration_days = CASE WHEN hold_duration_days IS NULL OR $5::timestamptz IS NOT NULL THEN NULL
			                          ELSE COALESCE($12::int, hold_duration_days) END,
			hold_deadline_at = CASE WHEN $5::timestamptz IS NOT NULL THEN NULL ELSE hold_deadline_at END,
			activated_at = CASE WHEN hold_duration_days IS NOT NULL AND $5::timestamptz IS NOT NULL THEN now() ELSE activated_at END,
			reset_strategy = COALESCE($11::text, reset_strategy),
			next_reset_at = CASE WHEN $11::text IS NULL THEN next_reset_at
			                     WHEN $11::text = 'none' THEN NULL
			                     ELSE `+nextResetSQL("$11", "now()")+` END,
			status = CASE WHEN `+liftQuota+` THEN 'active' ELSE status END,
			suspend_reason = CASE WHEN `+liftQuota+` THEN NULL ELSE suspend_reason END,
			updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id, p.Label, p.DataQuotaBytes, p.ExpiryAt, p.DeviceLimit, p.BandwidthLimitMbps, p.DeviceLimitHardEnforce,
		p.ClearDataQuota, p.ClearDeviceLimit, p.ResetStrategy, p.HoldDurationDays,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, err
	}
	if err := settleAccountTx(ctx, tx, &a); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return a, nil
}

func (s *Store) SuspendAccount(ctx context.Context, id string, callerNamespace *string, reason string) (Account, error) {
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx, `
		UPDATE accounts SET status = 'suspended', suspend_reason = $3, updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id, reason,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// EnableAccount reactivates an account. A reseller caller may not undo the
// super-admin bulk suspension (suspend_reason 'reseller_suspended') - that is
// ErrAccountLockedByAdmin (403), distinct from the 404 for a foreign namespace.
//
// An account at or over its data cap cannot be enabled, whatever the suspension reason:
// the next sample (or read) would suspend it again as quota_exceeded, and the caller
// would be left believing the enable worked. That is ErrAccountQuotaExhausted (409);
// raise the quota, renew with add_quota_gb or reset the usage first.
//
// The reseller guard is NULL-safe: it used to evaluate to NULL for an account with no
// suspend_reason, so a reseller enabling an already-active account got
// ErrAccountLockedByAdmin.
func (s *Store) EnableAccount(ctx context.Context, id string, callerNamespace *string) (Account, error) {
	_, isReseller := resellerIDFromNamespace(callerNamespace)
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx, `
		UPDATE accounts SET status = 'active', suspend_reason = NULL, updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		  AND NOT COALESCE($3::bool AND suspend_reason = 'reseller_suspended', false)
		  AND NOT `+accountQuotaReachedSQL+`
		RETURNING `+accountColumns,
		callerNamespace, id, isReseller,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing matched: say which guard it was (404 and 403 keep precedence).
		var locked, exhausted bool
		qerr := s.pool.QueryRow(ctx, `
			SELECT COALESCE($3::bool AND suspend_reason = 'reseller_suspended', false), `+accountQuotaReachedSQL+`
			FROM accounts WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter,
			callerNamespace, id, isReseller,
		).Scan(&locked, &exhausted)
		switch {
		case errors.Is(qerr, pgx.ErrNoRows):
			return Account{}, ErrAccountNotFound
		case qerr != nil:
			return Account{}, qerr
		case locked:
			return Account{}, ErrAccountLockedByAdmin
		case exhausted:
			return Account{}, ErrAccountQuotaExhausted
		}
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// RenewAccount adds quota and/or extends expiry, then reactivates the account if it
// was suspended solely for quota_exceeded or expired and the new numbers resolve
// that (PRD §6.2-6.3). A manual or abuse_flag suspension is left untouched - renewing
// quota doesn't override an operator's deliberate suspension.
func (s *Store) RenewAccount(ctx context.Context, id string, callerNamespace *string, addQuotaBytes *int64, extendExpiryTo *time.Time) (Account, error) {
	return s.RenewAccountHold(ctx, id, callerNamespace, addQuotaBytes, extendExpiryTo, nil)
}

// RenewAccountHold is RenewAccount plus addHoldDays: for an account still on hold the
// validity clock has not started, so "extend by N days" lengthens the duration it will
// get at activation (hold_duration_days += N) instead of touching expiry_at.
func (s *Store) RenewAccountHold(ctx context.Context, id string, callerNamespace *string, addQuotaBytes *int64, extendExpiryTo *time.Time, addHoldDays *int) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	lim, err := lockResellerForNamespace(ctx, tx, callerNamespace)
	if err != nil {
		return Account{}, err
	}
	if lim != nil {
		if extendExpiryTo != nil {
			if err := lim.checkExpiry(extendExpiryTo); err != nil {
				return Account{}, err
			}
		}
		if addQuotaBytes != nil {
			var cur *int64
			err := tx.QueryRow(ctx, `SELECT data_quota_bytes FROM accounts WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter,
				callerNamespace, id).Scan(&cur)
			if errors.Is(err, pgx.ErrNoRows) {
				return Account{}, ErrAccountNotFound
			}
			if err != nil {
				return Account{}, err
			}
			if cur != nil { // an unlimited account stays unlimited; nothing to allocate
				if *cur+*addQuotaBytes < 0 {
					return Account{}, ErrInvalidQuota
				}
				_, allocated, err := resellerAllocatedTx(ctx, tx, *callerNamespace, "")
				if err != nil {
					return Account{}, err
				}
				if err := lim.checkCredit(allocated, *addQuotaBytes); err != nil {
					return Account{}, err
				}
			}
		}
	}

	var a Account
	err = scanAccount(tx.QueryRow(ctx, `
		UPDATE accounts SET
			data_quota_bytes = CASE WHEN $3::bigint IS NULL THEN data_quota_bytes
			                        WHEN data_quota_bytes IS NULL THEN NULL
			                        ELSE data_quota_bytes + $3::bigint END,
			expiry_at = COALESCE($4, expiry_at),
			hold_duration_days = CASE WHEN hold_duration_days IS NOT NULL AND $5::int IS NOT NULL
			                          THEN hold_duration_days + $5::int ELSE hold_duration_days END,
			updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id, addQuotaBytes, extendExpiryTo, addHoldDays,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, err
	}

	// An expired account is not revived by data alone: a quota_exceeded suspension is
	// lifted only while the account is also inside its validity (expired wins, limits.go).
	notExpired := a.ExpiryAt == nil || a.ExpiryAt.After(time.Now())
	quotaResolved := a.SuspendReason != nil && *a.SuspendReason == "quota_exceeded" && notExpired &&
		(a.DataQuotaBytes == nil || a.DataUsedBytes < *a.DataQuotaBytes)
	expiryResolved := a.SuspendReason != nil && *a.SuspendReason == "expired" && notExpired

	if a.Status == "suspended" && (quotaResolved || expiryResolved) {
		err = scanAccount(tx.QueryRow(ctx, `
			UPDATE accounts SET status = 'active', suspend_reason = NULL, updated_at = now()
			WHERE id = $1
			RETURNING `+accountColumns,
			id,
		), &a)
		if err != nil {
			return Account{}, err
		}
	}
	// A renewal that extends validity but leaves the account at its data cap (or one that
	// shrinks the quota) must not report an active account the next sample would suspend.
	if err := settleAccountTx(ctx, tx, &a); err != nil {
		return Account{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return a, nil
}

// ResetAccountUsage zeroes data_used_bytes (a new billing cycle for the same quota) and,
// if the account was suspended for quota_exceeded and has not expired, reactivates it.
// It is one UPDATE, so both effects commit or neither does. Every other suspension
// reason (manual, abuse_flag, expired, device_limit, reseller_suspended) is left alone
// - resetting usage must never override an operator's deliberate suspension - and
// data_quota_bytes is untouched.
//
// Reactivation is just the status flip, exactly like EnableAccount: the node agents
// pick the account back up on their next heartbeat, because ListDesiredPeersForNode
// returns every 'active' account's peers.
//
// Sampling is unaffected: account_peers.last_receive_bytes/last_transmit_bytes are the
// per-peer baselines IngestHeartbeatTelemetry diffs against, and this does not touch
// them, so the next sample adds only the traffic since the previous heartbeat.
//
// A reseller caller goes through the same row lock/blocked check as RenewAccount
// (disabled or expired reseller -> ErrResellerDisabled/Expired).
func (s *Store) ResetAccountUsage(ctx context.Context, id string, callerNamespace *string) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	limits, err := lockResellerForNamespace(ctx, tx, callerNamespace)
	if err != nil {
		return Account{}, err
	}
	// The credit counts quota handed out, not traffic used, so a reset by a credit-limited
	// reseller would hand the same quota out again for nothing. Such a reseller renews with
	// add_quota_gb instead, which spends credit.
	if limits != nil && limits.DataLimitBytes != nil {
		return Account{}, ErrResellerResetNotAllowed
	}

	a, err := resetUsageTx(ctx, tx, id, callerNamespace)
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return a, nil
}

// resetUsageTx is the reset itself, shared by the manual endpoint and the scheduled
// sweep (ResetDueUsage) so counters, reactivation and cap settlement cannot diverge.
// A nil callerNamespace is unscoped (the sweep). It records last_reset_at.
func resetUsageTx(ctx context.Context, tx pgx.Tx, id string, callerNamespace *string) (Account, error) {
	const liftQuota = `(status = 'suspended' AND suspend_reason = 'quota_exceeded' AND (expiry_at IS NULL OR expiry_at > now()))`
	var a Account
	err := scanAccount(tx.QueryRow(ctx, `
		UPDATE accounts SET
			data_used_bytes = 0,
			last_reset_at = now(),
			status = CASE WHEN `+liftQuota+` THEN 'active' ELSE status END,
			suspend_reason = CASE WHEN `+liftQuota+` THEN NULL ELSE suspend_reason END,
			updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, err
	}
	// Zero usage is under every cap except a quota of exactly 0 bytes, which is always
	// "reached": settle so that corner is not reported active.
	if err := settleAccountTx(ctx, tx, &a); err != nil {
		return Account{}, err
	}
	return a, nil
}

// SoftDeleteAccount marks the account deleted and starts the 24h IP-reuse hold
// (PRD §6.1) rather than releasing the IP immediately. account_peers rows are left
// in place (not deleted) - every query that computes capacity/allocation/desired
// heartbeat peers already filters on the account's status, so a deleted account's
// peer rows are simply inert rather than needing their own cleanup pass.
func (s *Store) SoftDeleteAccount(ctx context.Context, id string, callerNamespace *string) (Account, error) {
	var a Account
	err := scanAccount(s.pool.QueryRow(ctx, `
		UPDATE accounts SET status = 'deleted', ip_release_at = now() + $3, updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		RETURNING `+accountColumns,
		callerNamespace, id, ipHoldAfterDelete,
	), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

// GetAccountPrivateKey fetches only the encrypted private key - kept separate from
// the general Account struct/scanAccount so ordinary reads never carry key material
// through code paths that don't need it.
func (s *Store) GetAccountPrivateKey(ctx context.Context, id string, callerNamespace *string) (string, error) {
	var encrypted string
	err := s.pool.QueryRow(ctx,
		`SELECT private_key_encrypted FROM accounts WHERE id = $2 AND `+namespaceFilter,
		callerNamespace, id,
	).Scan(&encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAccountNotFound
	}
	return encrypted, err
}

// intersectGroups combines a reseller's groups with a key's narrower set; empty on
// either side means "no restriction from that side". ok=false = both restrict and
// share nothing.
func intersectGroups(reseller, key []string) (out []string, ok bool) {
	switch {
	case len(reseller) == 0:
		return key, true
	case len(key) == 0:
		return reseller, true
	}
	for _, g := range key {
		if contains(reseller, g) {
			out = append(out, g)
		}
	}
	return out, len(out) > 0
}

// ListAccountsAfter is ListAccounts' keyset (cursor) variant: rows strictly after
// (afterCreatedAt, afterID) in the same (created_at, id) order. A nil afterCreatedAt
// starts from the beginning. Unlike offset paging it stays correct and cheap while
// accounts are created or deleted between page fetches.
func (s *Store) ListAccountsAfter(ctx context.Context, limit int, afterCreatedAt *time.Time, afterID string, callerNamespace *string, ownerFilter string) ([]Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return nil, err
	}
	var afterIDArg any
	if afterCreatedAt != nil {
		afterIDArg = afterID
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE status != 'deleted' AND `+namespaceFilter+`
		 AND ($3 = '' OR ($3 = 'admin' AND owner_key_namespace IS NULL) OR owner_key_namespace = $3)
		 AND ($4::timestamptz IS NULL OR (created_at, id) > ($4::timestamptz, $5::uuid))
		 ORDER BY created_at, id LIMIT $2`,
		callerNamespace, limit, ownerFilter, afterCreatedAt, afterIDArg,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := scanAccount(rows, &a); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}
