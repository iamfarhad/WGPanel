package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The Marzban facade's store needs (docs/marzban-compat.md). Kept in its own file, and
// its setting in its own accessor pair rather than in PanelSettings, so the facade
// does not touch the settings plumbing every other feature also edits.

// GetMarzbanCompatEnabled reports the panel-wide switch (migration 0031, default off).
func (s *Store) GetMarzbanCompatEnabled(ctx context.Context) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `SELECT marzban_compat_enabled FROM panel_settings WHERE id = 1`).Scan(&on)
	return on, err
}

func (s *Store) SetMarzbanCompatEnabled(ctx context.Context, on bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE panel_settings SET marzban_compat_enabled = $1, updated_at = now() WHERE id = 1`, on)
	return err
}

// ListAccountsForCompat returns the non-deleted accounts that carry an external_ref -
// the Marzban "users" - in callerNamespace, oldest first. search (non-empty) keeps only
// refs containing it, case-insensitively; usernames (non-empty) keeps only exact refs.
// Status filtering, sorting and paging happen in the caller because Marzban's status is
// derived (limited/expired/on_hold), not a column.
func (s *Store) ListAccountsForCompat(ctx context.Context, callerNamespace *string, search string, usernames []string) ([]Account, error) {
	if err := s.reconcileLimits(ctx); err != nil {
		return nil, err
	}
	if usernames == nil {
		usernames = []string{}
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts
		 WHERE status != 'deleted' AND external_ref IS NOT NULL AND `+namespaceFilter+`
		   AND ($2::text = '' OR position(lower($2::text) in lower(external_ref)) > 0)
		   AND (cardinality($3::text[]) = 0 OR external_ref = ANY($3::text[]))
		 ORDER BY created_at, id`,
		callerNamespace, search, usernames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := scanAccount(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClearAccountExpiry makes the account never expire - Marzban's "expire": 0 - which
// UpdateAccount cannot express (its nil ExpiryAt means "unchanged"). Like a renewal it
// lifts an 'expired' suspension (other reasons stay), then settles the data cap so the
// returned row matches what a GET would say. A reseller whose plan has an end date may
// not hand out unlimited-validity accounts (ErrResellerExpiryRequired), same as create.
func (s *Store) ClearAccountExpiry(ctx context.Context, id string, callerNamespace *string) (Account, error) {
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
		if err := lim.checkExpiry(nil); err != nil {
			return Account{}, err
		}
	}
	const lift = `(status = 'suspended' AND suspend_reason = 'expired')`
	var a Account
	err = scanAccount(tx.QueryRow(ctx, `
		UPDATE accounts SET
			expiry_at = NULL,
			status = CASE WHEN `+lift+` THEN 'active' ELSE status END,
			suspend_reason = CASE WHEN `+lift+` THEN NULL ELSE suspend_reason END,
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
	if err := settleAccountTx(ctx, tx, &a); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return a, nil
}
