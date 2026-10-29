package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Account limit enforcement: the single place that says when an account is out of
// time or out of data, and what suspending it means.
//
// The rule:
//
//   - expired:       expiry_at is set and not in the future.
//   - quota reached: data_quota_bytes is set (non-NULL) and data_used_bytes >= it.
//     A NULL quota is unlimited and is never over its cap.
//   - Only an 'active' account is ever suspended by enforcement. Any account that is
//     already suspended keeps its reason (manual, abuse_flag, device_limit,
//     reseller_suspended, an earlier expired/quota_exceeded), and a deleted one is never
//     touched.
//   - When an active account is both expired and over its cap, 'expired' wins: lifting
//     a data cap must never revive an expired account, so the more fundamental reason
//     is the one recorded (and the one a later renewal has to resolve first).
//
// Enforcement runs in two places that both use the fragments below:
//
//   - on the traffic path (IngestHeartbeatTelemetry, same transaction as the usage
//     update) for the accounts that just received traffic, so a peer that reaches its
//     cap drops out of the very next ListDesiredPeersForNode - the agents reconcile
//     their WireGuard interface from that list (ReplacePeers), nothing else needed;
//   - at read time (reconcileLimits), like expiry always was, so an account over its cap
//     reads as suspended before the next sample - e.g. after its quota was lowered.
//
// Like expiry enforcement it writes no audit row: it is derived state, not an action
// anyone took.
//
// The fragments use unqualified column names, so they work in any statement whose only
// accounts table is `accounts` (aliased or not).
const (
	accountExpiredSQL      = `(expiry_at IS NOT NULL AND expiry_at <= now())`
	accountQuotaReachedSQL = `(data_quota_bytes IS NOT NULL AND data_used_bytes >= data_quota_bytes)`

	// suspendOverLimitSQL is the one enforcement UPDATE; callers append the condition
	// that says which active accounts to consider.
	suspendOverLimitSQL = `
		UPDATE accounts SET status = 'suspended',
			suspend_reason = CASE WHEN ` + accountExpiredSQL + ` THEN 'expired' ELSE 'quota_exceeded' END,
			updated_at = now()
		WHERE status = 'active'`
)

// ErrAccountQuotaExhausted: re-enabling an account that is still at or over its data cap
// would only get it suspended again on the next sample.
var ErrAccountQuotaExhausted = errors.New("account has used its whole data quota")

// reconcileLimits suspends every active account that is expired or at/over its data cap
// (see the rule above). Run before every read, since there is no background scheduler:
// an account nobody reads and that sends no traffic does not flip until someone looks.
func (s *Store) reconcileLimits(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, suspendOverLimitSQL+` AND (`+accountExpiredSQL+` OR `+accountQuotaReachedSQL+`)`)
	return err
}

// suspendQuotaReachedTx applies the data-cap half of the rule to the given accounts
// inside tx and reports how many it suspended. Expired accounts among them are
// suspended as 'expired' (expired wins).
func suspendQuotaReachedTx(ctx context.Context, tx pgx.Tx, accountIDs []string) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, suspendOverLimitSQL+` AND `+accountQuotaReachedSQL+` AND id = ANY($1::uuid[])`, accountIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// settleAccountTx is the last step of every write that can change what an account's
// limits add up to (a quota PATCH, a renew, a usage reset): it applies the cap to that
// one account and, if that suspended it, re-reads the row into *a so the response the
// caller returns matches what a GET would say right after.
func settleAccountTx(ctx context.Context, tx pgx.Tx, a *Account) error {
	n, err := suspendQuotaReachedTx(ctx, tx, []string{a.ID})
	if err != nil || n == 0 {
		return err
	}
	return scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1`, a.ID), a)
}
