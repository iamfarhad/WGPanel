package httpapi

import (
	"context"
	"errors"
	"net/http"

	"wgpanel-api/internal/store"
)

// The wallet hook: where a reseller's account creation and renewal meet the wallet.
// It is the ONLY place the account code knows about money, kept deliberately small:
//
//   - createFromSpec (below) wraps the original, renamed createFromSpecUncharged. Single
//     create, bulk create and public purchases all funnel through it.
//   - renewMaybeTemplate is called by the renew handler and by bulk renew in place of
//     renewAccount; without a template_id it IS renewAccount.
//
// When it applies: the caller is a reseller (session or reseller-owned API key), the
// reseller has "charge wallet" switched on, and the template is a GLOBAL template with
// a price for that reseller (override, else base price less its discount). Then the
// price is debited BEFORE the account is touched; if the account operation fails the
// debit is refunded through the ledger. Debiting first is what makes concurrent creates
// safe - the guarded UPDATE in store.WalletApply admits only as many as the balance
// covers - at the price of a refund row when the create then fails.
//
// Not covered, by design: a reseller creating an account WITHOUT a template, or from its
// own template, or renewing with raw add_quota_gb/extend_days, is not charged - there is
// no price to charge. Those remain governed by the reseller's data credit as before.

func insufficientBalance() *apiError {
	return &apiError{Status: http.StatusPaymentRequired, Code: "insufficient_balance", Message: "the wallet balance is too low for this template: top it up and retry"}
}

// walletCharge is a debit already applied; settle(false) gives it back.
type walletCharge struct {
	s       *Server
	adminID string
	amount  int64
	actor   string
	reason  string
}

// settle is nil-safe so call sites need no "was anything charged" branch.
func (c *walletCharge) settle(ctx context.Context, ok bool) {
	if c == nil || ok {
		return
	}
	if _, err := c.s.Store.WalletApply(context.WithoutCancel(ctx), c.adminID, c.amount, store.LedgerRefund, "refund: "+c.reason, nil, c.actor); err != nil {
		// A failed refund must be loud: the reseller was charged for nothing.
		c.s.Logger.Error("wallet_refund_failed", "admin", c.adminID, "amount", c.amount, "reason", c.reason, "error", err)
	}
}

// salesCharge debits the caller's wallet for templateID when the rules above apply, and
// returns (nil, nil) when they do not.
func (s *Server) salesCharge(ctx context.Context, identity *CallerIdentity, templateID, reason string) (*walletCharge, *apiError) {
	if identity == nil || identity.ResellerID == "" || templateID == "" {
		return nil, nil
	}
	price, charged, err := s.Store.ResellerPriceFor(ctx, identity.ResellerID, templateID)
	if err != nil {
		s.Logger.Error("wallet_price_lookup_failed", "error", err)
		return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not price this template"}
	}
	if !charged {
		return nil, nil
	}
	actor := auditActor(identity)
	if _, err := s.Store.WalletApply(ctx, identity.ResellerID, -price, store.LedgerPurchase, reason, nil, actor); err != nil {
		if errors.Is(err, store.ErrInsufficientBalance) {
			return nil, insufficientBalance()
		}
		s.Logger.Error("wallet_debit_failed", "error", err)
		return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not charge the wallet"}
	}
	return &walletCharge{s: s, adminID: identity.ResellerID, amount: price, actor: actor, reason: reason}, nil
}

// createFromSpec provisions one account, charging a wallet-charging reseller first.
func (s *Server) createFromSpec(ctx context.Context, identity *CallerIdentity, sp accountSpec, label string) (store.Account, *apiError) {
	charge, aerr := s.salesCharge(ctx, identity, sp.TemplateID, "account created: "+label)
	if aerr != nil {
		return store.Account{}, aerr
	}
	acc, aerr := s.createFromSpecUncharged(ctx, identity, sp, label)
	charge.settle(ctx, aerr == nil)
	return acc, aerr
}

// renewMaybeTemplate is renewAccount, or - given a template id - "renew with this plan":
// the template's quota is added and its duration extends the validity, charged to a
// wallet-charging reseller like a create. The error is an *apiError where the
// handlers already expect one.
func (s *Server) renewMaybeTemplate(ctx context.Context, identity *CallerIdentity, id string, ns *string, templateID *string, addQuotaGB *float64, extendDays *int) (store.Account, error) {
	if templateID == nil || *templateID == "" {
		return s.renewAccount(ctx, id, ns, addQuotaGB, extendDays)
	}
	tmpl, aerr := s.loadTemplate(ctx, templateID, ns)
	if aerr != nil {
		return store.Account{}, aerr
	}
	// Fail on an unknown account before any money moves.
	if _, err := s.Store.GetAccount(ctx, id, ns); err != nil {
		return store.Account{}, err
	}
	charge, aerr := s.salesCharge(ctx, identity, tmpl.ID, "account renewed: "+id)
	if aerr != nil {
		return store.Account{}, aerr
	}
	acc, err := s.renewByBytes(ctx, id, ns, tmpl.DataQuotaBytes, tmpl.DurationDays)
	charge.settle(ctx, err == nil)
	return acc, err
}
