package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A reseller is an admins row with role 'reseller' plus a reseller_limits row (migration
// 0021). Its accounts carry owner_key_namespace = ResellerNamespace(adminID), which is
// all the existing namespaceFilter machinery needs to isolate it - the code below only
// adds the quota/limit enforcement on top.

const ResellerNamespacePrefix = "reseller:"

// SuspendReasonReseller marks accounts suspended by the super-admin bulk action, so
// the matching bulk reactivate never resurrects accounts suspended for quota/expiry
// or by the reseller itself.
const SuspendReasonReseller = "reseller_suspended"

var (
	ErrResellerNotFound            = errors.New("reseller not found")
	ErrResellerDisabled            = errors.New("reseller is disabled")
	ErrResellerExpired             = errors.New("reseller plan has expired")
	ErrResellerAccountLimit        = errors.New("reseller account limit reached")
	ErrResellerDataCredit          = errors.New("reseller data credit exceeded")
	ErrResellerUnlimitedNotAllowed = errors.New("reseller may not create unlimited-quota accounts")
	ErrResellerExpiryTooLong       = errors.New("expiry is beyond the reseller's maximum validity")
	ErrResellerExpiryRequired      = errors.New("reseller must give accounts an expiry")
	ErrResellerNodeGroupNotAllowed = errors.New("reseller is not allowed to place accounts on that node's group")
	ErrResellerHasAccounts         = errors.New("reseller still owns non-deleted accounts")
	ErrAccountLockedByAdmin        = errors.New("account was suspended by an administrator")
	ErrInvalidQuota                = errors.New("data quota must not be negative")
)

// ResellerNamespace is the owner_key_namespace value for accounts owned by adminID.
func ResellerNamespace(adminID string) string { return ResellerNamespacePrefix + adminID }

// resellerIDFromNamespace extracts the admin id when the caller namespace belongs to
// a reseller (API-key namespaces are key_ids and never carry the prefix).
func resellerIDFromNamespace(ns *string) (string, bool) {
	if ns == nil {
		return "", false
	}
	id, ok := strings.CutPrefix(*ns, ResellerNamespacePrefix)
	return id, ok && id != ""
}

type ResellerLimits struct {
	AdminID        string
	MaxAccounts    *int
	DataLimitBytes *int64
	NodeGroups     []string   // empty = every node group
	ExpiresAt      *time.Time // plan end date; nil = never expires
	Disabled       bool
	Notes          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ResellerUsage struct {
	AccountsUsed       int
	DataAllocatedBytes int64 // sum of data_quota_bytes over non-deleted accounts
	DataUsedBytes      int64 // sum of data_used_bytes over non-deleted accounts
}

type Reseller struct {
	ResellerLimits
	Username string
	Usage    ResellerUsage
}

const resellerLimitColumns = `l.admin_id, l.max_accounts, l.data_limit_bytes, l.node_groups, l.expires_at, l.disabled, l.notes, l.created_at, l.updated_at`

func scanResellerLimits(row pgx.Row, l *ResellerLimits, extra ...any) error {
	dest := []any{&l.AdminID, &l.MaxAccounts, &l.DataLimitBytes, &l.NodeGroups, &l.ExpiresAt, &l.Disabled, &l.Notes, &l.CreatedAt, &l.UpdatedAt}
	return row.Scan(append(dest, extra...)...)
}

// GetResellerLimits is the single-row PK lookup requireAdmin does on every reseller
// request (so disabling takes effect immediately, not when the 15-minute access token
// expires).
func (s *Store) GetResellerLimits(ctx context.Context, adminID string) (ResellerLimits, error) {
	var l ResellerLimits
	err := scanResellerLimits(s.pool.QueryRow(ctx,
		`SELECT `+resellerLimitColumns+` FROM reseller_limits l WHERE l.admin_id = $1`, adminID), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResellerLimits{}, ErrResellerNotFound
	}
	return l, err
}

const resellerSelect = `
	SELECT ` + resellerLimitColumns + `, a.username,
	       u.accounts_used, u.alloc, u.used
	FROM reseller_limits l
	JOIN admins a ON a.id = l.admin_id
	CROSS JOIN LATERAL (
		SELECT count(*)::int AS accounts_used,
		       COALESCE(sum(data_quota_bytes), 0)::bigint AS alloc,
		       COALESCE(sum(data_used_bytes), 0)::bigint AS used
		FROM accounts
		WHERE owner_key_namespace = 'reseller:' || a.id::text AND status != 'deleted'
	) u`

func scanReseller(row pgx.Row, r *Reseller) error {
	return scanResellerLimits(row, &r.ResellerLimits,
		&r.Username, &r.Usage.AccountsUsed, &r.Usage.DataAllocatedBytes, &r.Usage.DataUsedBytes)
}

// GetReseller returns limits plus live usage for one reseller.
func (s *Store) GetReseller(ctx context.Context, adminID string) (Reseller, error) {
	var r Reseller
	err := scanReseller(s.pool.QueryRow(ctx, resellerSelect+` WHERE l.admin_id = $1`, adminID), &r)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reseller{}, ErrResellerNotFound
	}
	return r, err
}

func (s *Store) ListResellers(ctx context.Context) ([]Reseller, error) {
	rows, err := s.pool.Query(ctx, resellerSelect+` ORDER BY a.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reseller
	for rows.Next() {
		var r Reseller
		if err := scanReseller(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NewResellerParams are the initial limits; nil = unlimited, empty NodeGroups = all.
type NewResellerParams struct {
	Username       string
	PasswordHash   string
	MaxAccounts    *int
	DataLimitBytes *int64
	NodeGroups     []string
	ExpiresAt      *time.Time
	Disabled       bool
	Notes          string
}

// CreateReseller inserts the admin row and its limits row atomically - a reseller
// without a limits row could never log in (requireAdmin denies it), but a half-created
// one would still burn the username.
func (s *Store) CreateReseller(ctx context.Context, p NewResellerParams) (Reseller, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reseller{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var adminID string
	err = tx.QueryRow(ctx,
		`INSERT INTO admins (username, password_hash, role) VALUES ($1, $2, 'reseller') RETURNING id`,
		p.Username, p.PasswordHash).Scan(&adminID)
	if err != nil {
		if isUniqueViolation(err) {
			return Reseller{}, ErrUsernameTaken
		}
		return Reseller{}, err
	}
	groups := p.NodeGroups
	if groups == nil {
		groups = []string{}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO reseller_limits (admin_id, max_accounts, data_limit_bytes, node_groups, expires_at, disabled, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		adminID, p.MaxAccounts, p.DataLimitBytes, groups, p.ExpiresAt, p.Disabled, p.Notes); err != nil {
		return Reseller{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Reseller{}, err
	}
	return s.GetReseller(ctx, adminID)
}

// Opt distinguishes a PATCH field that was omitted (Set=false: leave alone) from one
// explicitly set to null (Set=true, Value=nil: back to unlimited).
type Opt[T any] struct {
	Set   bool
	Value *T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

type ResellerLimitsPatch struct {
	MaxAccounts    Opt[int]
	DataLimitBytes Opt[int64]
	ExpiresAt      Opt[time.Time]
	NodeGroups     *[]string
	Disabled       *bool
	Notes          *string
}

// UpdateResellerLimits applies a patch. Lowering a limit below current usage is
// allowed: it blocks further growth but never touches existing accounts. It takes the
// same row lock as quota enforcement, so a limit change and a concurrent create
// serialise cleanly.
func (s *Store) UpdateResellerLimits(ctx context.Context, adminID string, p ResellerLimitsPatch) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cur ResellerLimits
	if err := scanResellerLimits(tx.QueryRow(ctx,
		`SELECT `+resellerLimitColumns+` FROM reseller_limits l WHERE l.admin_id = $1 FOR UPDATE`, adminID), &cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResellerNotFound
		}
		return err
	}
	if p.MaxAccounts.Set {
		cur.MaxAccounts = p.MaxAccounts.Value
	}
	if p.DataLimitBytes.Set {
		cur.DataLimitBytes = p.DataLimitBytes.Value
	}
	if p.ExpiresAt.Set {
		cur.ExpiresAt = p.ExpiresAt.Value
	}
	if p.NodeGroups != nil {
		cur.NodeGroups = *p.NodeGroups
		if cur.NodeGroups == nil {
			cur.NodeGroups = []string{}
		}
	}
	if p.Disabled != nil {
		cur.Disabled = *p.Disabled
	}
	if p.Notes != nil {
		cur.Notes = *p.Notes
	}
	if _, err := tx.Exec(ctx, `
		UPDATE reseller_limits SET max_accounts = $2, data_limit_bytes = $3, node_groups = $4,
			expires_at = $5, disabled = $6, notes = $7, updated_at = now()
		WHERE admin_id = $1`,
		adminID, cur.MaxAccounts, cur.DataLimitBytes, cur.NodeGroups, cur.ExpiresAt, cur.Disabled, cur.Notes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteReseller removes the login and its limits, but only while the reseller owns
// no non-deleted accounts: those accounts would otherwise be orphaned in a namespace
// nobody can act on except a super admin. The limits row lock makes the check race-safe
// against a concurrent create.
func (s *Store) DeleteReseller(ctx context.Context, adminID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := lockResellerRow(ctx, tx, adminID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM accounts WHERE owner_key_namespace = $1 AND status != 'deleted'`,
		ResellerNamespace(adminID)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrResellerHasAccounts
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admins WHERE id = $1 AND role = 'reseller'`, adminID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SuspendResellerAccounts suspends every active account the reseller owns with the
// dedicated reason, returning how many it changed. Accounts already suspended keep
// their original reason.
func (s *Store) SuspendResellerAccounts(ctx context.Context, adminID string) (int, error) {
	if _, err := s.GetResellerLimits(ctx, adminID); err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET status = 'suspended', suspend_reason = $2, updated_at = now()
		WHERE owner_key_namespace = $1 AND status = 'active'`,
		ResellerNamespace(adminID), SuspendReasonReseller)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ReactivateResellerAccounts undoes SuspendResellerAccounts - only accounts whose
// reason is 'reseller_suspended'. One that has meanwhile expired or run out of quota
// is not reactivated; it stays suspended under the real reason instead (otherwise it
// would flip back on for a heartbeat or two before enforcement caught it again).
func (s *Store) ReactivateResellerAccounts(ctx context.Context, adminID string) (reactivated, stillSuspended int, err error) {
	if _, err := s.GetResellerLimits(ctx, adminID); err != nil {
		return 0, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE accounts SET
			status = CASE WHEN (expiry_at IS NOT NULL AND expiry_at <= now())
			                OR (data_quota_bytes IS NOT NULL AND data_used_bytes >= data_quota_bytes)
			              THEN 'suspended' ELSE 'active' END,
			suspend_reason = CASE WHEN expiry_at IS NOT NULL AND expiry_at <= now() THEN 'expired'
			                      WHEN data_quota_bytes IS NOT NULL AND data_used_bytes >= data_quota_bytes THEN 'quota_exceeded'
			                      ELSE NULL END,
			updated_at = now()
		WHERE owner_key_namespace = $1 AND status = 'suspended' AND suspend_reason = $2
		RETURNING status`,
		ResellerNamespace(adminID), SuspendReasonReseller)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return 0, 0, err
		}
		if st == "active" {
			reactivated++
		} else {
			stillSuspended++
		}
	}
	return reactivated, stillSuspended, rows.Err()
}

// lockResellerRow takes the per-reseller row lock every quota-affecting write
// serialises on. Callers must always take it before any node row locks (CreateAccount
// does) so lock order is consistent and cannot deadlock.
func lockResellerRow(ctx context.Context, tx pgx.Tx, adminID string) (ResellerLimits, error) {
	var l ResellerLimits
	err := scanResellerLimits(tx.QueryRow(ctx,
		`SELECT `+resellerLimitColumns+` FROM reseller_limits l WHERE l.admin_id = $1 FOR UPDATE`, adminID), &l)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResellerLimits{}, ErrResellerNotFound
	}
	return l, err
}

// lockResellerForNamespace returns (nil, nil) when ns is not a reseller namespace
// (admin or API key caller: no reseller limits apply), otherwise the locked, enabled
// limits row.
func lockResellerForNamespace(ctx context.Context, tx pgx.Tx, ns *string) (*ResellerLimits, error) {
	id, ok := resellerIDFromNamespace(ns)
	if !ok {
		return nil, nil
	}
	l, err := lockResellerRow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := l.blockedErr(); err != nil {
		return nil, err
	}
	return &l, nil
}

// blockedErr is non-nil when the reseller may not act: disabled by an admin, or past
// its plan end date. Its existing accounts are untouched either way.
func (l *ResellerLimits) blockedErr() error {
	if l.Disabled {
		return ErrResellerDisabled
	}
	if l.ExpiresAt != nil && !l.ExpiresAt.After(time.Now()) {
		return ErrResellerExpired
	}
	return nil
}

// checkExpiry: no account may outlive the reseller's plan, and when the plan has an
// end date an account must carry an expiry (otherwise it would be unbounded).
func (l *ResellerLimits) checkExpiry(expiry *time.Time) error {
	if l.ExpiresAt == nil {
		return nil
	}
	if expiry == nil {
		return ErrResellerExpiryRequired
	}
	if expiry.After(*l.ExpiresAt) {
		return ErrResellerExpiryTooLong
	}
	return nil
}

// resellerAllocatedTx sums the quota already handed out, optionally excluding one
// account (the one being re-quoted).
func resellerAllocatedTx(ctx context.Context, tx pgx.Tx, ns string, excludeAccountID string) (count int, allocated int64, err error) {
	err = tx.QueryRow(ctx, `
		SELECT count(*)::int, COALESCE(sum(data_quota_bytes), 0)::bigint
		FROM accounts
		WHERE owner_key_namespace = $1 AND status != 'deleted' AND ($2 = '' OR id::text != $2)`,
		ns, excludeAccountID).Scan(&count, &allocated)
	return
}

// checkCredit reports whether handing out `add` more bytes would exceed the credit.
// Written as add > limit-allocated (never allocated+add) so a huge add can't overflow.
func (l *ResellerLimits) checkCredit(allocated, add int64) error {
	if l.DataLimitBytes == nil || add <= 0 {
		return nil
	}
	if add > *l.DataLimitBytes-allocated {
		return ErrResellerDataCredit
	}
	return nil
}
