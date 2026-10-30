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
	ErrResellerResetNotAllowed     = errors.New("a reseller with a traffic credit may not reset usage")
	ErrResellerExpiryTooLong       = errors.New("expiry is beyond the reseller's maximum validity")
	ErrResellerExpiryRequired      = errors.New("reseller must give accounts an expiry")
	ErrResellerNodeGroupNotAllowed = errors.New("reseller is not allowed to place accounts on that node's group")
	ErrResellerHasAccounts         = errors.New("reseller still owns non-deleted accounts")
	ErrAccountLockedByAdmin        = errors.New("account was suspended by an administrator")
	ErrInvalidQuota                = errors.New("data quota must not be negative")
)

// Hierarchy errors (migration 0032). The Child* ones are the carving rules: a child's
// plan is cut out of its parent's, so every one of them is a refusal about the plan the
// request asked for (422) except the allocation ones, which depend on how much the
// parent has left right now (409).
var (
	ErrResellerTooDeep          = errors.New("reseller hierarchy is too deep")
	ErrChildAccountAllocation   = errors.New("child account limit exceeds the parent's remaining allocation")
	ErrChildCreditAllocation    = errors.New("child traffic credit exceeds the parent's remaining credit")
	ErrChildLimitRequired       = errors.New("a child of a limited reseller must have finite account and traffic limits")
	ErrChildExpiryTooLong       = errors.New("child plan expiry is beyond the parent's plan expiry")
	ErrChildExpiryRequired      = errors.New("a child of an expiring reseller must have a plan expiry")
	ErrChildNodeGroupNotAllowed = errors.New("child node groups must be a subset of the parent's")
	ErrResellerHasChildren      = errors.New("reseller still has child resellers")
	ErrResellerCarveConflict    = errors.New("limit change conflicts with what the reseller already handed to its children")
	ErrChildLockedByAdmin       = errors.New("reseller was disabled by an administrator")
	ErrNotDescendant            = errors.New("reseller is not in your subtree")
)

// MaxResellerDepth is how many reseller levels may be stacked below the super admin
// (a top-level reseller is depth 1). Three covers the real market structure -
// wholesaler -> agent -> sub-agent - plus one spare level for a sub-agent's own
// helper, which is the deepest a human can still audit. Deeper trees add nothing but
// cost: every subtree read (accounts list, reports) widens with depth, a chain of four
// resellers is no longer a business anyone can reconcile, and each level multiplies the
// "who disabled whom" questions the UI has to answer. It is a constant, not a setting,
// because the carving rules and their tests are written against it.
const MaxResellerDepth = 3

// SuspendReasonParent marks accounts a parent reseller suspended in bulk through its
// child. Unlike SuspendReasonReseller (staff only), any ancestor can undo it but the
// owning reseller cannot - see EnableAccount and ReactivateChildAccounts.
const SuspendReasonParent = "parent_suspended"

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

	// Hierarchy (migration 0032). ParentID is nil for a top-level reseller; Ancestors
	// holds every ancestor namespace, root first.
	ParentID        *string
	Ancestors       []string
	DisabledByStaff bool
	// EffectiveExpiresAt is the earliest plan end along the chain (own included) and
	// AncestorDisabled whether any ancestor is disabled - the SQL functions in
	// migration 0032. Both are derived: they cannot be set, only inherited.
	EffectiveExpiresAt *time.Time
	AncestorDisabled   bool
	// ReservedAccounts/ReservedBytes are the direct children's limits, i.e. what this
	// reseller has already handed down. Filled by lockResellerRow and the Reseller
	// view; zero elsewhere.
	ReservedAccounts int
	ReservedBytes    int64
}

// Depth is the reseller's level: 1 for a top-level reseller.
func (l *ResellerLimits) Depth() int { return len(l.Ancestors) + 1 }

// IsDisabled: the reseller or any ancestor is disabled.
func (l *ResellerLimits) IsDisabled() bool { return l.Disabled || l.AncestorDisabled }

// PlanEnd is the earliest plan end along the chain, nil when nothing expires. It folds
// ExpiresAt in as well so a hand-built ResellerLimits (tests) behaves like a loaded one.
func (l *ResellerLimits) PlanEnd() *time.Time {
	end := l.ExpiresAt
	if l.EffectiveExpiresAt != nil && (end == nil || l.EffectiveExpiresAt.Before(*end)) {
		end = l.EffectiveExpiresAt
	}
	return end
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
	Children int // direct child resellers
}

const resellerLimitColumns = `l.admin_id, l.max_accounts, l.data_limit_bytes, l.node_groups, l.expires_at, l.disabled, l.notes, l.created_at, l.updated_at,
	l.parent_id::text, l.ancestors, l.disabled_by_staff, reseller_effective_expires_at(l.admin_id), reseller_ancestor_disabled(l.admin_id)`

func scanResellerLimits(row pgx.Row, l *ResellerLimits, extra ...any) error {
	dest := []any{&l.AdminID, &l.MaxAccounts, &l.DataLimitBytes, &l.NodeGroups, &l.ExpiresAt, &l.Disabled, &l.Notes, &l.CreatedAt, &l.UpdatedAt,
		&l.ParentID, &l.Ancestors, &l.DisabledByStaff, &l.EffectiveExpiresAt, &l.AncestorDisabled}
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
	       u.accounts_used, u.alloc, u.used, k.res_accounts, k.res_bytes, k.children
	FROM reseller_limits l
	JOIN admins a ON a.id = l.admin_id
	CROSS JOIN LATERAL (
		SELECT count(*)::int AS accounts_used,
		       COALESCE(sum(data_quota_bytes), 0)::bigint AS alloc,
		       COALESCE(sum(data_used_bytes), 0)::bigint AS used
		FROM accounts
		WHERE owner_key_namespace = 'reseller:' || a.id::text AND status != 'deleted'
	) u
	CROSS JOIN LATERAL (
		SELECT COALESCE(sum(c.max_accounts), 0)::int AS res_accounts,
		       COALESCE(sum(c.data_limit_bytes), 0)::bigint AS res_bytes,
		       count(*)::int AS children
		FROM reseller_limits c WHERE c.parent_id = a.id
	) k`

func scanReseller(row pgx.Row, r *Reseller) error {
	return scanResellerLimits(row, &r.ResellerLimits,
		&r.Username, &r.Usage.AccountsUsed, &r.Usage.DataAllocatedBytes, &r.Usage.DataUsedBytes,
		&r.ReservedAccounts, &r.ReservedBytes, &r.Children)
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
	// ParentID makes the new reseller a child of that reseller (nil = top level, staff
	// only). A child's plan is carved out of the parent's: see carveChild.
	ParentID *string
	// ByParent says a reseller (not staff) is creating it; only then may the child's
	// disabled state be cleared by the parent later (DisabledByStaff stays false).
	ByParent bool
}

// CreateReseller inserts the admin row and its limits row atomically - a reseller
// without a limits row could never log in (requireAdmin denies it), but a half-created
// one would still burn the username. For a child it first locks the parent's limits
// row, so concurrent child creates (and the parent's own account creates) serialise
// and cannot over-allocate the parent's plan - same discipline as CreateAccount.
func (s *Store) CreateReseller(ctx context.Context, p NewResellerParams) (Reseller, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reseller{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ancestors := []string{}
	if p.ParentID != nil {
		parent, err := lockResellerRow(ctx, tx, *p.ParentID)
		if err != nil {
			return Reseller{}, err
		}
		if err := parent.loadReserved(ctx, tx); err != nil {
			return Reseller{}, err
		}
		// A disabled/expired parent (or one under such an ancestor) cannot grow its tree.
		if err := parent.blockedErr(); err != nil {
			return Reseller{}, err
		}
		if parent.Depth() >= MaxResellerDepth {
			return Reseller{}, ErrResellerTooDeep
		}
		own, alloc, err := resellerAllocatedTx(ctx, tx, ResellerNamespace(parent.AdminID), "")
		if err != nil {
			return Reseller{}, err
		}
		p.NodeGroups, err = parent.carveChild(childPlan{
			MaxAccounts: p.MaxAccounts, DataLimitBytes: p.DataLimitBytes, ExpiresAt: p.ExpiresAt, NodeGroups: p.NodeGroups,
			checkAccounts: true, checkCredit: true, checkExpiry: true, checkGroups: true,
		}, own, alloc, 0, 0)
		if err != nil {
			return Reseller{}, err
		}
		ancestors = append(append(ancestors, parent.Ancestors...), ResellerNamespace(parent.AdminID))
	}

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
		INSERT INTO reseller_limits (admin_id, max_accounts, data_limit_bytes, node_groups, expires_at, disabled, notes,
		                             parent_id, ancestors, disabled_by_staff)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		adminID, p.MaxAccounts, p.DataLimitBytes, groups, p.ExpiresAt, p.Disabled, p.Notes,
		p.ParentID, ancestors, p.Disabled && !p.ByParent); err != nil {
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
	if err := l.loadReserved(ctx, tx); err != nil {
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
	// Disabled or expired anywhere up the chain blocks the whole subtree: that is how
	// suspending or expiring a parent cascades to its descendants without writing to
	// each of them (and re-enabling the parent undoes it just as cleanly).
	if l.IsDisabled() {
		return ErrResellerDisabled
	}
	if end := l.PlanEnd(); end != nil && !end.After(time.Now()) {
		return ErrResellerExpired
	}
	return nil
}

// checkExpiry: no account may outlive the reseller's plan - the earliest plan end up
// the chain, so a child can never sell past its parent - and when the plan has an end
// date an account must carry an expiry (otherwise it would be unbounded).
func (l *ResellerLimits) checkExpiry(expiry *time.Time) error {
	end := l.PlanEnd()
	if end == nil {
		return nil
	}
	if expiry == nil {
		return ErrResellerExpiryRequired
	}
	if expiry.After(*end) {
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
// What the reseller has already carved out for its children counts as allocated: that
// credit is spoken for, so the reseller's own accounts can only use the rest.
func (l *ResellerLimits) checkCredit(allocated, add int64) error {
	if l.DataLimitBytes == nil || add <= 0 {
		return nil
	}
	if add > *l.DataLimitBytes-allocated-l.ReservedBytes {
		return ErrResellerDataCredit
	}
	return nil
}

// checkAccountRoom reports whether one more own account fits: count of own accounts
// plus the account slots reserved for children must stay under max_accounts.
func (l *ResellerLimits) checkAccountRoom(count int) error {
	if l.MaxAccounts != nil && count+l.ReservedAccounts >= *l.MaxAccounts {
		return ErrResellerAccountLimit
	}
	return nil
}
