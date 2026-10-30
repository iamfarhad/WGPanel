package store

import (
	"context"
	"time"
)

// AutoDeletedAccount describes one account removed by the auto-cleanup sweep, for the
// audit row and the account.deleted event the caller emits.
type AutoDeletedAccount struct {
	ID          string
	Label       string
	ExternalRef *string
	Namespace   string // owner namespace, "" for staff-created accounts
	Reason      string // the suspend_reason it was cleaned up for: expired | quota_exceeded
	SuspendedAt time.Time
}

// AutoDeleteDeadAccounts soft-deletes (the same state change as DELETE /accounts/{id}:
// status 'deleted', IP held for ipHoldAfterDelete) up to limit accounts that have been
// suspended as 'expired' or 'quota_exceeded' for at least `days` days.
//
// The selection is intentionally narrow, and this is the safety property of the feature:
//   - status must still be 'suspended' with one of exactly those two reasons - a
//     manually suspended, abuse-flagged, device-limit, torrent or reseller-suspended
//     account is never a candidate, however old;
//   - the clock is accounts.suspended_at (trigger-maintained), not updated_at, so
//     editing a note or label cannot postpone deletion nor can anything but a
//     suspension start it;
//   - a renewed/enabled account stops being 'suspended' and so drops out on its own.
//
// FOR UPDATE SKIP LOCKED keeps two API replicas (or a sweep racing a manual enable)
// from acting on the same row (suspended_at is read in the CTE because the trigger
// clears it on the row once it is deleted).
func (s *Store) AutoDeleteDeadAccounts(ctx context.Context, days, limit int) ([]AutoDeletedAccount, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id, suspended_at FROM accounts
			WHERE status = 'suspended' AND suspend_reason IN ('expired', 'quota_exceeded')
			  AND suspended_at IS NOT NULL AND suspended_at <= now() - make_interval(days => $1)
			ORDER BY suspended_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE accounts a SET status = 'deleted', ip_release_at = now() + $3, updated_at = now()
		FROM due WHERE a.id = due.id
		RETURNING a.id, a.label, a.external_ref, COALESCE(a.owner_key_namespace, ''), a.suspend_reason, due.suspended_at
	`, days, limit, ipHoldAfterDelete)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AutoDeletedAccount
	for rows.Next() {
		var d AutoDeletedAccount
		var suspendedAt *time.Time
		var reason *string
		if err := rows.Scan(&d.ID, &d.Label, &d.ExternalRef, &d.Namespace, &reason, &suspendedAt); err != nil {
			return nil, err
		}
		if reason != nil {
			d.Reason = *reason
		}
		if suspendedAt != nil {
			d.SuspendedAt = *suspendedAt
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
