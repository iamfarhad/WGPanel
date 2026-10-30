package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sub-resellers (migration 0032). The model, in one place:
//
//   - A reseller may own child resellers up to MaxResellerDepth levels. The tree is a
//     materialised path (reseller_limits.ancestors), see the migration.
//   - CARVING. A child's plan is cut out of its parent's *reservation style*: the
//     child's max_accounts and data_limit_bytes are reserved from the parent the moment
//     they are set, so  own_accounts + sum(direct children's max_accounts) <= max_accounts
//     (same for bytes) is an invariant the parent's row lock protects. Reserving the
//     child's *limit* (not its live usage) is deliberate: the parent never has to look
//     into a child's - or grandchild's - accounts, a child spending its own credit does
//     not touch the parent's row at all, and concurrent creates anywhere in the subtree
//     contend only on their own parent. A parent with no limit (NULL) is unconstrained;
//     a limited parent requires finite limits on every child, otherwise "unlimited
//     under a limit" would be a hole in the invariant.
//   - Node groups of a child must be a subset of its parent's (empty parent list = all
//     groups). A child created with an empty list under a restricted parent inherits the
//     parent's groups rather than silently meaning "all".
//   - Plan expiry: nothing in a subtree may outlive the earliest plan end up the chain
//     (PlanEnd); a child of an expiring parent must itself expire.
//   - Disabled / expired parents block their whole subtree (blockedErr); no per-child
//     write is needed, so re-enabling or renewing the parent undoes it.
//
// Lock order is always parent row, then child row, then node rows.

// childPlan is the plan a child is being given. The check* flags say which dimensions
// the caller is changing: a PATCH that only toggles `disabled` must not fail because the
// parent has since been lowered below an old, unrelated allocation.
type childPlan struct {
	MaxAccounts    *int
	DataLimitBytes *int64
	ExpiresAt      *time.Time
	NodeGroups     []string

	checkAccounts, checkCredit, checkExpiry, checkGroups bool
}

// loadReserved fills ReservedAccounts/ReservedBytes with the direct children's limits.
// Call it with the parent's row already locked, or the numbers can move under you.
func (l *ResellerLimits) loadReserved(ctx context.Context, tx pgx.Tx) error {
	return tx.QueryRow(ctx, `
		SELECT COALESCE(sum(max_accounts), 0)::int, COALESCE(sum(data_limit_bytes), 0)::bigint
		FROM reseller_limits WHERE parent_id = $1`, l.AdminID).Scan(&l.ReservedAccounts, &l.ReservedBytes)
}

// carveChild validates a child plan against this parent and returns the node groups the
// child should end up with (inherited when it asked for "all" under a restricted
// parent). ownCount/ownAlloc are the parent's own live accounts; exclAccounts/exclBytes
// are the child's current reservation when it is being re-planned (0 for a new child).
func (l *ResellerLimits) carveChild(p childPlan, ownCount int, ownAlloc int64, exclAccounts int, exclBytes int64) ([]string, error) {
	if p.checkAccounts && l.MaxAccounts != nil {
		if p.MaxAccounts == nil {
			return nil, ErrChildLimitRequired
		}
		others := int64(l.ReservedAccounts - exclAccounts)
		if int64(ownCount)+others+int64(*p.MaxAccounts) > int64(*l.MaxAccounts) {
			return nil, ErrChildAccountAllocation
		}
	}
	if p.checkCredit && l.DataLimitBytes != nil {
		if p.DataLimitBytes == nil {
			return nil, ErrChildLimitRequired
		}
		// Written as want > limit - used (never used + want) so a huge want can't overflow.
		room := *l.DataLimitBytes - ownAlloc - (l.ReservedBytes - exclBytes)
		if *p.DataLimitBytes > room {
			return nil, ErrChildCreditAllocation
		}
	}
	if p.checkExpiry {
		if end := l.PlanEnd(); end != nil {
			if p.ExpiresAt == nil {
				return nil, ErrChildExpiryRequired
			}
			if p.ExpiresAt.After(*end) {
				return nil, ErrChildExpiryTooLong
			}
		}
	}
	groups := p.NodeGroups
	if groups == nil {
		groups = []string{}
	}
	if p.checkGroups && len(l.NodeGroups) > 0 {
		if len(groups) == 0 {
			return append([]string{}, l.NodeGroups...), nil
		}
		for _, g := range groups {
			if !contains(l.NodeGroups, g) {
				return nil, ErrChildNodeGroupNotAllowed
			}
		}
	}
	return groups, nil
}

// checkChildren is the downward half of the invariant: changing this reseller's own
// limits must not break what it already handed to children. Lowering below what is
// committed (own accounts + children's reservations), limiting a reseller that has
// unlimited children, or narrowing node groups under a child that uses others, is
// ErrResellerCarveConflict - the owner has to shrink the children first. (A reseller
// without children keeps the old rule: lowering below usage is allowed and just blocks
// growth.) l must carry the patched values and its row must be locked.
func (l *ResellerLimits) checkChildren(ctx context.Context, tx pgx.Tx, p ResellerLimitsPatch) error {
	var kids, unlimAcc, unlimBytes int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE max_accounts IS NULL), count(*) FILTER (WHERE data_limit_bytes IS NULL)
		FROM reseller_limits WHERE parent_id = $1`, l.AdminID).Scan(&kids, &unlimAcc, &unlimBytes); err != nil {
		return err
	}
	if kids == 0 {
		return nil
	}
	if err := l.loadReserved(ctx, tx); err != nil {
		return err
	}
	own, alloc, err := resellerAllocatedTx(ctx, tx, ResellerNamespace(l.AdminID), "")
	if err != nil {
		return err
	}
	if p.MaxAccounts.Set && l.MaxAccounts != nil {
		if unlimAcc > 0 || int64(own)+int64(l.ReservedAccounts) > int64(*l.MaxAccounts) {
			return ErrResellerCarveConflict
		}
	}
	if p.DataLimitBytes.Set && l.DataLimitBytes != nil {
		if unlimBytes > 0 || alloc > *l.DataLimitBytes-l.ReservedBytes {
			return ErrResellerCarveConflict
		}
	}
	if p.NodeGroups != nil && len(l.NodeGroups) > 0 {
		rows, err := tx.Query(ctx, `SELECT node_groups FROM reseller_limits WHERE parent_id = $1`, l.AdminID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g []string
			if err := rows.Scan(&g); err != nil {
				return err
			}
			if len(g) == 0 {
				return ErrResellerCarveConflict
			}
			for _, x := range g {
				if !contains(l.NodeGroups, x) {
					return ErrResellerCarveConflict
				}
			}
		}
		return rows.Err()
	}
	return nil
}

type ResellerLimitsPatch struct {
	MaxAccounts    Opt[int]
	DataLimitBytes Opt[int64]
	ExpiresAt      Opt[time.Time]
	NodeGroups     *[]string
	Disabled       *bool
	Notes          *string
	// ByParent is true when a reseller (not staff) applies the patch: it may not clear
	// a disable that staff set (ErrChildLockedByAdmin). The zero value is staff.
	ByParent bool
}

// UpdateResellerLimits applies a patch. Lowering a limit below current usage is
// allowed for a reseller without children: it blocks further growth but never touches
// existing accounts. A child's plan changes are re-checked against its parent
// (carveChild, only for the dimensions the patch touches) and a parent's against its
// children (checkChildren). It takes the parent's row lock first, then its own - the
// same locks quota enforcement takes - so a limit change, a child create and a
// concurrent account create serialise cleanly.
func (s *Store) UpdateResellerLimits(ctx context.Context, adminID string, p ResellerLimitsPatch) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var parentID *string
	if err := tx.QueryRow(ctx, `SELECT parent_id::text FROM reseller_limits WHERE admin_id = $1`, adminID).Scan(&parentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResellerNotFound
		}
		return err
	}
	var parent *ResellerLimits
	if parentID != nil {
		pl, err := lockResellerRow(ctx, tx, *parentID)
		if err != nil {
			return err
		}
		if err := pl.loadReserved(ctx, tx); err != nil {
			return err
		}
		parent = &pl
	}

	var cur ResellerLimits
	if err := scanResellerLimits(tx.QueryRow(ctx,
		`SELECT `+resellerLimitColumns+` FROM reseller_limits l WHERE l.admin_id = $1 FOR UPDATE`, adminID), &cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResellerNotFound
		}
		return err
	}
	old := cur
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
		switch {
		case *p.Disabled:
			cur.Disabled = true
			cur.DisabledByStaff = cur.DisabledByStaff || !p.ByParent
		case cur.DisabledByStaff && p.ByParent:
			return ErrChildLockedByAdmin
		default:
			cur.Disabled, cur.DisabledByStaff = false, false
		}
	}
	if p.Notes != nil {
		cur.Notes = *p.Notes
	}

	if parent != nil && (p.MaxAccounts.Set || p.DataLimitBytes.Set || p.ExpiresAt.Set || p.NodeGroups != nil) {
		own, alloc, err := resellerAllocatedTx(ctx, tx, ResellerNamespace(parent.AdminID), "")
		if err != nil {
			return err
		}
		exclAcc, exclBytes := 0, int64(0)
		if old.MaxAccounts != nil {
			exclAcc = *old.MaxAccounts
		}
		if old.DataLimitBytes != nil {
			exclBytes = *old.DataLimitBytes
		}
		groups, err := parent.carveChild(childPlan{
			MaxAccounts: cur.MaxAccounts, DataLimitBytes: cur.DataLimitBytes, ExpiresAt: cur.ExpiresAt, NodeGroups: cur.NodeGroups,
			checkAccounts: p.MaxAccounts.Set, checkCredit: p.DataLimitBytes.Set, checkExpiry: p.ExpiresAt.Set, checkGroups: p.NodeGroups != nil,
		}, own, alloc, exclAcc, exclBytes)
		if err != nil {
			return err
		}
		cur.NodeGroups = groups
	}
	if err := cur.checkChildren(ctx, tx, p); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE reseller_limits SET max_accounts = $2, data_limit_bytes = $3, node_groups = $4,
			expires_at = $5, disabled = $6, notes = $7, disabled_by_staff = $8, updated_at = now()
		WHERE admin_id = $1`,
		adminID, cur.MaxAccounts, cur.DataLimitBytes, cur.NodeGroups, cur.ExpiresAt, cur.Disabled, cur.Notes, cur.DisabledByStaff); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteReseller removes a reseller that has no children and owns no non-deleted
// accounts. See DeleteResellerTree.
func (s *Store) DeleteReseller(ctx context.Context, adminID string) error {
	_, err := s.DeleteResellerTree(ctx, adminID, false)
	return err
}

// DeleteResellerTree removes the login and limits of a reseller, but only while it -
// and, with cascade, every descendant - owns no non-deleted accounts: those accounts
// would otherwise be orphaned in a namespace nobody can act on except a super admin.
// Without cascade a reseller that still has children is refused (ErrResellerHasChildren)
// so a delete never silently takes a sub-agent's login with it. It locks the reseller
// row, then every descendant row parent-first, which makes the checks race-safe against
// concurrent creates anywhere in the subtree. It returns how many resellers it removed.
func (s *Store) DeleteResellerTree(ctx context.Context, adminID string, cascade bool) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := lockResellerRow(ctx, tx, adminID); err != nil {
		return 0, err
	}
	ns := ResellerNamespace(adminID)
	rows, err := tx.Query(ctx, `
		SELECT admin_id::text FROM reseller_limits WHERE ancestors @> ARRAY[$1::text]
		ORDER BY cardinality(ancestors), admin_id FOR UPDATE`, ns)
	if err != nil {
		return 0, err
	}
	ids := []string{adminID}
	namespaces := []string{ns}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		namespaces = append(namespaces, ResellerNamespace(id))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) > 1 && !cascade {
		return 0, ErrResellerHasChildren
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM accounts WHERE owner_key_namespace = ANY($1::text[]) AND status != 'deleted'`,
		namespaces).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrResellerHasAccounts
	}
	// Deepest first: reseller_limits.parent_id is an immediate foreign key, so a parent's
	// admin row (whose limits row cascades with it) can only go once its children have.
	for i := len(ids) - 1; i >= 0; i-- {
		if _, err := tx.Exec(ctx, `DELETE FROM admins WHERE id = $1 AND role = 'reseller'`, ids[i]); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit(ctx)
}

// nsTreeSQL is "col is param's namespace or belongs to one of its descendants". The
// subquery is uncorrelated, so Postgres evaluates it once into a hash; for an API-key
// or admin namespace it matches nothing. param must be a text expression.
func nsTreeSQL(col, param string) string {
	return `(` + col + ` = ` + param + ` OR ` + col + ` IN (
		SELECT 'reseller:' || rt.admin_id::text FROM reseller_limits rt WHERE rt.ancestors @> ARRAY[` + param + `]))`
}

// namespaceTreeFilter is namespaceFilter widened to the caller's subtree: a reseller (or
// its API key) sees its own accounts and every descendant's. Only reads and the
// suspend/enable pair use it; every other mutation keeps the strict namespaceFilter, so
// a parent can never edit, renew, delete or download the config of a descendant's
// account. Rationale: those writes move the child's allocation or hand out its keys,
// which belong to the child's business; a parent that needs control suspends (reversible,
// touches no accounting) or tightens the child's plan.
const namespaceTreeFilter = `($1::text IS NULL OR owner_key_namespace = $1::text OR owner_key_namespace IN (
	SELECT 'reseller:' || rt.admin_id::text FROM reseller_limits rt WHERE rt.ancestors @> ARRAY[$1::text]))`

// enableLockedSQL ($1 caller namespace, $3 caller-is-reseller): the suspensions a
// reseller caller may not undo through a plain enable - the staff bulk suspend
// ('reseller_suspended') and, for the owning reseller only, its parent's bulk suspend
// ('parent_suspended'; ancestors may lift it).
const enableLockedSQL = `COALESCE($3::bool AND (suspend_reason = 'reseller_suspended'
	OR (suspend_reason = 'parent_suspended' AND owner_key_namespace = $1::text)), false)`

// ListDescendants returns every reseller below rootID (children, grandchildren, ...),
// parents before children, with live usage. Empty for a leaf.
func (s *Store) ListDescendants(ctx context.Context, rootID string) ([]Reseller, error) {
	rows, err := s.pool.Query(ctx,
		resellerSelect+` WHERE l.ancestors @> ARRAY[$1::text] ORDER BY cardinality(l.ancestors), a.created_at`,
		ResellerNamespace(rootID))
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

// GetDescendant returns reseller id only when it lies strictly below rootID; anything
// else (a sibling, an ancestor, the caller itself, an unknown id) is ErrNotDescendant,
// which the API reports as 404 so a probe cannot tell which ids exist.
func (s *Store) GetDescendant(ctx context.Context, rootID, id string) (Reseller, error) {
	r, err := s.GetReseller(ctx, id)
	if errors.Is(err, ErrResellerNotFound) {
		return Reseller{}, ErrNotDescendant
	}
	if err != nil {
		return Reseller{}, err
	}
	if !contains(r.Ancestors, ResellerNamespace(rootID)) {
		return Reseller{}, ErrNotDescendant
	}
	return r, nil
}

// SuspendResellerAccounts is the super-admin bulk suspend: every active account of the
// reseller AND of all its descendants gets the dedicated reason, returning how many it
// changed. Accounts already suspended keep their original reason. Cascading is the
// point: suspending an agent must not leave its sub-agents' customers online.
func (s *Store) SuspendResellerAccounts(ctx context.Context, adminID string) (int, error) {
	return s.suspendSubtree(ctx, adminID, SuspendReasonReseller)
}

// SuspendChildAccounts is the same bulk suspend, applied by a parent reseller to one of
// its descendants' subtree. The reason differs (SuspendReasonParent) so the child cannot
// undo it itself but a staff bulk-reactivate does not resurrect it either.
func (s *Store) SuspendChildAccounts(ctx context.Context, childID string) (int, error) {
	return s.suspendSubtree(ctx, childID, SuspendReasonParent)
}

func (s *Store) suspendSubtree(ctx context.Context, adminID, reason string) (int, error) {
	if _, err := s.GetResellerLimits(ctx, adminID); err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET status = 'suspended', suspend_reason = $2, updated_at = now()
		WHERE `+nsTreeSQL("owner_key_namespace", "$1::text")+` AND status = 'active'`,
		ResellerNamespace(adminID), reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ReactivateResellerAccounts undoes SuspendResellerAccounts over the same subtree - only
// accounts whose reason is 'reseller_suspended'. One that has meanwhile expired or run
// out of quota is not reactivated; it stays suspended under the real reason instead
// (otherwise it would flip back on for a heartbeat or two before enforcement caught it
// again). It applies the same rule as the enforcement in limits.go - same fragments,
// expired before quota_exceeded - to the accounts it touches.
func (s *Store) ReactivateResellerAccounts(ctx context.Context, adminID string) (reactivated, stillSuspended int, err error) {
	return s.reactivateSubtree(ctx, adminID, SuspendReasonReseller)
}

// ReactivateChildAccounts undoes SuspendChildAccounts.
func (s *Store) ReactivateChildAccounts(ctx context.Context, childID string) (reactivated, stillSuspended int, err error) {
	return s.reactivateSubtree(ctx, childID, SuspendReasonParent)
}

func (s *Store) reactivateSubtree(ctx context.Context, adminID, reason string) (reactivated, stillSuspended int, err error) {
	if _, err := s.GetResellerLimits(ctx, adminID); err != nil {
		return 0, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE accounts SET
			status = CASE WHEN `+accountExpiredSQL+` OR `+accountQuotaReachedSQL+`
			              THEN 'suspended' ELSE 'active' END,
			suspend_reason = CASE WHEN `+accountExpiredSQL+` THEN 'expired'
			                      WHEN `+accountQuotaReachedSQL+` THEN 'quota_exceeded'
			                      ELSE NULL END,
			updated_at = now()
		WHERE `+nsTreeSQL("owner_key_namespace", "$1::text")+` AND status = 'suspended' AND suspend_reason = $2
		RETURNING status`,
		ResellerNamespace(adminID), reason)
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
