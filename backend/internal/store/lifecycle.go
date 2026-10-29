package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Account lifecycle (migration 0025): on-hold activation and scheduled usage resets.

const (
	ResetNone  = "none"
	ResetDay   = "day"
	ResetWeek  = "week"
	ResetMonth = "month"
	ResetYear  = "year"
)

var (
	ErrInvalidResetStrategy = errors.New("reset_strategy must be none, day, week, month or year")
	ErrHoldWithExpiry       = errors.New("an on-hold account cannot also have a fixed expiry")
)

// ValidResetStrategy reports whether s is one of the five strategies.
func ValidResetStrategy(s string) bool {
	switch s {
	case ResetNone, ResetDay, ResetWeek, ResetMonth, ResetYear:
		return true
	}
	return false
}

// nextResetSQL is the SQL expression for "the reset date one period after `from`" for
// a strategy parameter/column expression. It yields NULL for 'none'. Calendar months
// and years use PostgreSQL's interval arithmetic (Jan 31 + 1 month = Feb 28/29).
func nextResetSQL(strategy, from string) string {
	return `(` + from + ` + CASE ` + strategy + `::text
		WHEN 'day' THEN interval '1 day'
		WHEN 'week' THEN interval '7 days'
		WHEN 'month' THEN interval '1 month'
		WHEN 'year' THEN interval '1 year' END)`
}

// ActivatedAccount describes one account that left the on-hold state.
type ActivatedAccount struct {
	ID        string
	Label     string
	ExpiryAt  time.Time
	Trigger   string // "first_handshake" | "deadline"
	Namespace *string
}

// activateHeldSQL starts the validity clock of on-hold accounts matching a condition:
// expiry_at = now + hold_duration_days, clamped to the owning reseller's plan expiry (so
// an on-hold account can never outlive the plan it was sold under), and the reset
// schedule (if any) starts counting from the activation.
func activateHeldSQL(cond string) string {
	return `
		UPDATE accounts SET
			expiry_at = LEAST(now() + hold_duration_days * interval '1 day',
				COALESCE((SELECT rl.expires_at FROM reseller_limits rl
				          WHERE accounts.owner_key_namespace = '` + ResellerNamespacePrefix + `' || rl.admin_id::text),
				         'infinity'::timestamptz)),
			hold_duration_days = NULL,
			hold_deadline_at = NULL,
			activated_at = now(),
			next_reset_at = CASE WHEN reset_strategy = 'none' THEN NULL ELSE ` + nextResetSQL("reset_strategy", "now()") + ` END,
			updated_at = now()
		WHERE hold_duration_days IS NOT NULL AND status != 'deleted' AND ` + cond + `
		RETURNING id, label, expiry_at, owner_key_namespace`
}

func scanActivated(rows pgx.Rows, trigger string) ([]ActivatedAccount, error) {
	defer rows.Close()
	var out []ActivatedAccount
	for rows.Next() {
		a := ActivatedAccount{Trigger: trigger}
		if err := rows.Scan(&a.ID, &a.Label, &a.ExpiryAt, &a.Namespace); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// activateOnHandshakeTx is called from heartbeat ingest, in the ingest transaction, for
// the accounts whose peers just reported a handshake: the first one starts the clock.
func activateOnHandshakeTx(ctx context.Context, tx pgx.Tx, accountIDs []string) ([]ActivatedAccount, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, activateHeldSQL(`id = ANY($1::uuid[])`), accountIDs)
	if err != nil {
		return nil, err
	}
	return scanActivated(rows, "first_handshake")
}

// ActivateOverdueHolds activates every on-hold account whose hold_deadline_at has
// passed without a handshake (decision: "activate anyway", not "expire" - the customer
// paid for the duration, an unused account should not silently vanish; the clock simply
// starts at the deadline sweep).
func (s *Store) ActivateOverdueHolds(ctx context.Context) ([]ActivatedAccount, error) {
	rows, err := s.pool.Query(ctx, activateHeldSQL(`hold_deadline_at IS NOT NULL AND hold_deadline_at <= now()`))
	if err != nil {
		return nil, err
	}
	return scanActivated(rows, "deadline")
}

// ScheduledReset is one usage reset performed by the sweep.
type ScheduledReset struct {
	AccountID         string
	Label             string
	Strategy          string
	PreviousUsedBytes int64
	NextResetAt       time.Time
}

// ResetDueUsage resets data_used_bytes of up to `limit` accounts whose next_reset_at has
// passed, using exactly the same logic as the manual reset (resetUsageTx: counters,
// quota_exceeded reactivation, cap settlement), then moves next_reset_at forward.
//
// Credit: a reseller's data credit counts allocated quota (data_quota_bytes), which a
// reset never touches - so a periodic reset cannot double-charge it. This is also why
// the manual-reset refusal for credit-limited resellers (ErrResellerResetNotAllowed)
// does not apply here: that refusal is a policy for reseller-initiated resets, this one
// is the plan the account was sold with. Accounts still on hold are skipped (their
// schedule starts at activation). Rows are claimed with FOR UPDATE SKIP LOCKED so two
// API replicas never reset the same account twice.
func (s *Store) ResetDueUsage(ctx context.Context, limit int) ([]ScheduledReset, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT id, label, reset_strategy, data_used_bytes FROM accounts
		WHERE next_reset_at IS NOT NULL AND next_reset_at <= now() AND reset_strategy != 'none'
		  AND status != 'deleted' AND hold_duration_days IS NULL
		ORDER BY next_reset_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var due []ScheduledReset
	for rows.Next() {
		var r ScheduledReset
		if err := rows.Scan(&r.AccountID, &r.Label, &r.Strategy, &r.PreviousUsedBytes); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range due {
		if _, err := resetUsageTx(ctx, tx, due[i].AccountID, nil); err != nil {
			return nil, err
		}
		// Advance from the previous due date (no drift); if the sweep was down for
		// longer than a period, restart from now instead of firing repeatedly.
		err := tx.QueryRow(ctx, `
			UPDATE accounts SET next_reset_at = CASE
				WHEN `+nextResetSQL("reset_strategy", "next_reset_at")+` > now() THEN `+nextResetSQL("reset_strategy", "next_reset_at")+`
				ELSE `+nextResetSQL("reset_strategy", "now()")+` END
			WHERE id = $1 RETURNING next_reset_at`, due[i].AccountID).Scan(&due[i].NextResetAt)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return due, nil
}
