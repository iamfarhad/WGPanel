package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reseller wallet (migration 0030): a balance row plus an append-only ledger, both in
// new tables - the resellers schema is not touched. Balance is integer Toman.

var ErrInsufficientBalance = errors.New("insufficient wallet balance")

const (
	LedgerTopup        = "topup"
	LedgerManualCredit = "manual_credit"
	LedgerManualDebit  = "manual_debit"
	LedgerPurchase     = "purchase"
	LedgerRefund       = "refund"
)

type WalletEntry struct {
	ID           string
	AdminID      string
	Kind         string
	AmountToman  int64 // signed: negative = debit
	BalanceAfter int64
	Reason       string
	OrderID      *string
	Actor        string
	CreatedAt    time.Time
}

// WalletBalance is 0 for a reseller that never had a wallet row.
func (s *Store) WalletBalance(ctx context.Context, adminID string) (int64, error) {
	var b int64
	err := s.pool.QueryRow(ctx, `SELECT balance_toman FROM reseller_wallets WHERE admin_id = $1`, adminID).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return b, err
}

// WalletApply moves the balance by delta (negative = debit) and appends the matching
// ledger row, atomically. The UPDATE carries the "enough money" condition itself, so two
// concurrent debits serialise on the row lock and the second one re-checks the already
// reduced balance: the wallet cannot be overdrawn, whatever the interleaving. A debit
// that does not fit returns ErrInsufficientBalance and changes nothing.
func (s *Store) WalletApply(ctx context.Context, adminID string, delta int64, kind, reason string, orderID *string, actor string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	bal, err := walletApplyTx(ctx, tx, adminID, delta, kind, reason, orderID, actor)
	if err != nil {
		return 0, err
	}
	return bal, tx.Commit(ctx)
}

func walletApplyTx(ctx context.Context, tx pgx.Tx, adminID string, delta int64, kind, reason string, orderID *string, actor string) (int64, error) {
	if delta == 0 {
		return 0, errors.New("wallet amount must not be zero")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO reseller_wallets (admin_id) VALUES ($1) ON CONFLICT DO NOTHING`, adminID); err != nil {
		return 0, err
	}
	var bal int64
	err := tx.QueryRow(ctx, `
		UPDATE reseller_wallets SET balance_toman = balance_toman + $2, updated_at = now()
		WHERE admin_id = $1 AND balance_toman + $2 >= 0
		RETURNING balance_toman`, adminID, delta).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInsufficientBalance
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet_ledger (admin_id, kind, amount_toman, balance_after, reason, order_id, actor)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, adminID, kind, delta, bal, reason, orderID, actor); err != nil {
		return 0, err
	}
	return bal, nil
}

// ListWalletLedger returns a reseller's entries newest first; before (optional) is the
// created_at cursor of the previous page.
func (s *Store) ListWalletLedger(ctx context.Context, adminID string, limit int, before *time.Time) ([]WalletEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, admin_id, kind, amount_toman, balance_after, reason, order_id::text, actor, created_at
		FROM wallet_ledger
		WHERE admin_id = $1 AND ($3::timestamptz IS NULL OR created_at < $3)
		ORDER BY created_at DESC, id DESC LIMIT $2`, adminID, limit, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WalletEntry{}
	for rows.Next() {
		var e WalletEntry
		if err := rows.Scan(&e.ID, &e.AdminID, &e.Kind, &e.AmountToman, &e.BalanceAfter, &e.Reason, &e.OrderID, &e.Actor, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
