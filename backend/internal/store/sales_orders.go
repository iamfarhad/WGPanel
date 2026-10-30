package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Orders (migration 0030). The state machine lives here, in SQL, so no caller can move
// an order through an illegal edge and two racing callers cannot both win one edge:
// every transition runs in one transaction that first locks the order row
// (SELECT ... FOR UPDATE) and validates the edge against the status it locked.

const (
	KindAccountRenew    = "account_renew"
	KindAccountPurchase = "account_purchase"
	KindWalletTopup     = "wallet_topup"

	MethodCard        = "card_to_card"
	MethodZarinpal    = "zarinpal"
	MethodNowPayments = "nowpayments"

	OrderPending        = "pending"
	OrderAwaitingReview = "awaiting_review"
	OrderPaid           = "paid"
	OrderFulfilled      = "fulfilled"
	OrderFailed         = "failed"
	OrderExpired        = "expired"
	OrderRejected       = "rejected"
	OrderCancelled      = "cancelled"
)

var (
	ErrOrderNotFound     = errors.New("order not found")
	ErrIllegalTransition = errors.New("illegal order state transition")
	ErrDuplicateReceipt  = errors.New("tracking code or receipt already used by another order")
)

// orderEdges is the whole state machine. Terminal states (fulfilled, failed, rejected,
// cancelled) have no outgoing edge. expired -> paid / awaiting_review exist because
// money can arrive after our timer gave up on the order (a slow bank transfer, a gateway
// callback after the TTL): a verified payment must never be thrown away, it still goes
// to fulfilment or to a human.
var orderEdges = map[string][]string{
	OrderPending:        {OrderAwaitingReview, OrderPaid, OrderFailed, OrderExpired, OrderCancelled},
	OrderAwaitingReview: {OrderPaid, OrderRejected, OrderFailed, OrderCancelled},
	OrderPaid:           {OrderFulfilled},
	OrderExpired:        {OrderPaid, OrderAwaitingReview},
}

// CanTransition reports whether from -> to is a legal edge.
func CanTransition(from, to string) bool {
	for _, t := range orderEdges[from] {
		if t == to {
			return true
		}
	}
	return false
}

type Order struct {
	ID              string
	PublicToken     string
	Kind            string
	Status          string
	Method          string
	AmountToman     int64
	TemplateID      *string
	Plan            json.RawMessage
	AccountID       *string
	AdminID         *string
	OwnerNamespace  string
	Contact         string
	BuyerIP         string
	TrackingCode    *string
	ReceiptType     *string
	ReceiptSize     *int
	GatewayRef      *string
	GatewayData     json.RawMessage
	Reviewer        string
	ReviewNote      string
	FulfilClaimedAt *time.Time
	FulfilAttempts  int
	FulfilError     string
	PaidAt          *time.Time
	FulfilledAt     *time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// Read-only joins for display: the account's label and the reseller's username.
	AccountLabel  *string
	AdminUsername *string
}

const orderColumns = `id, public_token, kind, status, method, amount_toman, template_id::text, plan, account_id::text, admin_id::text,
	owner_namespace, contact, buyer_ip, tracking_code, receipt_type, receipt_size, gateway_ref, gateway_data, reviewer,
	review_note, fulfil_claimed_at, fulfil_attempts, fulfil_error, paid_at, fulfilled_at, expires_at, created_at, updated_at,
	(SELECT label FROM accounts WHERE accounts.id = orders.account_id),
	(SELECT username FROM admins WHERE admins.id = orders.admin_id)`

func scanOrder(row pgx.Row, o *Order) error {
	return row.Scan(&o.ID, &o.PublicToken, &o.Kind, &o.Status, &o.Method, &o.AmountToman, &o.TemplateID, &o.Plan, &o.AccountID, &o.AdminID,
		&o.OwnerNamespace, &o.Contact, &o.BuyerIP, &o.TrackingCode, &o.ReceiptType, &o.ReceiptSize, &o.GatewayRef, &o.GatewayData, &o.Reviewer,
		&o.ReviewNote, &o.FulfilClaimedAt, &o.FulfilAttempts, &o.FulfilError, &o.PaidAt, &o.FulfilledAt, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt,
		&o.AccountLabel, &o.AdminUsername)
}

type NewOrder struct {
	PublicToken    string
	Kind           string
	Method         string
	AmountToman    int64
	TemplateID     *string
	Plan           json.RawMessage
	AccountID      *string
	AdminID        *string
	OwnerNamespace string
	Contact        string
	BuyerIP        string
	GatewayData    map[string]any
	TTL            time.Duration
}

func (s *Store) CreateOrder(ctx context.Context, n NewOrder) (Order, error) {
	gd := []byte(`{}`)
	if len(n.GatewayData) > 0 {
		gd, _ = json.Marshal(n.GatewayData)
	}
	var o Order
	err := scanOrder(s.pool.QueryRow(ctx, `
		INSERT INTO orders (public_token, kind, method, amount_toman, template_id, plan, account_id, admin_id,
		                    owner_namespace, contact, buyer_ip, gateway_data, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now() + make_interval(secs => $13))
		RETURNING `+orderColumns,
		n.PublicToken, n.Kind, n.Method, n.AmountToman, n.TemplateID, n.Plan, n.AccountID, n.AdminID,
		n.OwnerNamespace, n.Contact, n.BuyerIP, gd, n.TTL.Seconds()), &o)
	return o, err
}

func (s *Store) GetOrder(ctx context.Context, id string) (Order, error) {
	var o Order
	err := scanOrder(s.pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1`, id), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrOrderNotFound
	}
	return o, err
}

func (s *Store) GetOrderByToken(ctx context.Context, token string) (Order, error) {
	var o Order
	err := scanOrder(s.pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE public_token = $1`, token), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrOrderNotFound
	}
	return o, err
}

// OrderFilter narrows ListOrders. OwnerNamespace nil = every order (staff); a pointer
// restricts to that namespace ("" would be the staff namespace, and is not used for
// scoping a reseller or key).
type OrderFilter struct {
	OwnerNamespace *string
	Status         string
	Kind           string
	AccountID      string
	Before         *time.Time
	Limit          int
}

func (s *Store) ListOrders(ctx context.Context, f OrderFilter) ([]Order, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+orderColumns+` FROM orders
		WHERE ($1::text IS NULL OR owner_namespace = $1)
		  AND ($2 = '' OR status = $2) AND ($3 = '' OR kind = $3)
		  AND ($4 = '' OR account_id::text = $4)
		  AND ($5::timestamptz IS NULL OR created_at < $5)
		ORDER BY created_at DESC, id DESC LIMIT $6`,
		f.OwnerNamespace, f.Status, f.Kind, f.AccountID, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		var o Order
		if err := scanOrder(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CountOpenOrdersForAccount counts orders still waiting on payment or review - the cap
// that stops a public page being used to pile up orders.
func (s *Store) CountOpenOrdersForAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE account_id = $1 AND status IN ('pending', 'awaiting_review')`, accountID).Scan(&n)
	return n, err
}

// CountOpenOrdersForIP does the same for anonymous purchases by buyer address.
func (s *Store) CountOpenOrdersForIP(ctx context.Context, ip string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE buyer_ip = $1 AND status IN ('pending', 'awaiting_review')`, ip).Scan(&n)
	return n, err
}

// TransitionParams carries what a transition may record along with the status change.
type TransitionParams struct {
	Reviewer   string
	Note       string
	GatewayRef string         // set once (ignored when empty)
	Data       map[string]any // merged into gateway_data
}

// TransitionOrder moves an order to `to` under a row lock. changed=false with a nil
// error means the order was ALREADY in `to` - the idempotent no-op that makes a repeated
// gateway callback or a double click harmless. Any other illegal edge is
// ErrIllegalTransition (wrapped with the two states).
func (s *Store) TransitionOrder(ctx context.Context, id, to string, p TransitionParams) (Order, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	o, changed, err := transitionOrderTx(ctx, tx, id, to, p)
	if err != nil {
		return o, false, err
	}
	return o, changed, tx.Commit(ctx)
}

func transitionOrderTx(ctx context.Context, tx pgx.Tx, id, to string, p TransitionParams) (Order, bool, error) {
	var o Order
	err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1 FOR UPDATE`, id), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, ErrOrderNotFound
	}
	if err != nil {
		return Order{}, false, err
	}
	if o.Status == to {
		return o, false, nil
	}
	if !CanTransition(o.Status, to) {
		return o, false, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, o.Status, to)
	}
	data := []byte(`{}`)
	if len(p.Data) > 0 {
		data, _ = json.Marshal(p.Data)
	}
	err = scanOrder(tx.QueryRow(ctx, `
		UPDATE orders SET status = $2,
			paid_at = CASE WHEN $2 = 'paid' THEN now() ELSE paid_at END,
			reviewer = CASE WHEN $3 <> '' THEN $3 ELSE reviewer END,
			review_note = CASE WHEN $4 <> '' THEN $4 ELSE review_note END,
			gateway_ref = COALESCE(gateway_ref, NULLIF($5, '')),
			gateway_data = gateway_data || $6::jsonb,
			updated_at = now()
		WHERE id = $1 RETURNING `+orderColumns, id, to, p.Reviewer, p.Note, p.GatewayRef, data), &o)
	if err != nil {
		return Order{}, false, err
	}
	return o, true, nil
}

// SetOrderGateway records the gateway-side reference (ZarinPal authority, NowPayments
// invoice id) and extra data after the provider call succeeded.
func (s *Store) SetOrderGateway(ctx context.Context, id, ref string, data map[string]any) error {
	b, _ := json.Marshal(data)
	_, err := s.pool.Exec(ctx, `UPDATE orders SET gateway_ref = NULLIF($2, ''), gateway_data = gateway_data || $3::jsonb, updated_at = now() WHERE id = $1`,
		id, ref, b)
	return err
}

// SubmitReceipt stores a card-to-card receipt and moves pending -> awaiting_review in the
// same transaction. A tracking code or image already backing another live order is
// ErrDuplicateReceipt.
func (s *Store) SubmitReceipt(ctx context.Context, id, tracking, contentType, sha string, data []byte) (Order, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var method string
	if err := tx.QueryRow(ctx, `SELECT method FROM orders WHERE id = $1`, id).Scan(&method); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Order{}, ErrOrderNotFound
		}
		return Order{}, err
	}
	if method != MethodCard {
		return Order{}, fmt.Errorf("%w: order is not card-to-card", ErrIllegalTransition)
	}
	o, changed, err := transitionOrderTx(ctx, tx, id, OrderAwaitingReview, TransitionParams{})
	if err != nil {
		return Order{}, err
	}
	if !changed { // a receipt is already waiting for review: one submission per order
		return Order{}, fmt.Errorf("%w: a receipt was already submitted", ErrIllegalTransition)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_receipts (order_id, content_type, data) VALUES ($1, $2, $3)
		ON CONFLICT (order_id) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data`,
		id, contentType, data); err != nil {
		return Order{}, err
	}
	err = scanOrder(tx.QueryRow(ctx, `
		UPDATE orders SET tracking_code = $2, receipt_type = $3, receipt_size = $4, receipt_sha256 = $5, updated_at = now()
		WHERE id = $1 RETURNING `+orderColumns, id, tracking, contentType, len(data), sha), &o)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Order{}, ErrDuplicateReceipt
	}
	if err != nil {
		return Order{}, err
	}
	return o, tx.Commit(ctx)
}

var ErrReceiptNotFound = errors.New("receipt not found")

func (s *Store) GetReceipt(ctx context.Context, orderID string) (contentType string, data []byte, err error) {
	err = s.pool.QueryRow(ctx, `SELECT content_type, data FROM order_receipts WHERE order_id = $1`, orderID).Scan(&contentType, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrReceiptNotFound
	}
	return
}

// ---- fulfilment -------------------------------------------------------------------

// ClaimFulfilment takes the exclusive right to fulfil a paid order. It is ONE UPDATE
// whose WHERE demands status 'paid' and no earlier claim, so of any number of concurrent
// callers (a double approve, a callback racing the retry sweep) exactly one gets the row
// back; the rest see claimed=false and do nothing. The effect (create / renew an
// account) runs in other transactions, so a claim - not the order's status - is what
// makes it run at most once; ReleaseFulfilment hands the claim back when the effect
// failed cleanly and can be retried.
func (s *Store) ClaimFulfilment(ctx context.Context, id string) (Order, bool, error) {
	var o Order
	err := scanOrder(s.pool.QueryRow(ctx, `
		UPDATE orders SET fulfil_claimed_at = now(), fulfil_attempts = fulfil_attempts + 1, updated_at = now()
		WHERE id = $1 AND status = 'paid' AND fulfil_claimed_at IS NULL
		RETURNING `+orderColumns, id), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	return o, err == nil, err
}

func (s *Store) ReleaseFulfilment(ctx context.Context, id, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE orders SET fulfil_claimed_at = NULL, fulfil_error = $2, updated_at = now() WHERE id = $1 AND status = 'paid'`, id, reason)
	return err
}

// MarkFulfilled finishes a claimed order: paid -> fulfilled, recording the account.
func (s *Store) MarkFulfilled(ctx context.Context, id string, accountID *string) (Order, error) {
	var o Order
	err := scanOrder(s.pool.QueryRow(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now(), fulfil_error = '',
			account_id = COALESCE($2::uuid, account_id), updated_at = now()
		WHERE id = $1 AND status = 'paid' AND fulfil_claimed_at IS NOT NULL
		RETURNING `+orderColumns, id, accountID), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrIllegalTransition
	}
	return o, err
}

// FulfilTopup credits the wallet and marks the order fulfilled in ONE transaction, so a
// top-up is exactly-once without any claim: the order row is locked, a second caller
// finds it already fulfilled (changed=false), and the unique ledger index on the order
// id is the backstop if even that were bypassed.
func (s *Store) FulfilTopup(ctx context.Context, id string) (o Order, balance int64, changed bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, 0, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1 FOR UPDATE`, id), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, 0, false, ErrOrderNotFound
	}
	if err != nil {
		return Order{}, 0, false, err
	}
	if o.Kind != KindWalletTopup || o.AdminID == nil {
		return o, 0, false, fmt.Errorf("%w: not a wallet top-up", ErrIllegalTransition)
	}
	if o.Status == OrderFulfilled {
		return o, 0, false, nil
	}
	if o.Status != OrderPaid {
		return o, 0, false, fmt.Errorf("%w: %s -> fulfilled", ErrIllegalTransition, o.Status)
	}
	balance, err = walletApplyTx(ctx, tx, *o.AdminID, o.AmountToman, LedgerTopup, "wallet top-up via "+o.Method, &o.ID, "order:"+o.ID)
	if err != nil {
		return o, 0, false, err
	}
	err = scanOrder(tx.QueryRow(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now(), fulfil_error = '', fulfil_attempts = fulfil_attempts + 1, updated_at = now()
		WHERE id = $1 RETURNING `+orderColumns, id), &o)
	if err != nil {
		return o, 0, false, err
	}
	return o, balance, true, tx.Commit(ctx)
}

// ---- background upkeep ------------------------------------------------------------

// ExpirePendingOrders flips pending orders past their TTL to expired and returns them.
// A single UPDATE: a concurrent transition either committed first (the row no longer
// matches 'pending') or waits for this statement and then fails its own edge check.
func (s *Store) ExpirePendingOrders(ctx context.Context) ([]Order, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE orders SET status = 'expired', updated_at = now()
		WHERE status = 'pending' AND expires_at < now() RETURNING `+orderColumns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := scanOrder(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PaidUnfulfilled lists paid orders nobody is working on, for the retry sweep.
func (s *Store) PaidUnfulfilled(ctx context.Context, maxAttempts, limit int) ([]Order, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+orderColumns+` FROM orders
		WHERE status = 'paid' AND fulfil_claimed_at IS NULL AND fulfil_attempts < $1
		  AND paid_at < now() - interval '20 seconds'
		ORDER BY paid_at LIMIT $2`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := scanOrder(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PurgeSettledReceipts deletes receipt images of orders settled more than olderThan ago.
func (s *Store) PurgeSettledReceipts(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM order_receipts WHERE order_id IN (
			SELECT id FROM orders WHERE status IN ('fulfilled', 'rejected', 'cancelled', 'failed', 'expired')
			  AND updated_at < now() - make_interval(secs => $1))`, olderThan.Seconds())
	return tag.RowsAffected(), err
}

// ForceReleaseFulfilment lets an admin re-open a claim that never finished (the API
// process died mid-effect). The caller must have checked the effect did not happen.
func (s *Store) ForceReleaseFulfilment(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE orders SET fulfil_claimed_at = NULL, updated_at = now() WHERE id = $1 AND status = 'paid'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrIllegalTransition
	}
	return nil
}
