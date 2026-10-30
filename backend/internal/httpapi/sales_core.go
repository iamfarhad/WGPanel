package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// The order engine: every state change of an order goes through the functions below,
// which pair the SQL transition (store.TransitionOrder - one transaction, row lock,
// edge check) with its audit-log entry and its event. Handlers, gateway callbacks and
// the background worker all call these, so there is exactly one place that decides what
// "paid" or "fulfilled" means.

const (
	// Gateway orders wait an hour for the buyer to pay; a card-to-card order waits a day
	// for the receipt (people pay from a banking app later).
	gatewayOrderTTL = time.Hour
	cardOrderTTL    = 24 * time.Hour
	// A paid order whose fulfilment keeps failing is left for a human after this many tries.
	maxFulfilAttempts = 10
)

// orderActor is the audit actor of automatic transitions.
const orderActorSystem = "system"

func (s *Server) auditOrder(ctx context.Context, actor, action string, o store.Order, extra map[string]any) {
	detail := map[string]any{"kind": o.Kind, "method": o.Method, "amount_toman": o.AmountToman, "status": o.Status}
	for k, v := range extra {
		detail[k] = v
	}
	if err := s.Store.InsertAuditLog(ctx, actor, action, o.ID, detail, ""); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}

// emitOrderEvent publishes a sales event for o. payment.awaiting_review is always
// global (namespace "") because only staff can act on it; the others carry the order's
// owner namespace so the owning reseller or bot key - and only it - sees them.
func (s *Server) emitOrderEvent(ctx context.Context, typ string, o store.Order, extra map[string]any) {
	data := map[string]any{
		"order_id": o.ID, "kind": o.Kind, "status": o.Status, "method": o.Method, "amount_toman": o.AmountToman,
	}
	if o.Contact != "" {
		data["contact"] = o.Contact
	}
	if o.AccountID != nil {
		data["account_id"] = *o.AccountID
		if acc, err := s.Store.GetAccount(ctx, *o.AccountID, nil); err == nil {
			data["label"] = acc.Label
			if acc.ExternalRef != nil {
				data["external_ref"] = *acc.ExternalRef
			}
		}
	}
	if o.AdminID != nil {
		data["reseller_id"] = *o.AdminID
	}
	if o.AdminUsername != nil {
		data["reseller_username"] = *o.AdminUsername
	}
	if o.TrackingCode != nil && typ == events.PaymentAwaitingReview {
		data["tracking_code"] = *o.TrackingCode
	}
	for k, v := range extra {
		data[k] = v
	}
	ns := o.OwnerNamespace
	if typ == events.PaymentAwaitingReview {
		ns = ""
	}
	s.emit(typ, ns, data)
}

// announceAwaitingReview is called once an order has just entered awaiting_review.
func (s *Server) announceAwaitingReview(ctx context.Context, o store.Order, note string) {
	s.auditOrder(ctx, orderActorSystem, "order.awaiting_review", o, map[string]any{"note": note})
	s.emitOrderEvent(ctx, events.PaymentAwaitingReview, o, map[string]any{"note": note})
}

// payOrder moves an order to paid and runs fulfilment. Idempotent: a duplicate gateway
// callback or a second approve finds the order already paid/fulfilled (changed=false)
// and only retries fulfilment, which itself runs at most once.
func (s *Server) payOrder(ctx context.Context, id, actor string, p store.TransitionParams) (store.Order, bool, error) {
	o, changed, err := s.Store.TransitionOrder(ctx, id, store.OrderPaid, p)
	if err != nil {
		return o, false, err
	}
	if changed {
		s.auditOrder(ctx, actor, "order.paid", o, map[string]any{"reviewer": p.Reviewer})
		s.emitOrderEvent(ctx, events.PaymentApproved, o, nil)
	}
	if ferr := s.fulfilOrder(ctx, o.ID); ferr != nil {
		s.Logger.Error("order_fulfil_failed", "order", o.ID, "error", ferr)
	}
	// Reload: fulfilment changed the row.
	if fresh, err := s.Store.GetOrder(ctx, o.ID); err == nil {
		o = fresh
	}
	return o, changed, nil
}

// rejectOrder refuses an order awaiting review.
func (s *Server) rejectOrder(ctx context.Context, id, actor, note string) (store.Order, error) {
	o, changed, err := s.Store.TransitionOrder(ctx, id, store.OrderRejected, store.TransitionParams{Reviewer: actor, Note: note})
	if err != nil {
		return o, err
	}
	if changed {
		s.auditOrder(ctx, actor, "order.rejected", o, map[string]any{"note": note})
		s.emitOrderEvent(ctx, events.PaymentRejected, o, map[string]any{"note": note})
	}
	return o, nil
}

// closeOrder moves a pending order to failed/expired/cancelled with an audit entry.
func (s *Server) closeOrder(ctx context.Context, id, to, actor, note string) (store.Order, error) {
	o, changed, err := s.Store.TransitionOrder(ctx, id, to, store.TransitionParams{Note: note})
	if err == nil && changed {
		s.auditOrder(ctx, actor, "order."+to, o, map[string]any{"note": note})
	}
	return o, err
}

// applySettle turns a provider's verdict into the matching transition.
func (s *Server) applySettle(ctx context.Context, res settleResult) (store.Order, error) {
	id := res.Order.ID
	tp := store.TransitionParams{Data: res.Data, Note: res.Note}
	switch res.Outcome {
	case outcomePaid:
		o, _, err := s.payOrder(ctx, id, orderActorSystem, tp)
		if errors.Is(err, store.ErrIllegalTransition) {
			// e.g. the order was cancelled or rejected before the money showed up: keep
			// it as is but make the late payment visible to a human.
			s.Logger.Warn("late_payment_on_closed_order", "order", id, "status", res.Order.Status)
			s.auditOrder(ctx, orderActorSystem, "order.late_payment", res.Order, map[string]any{"note": res.Note})
			return res.Order, nil
		}
		return o, err
	case outcomeReview:
		o, changed, err := s.Store.TransitionOrder(ctx, id, store.OrderAwaitingReview, tp)
		if err == nil && changed {
			s.announceAwaitingReview(ctx, o, res.Note)
		}
		if errors.Is(err, store.ErrIllegalTransition) {
			return res.Order, nil
		}
		return o, err
	case outcomeFailed:
		o, err := s.closeOrder(ctx, id, store.OrderFailed, orderActorSystem, res.Note)
		if errors.Is(err, store.ErrIllegalTransition) {
			return res.Order, nil // already paid/settled: a failure report must not undo that
		}
		return o, err
	case outcomeExpired:
		o, err := s.closeOrder(ctx, id, store.OrderExpired, orderActorSystem, res.Note)
		if errors.Is(err, store.ErrIllegalTransition) {
			return res.Order, nil
		}
		return o, err
	}
	// ignore: record the gateway status for the admin, no state change.
	if len(res.Data) > 0 {
		_ = s.Store.SetOrderGateway(ctx, id, "", res.Data)
	}
	return res.Order, nil
}

// ---- fulfilment -------------------------------------------------------------------

// planSnapshot decodes the template snapshot stored on an order.
func planSnapshot(o store.Order) (store.PlanTemplate, error) {
	var t store.PlanTemplate
	if len(o.Plan) == 0 {
		return t, errors.New("order has no plan")
	}
	return t, json.Unmarshal(o.Plan, &t)
}

// fulfilOrder delivers what a PAID order bought, at most once. Wallet top-ups are a
// single database transaction (see store.FulfilTopup). Account orders claim the order
// with one atomic UPDATE first; of any number of concurrent callers one gets the claim
// and the rest return immediately. If the delivery fails cleanly the claim is released
// and the error recorded, so the retry sweep (or an admin) can run it again; the order
// stays "paid", never silently lost.
func (s *Server) fulfilOrder(ctx context.Context, id string) error {
	o, err := s.Store.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if o.Status != store.OrderPaid {
		return nil
	}
	if o.Kind == store.KindWalletTopup {
		done, balance, changed, err := s.Store.FulfilTopup(ctx, id)
		if err != nil {
			_ = s.Store.ReleaseFulfilment(ctx, id, err.Error())
			return err
		}
		if changed {
			s.auditOrder(ctx, orderActorSystem, "order.fulfilled", done, map[string]any{"balance_toman": balance})
			s.emitOrderEvent(ctx, events.WalletCredited, done, map[string]any{"balance_toman": balance})
			s.emitOrderEvent(ctx, events.OrderFulfilled, done, nil)
		}
		return nil
	}

	claimed, ok, err := s.Store.ClaimFulfilment(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil // fulfilled, or another caller holds the claim
	}
	accountID, ferr := s.deliverOrder(ctx, claimed)
	if ferr != nil {
		_ = s.Store.ReleaseFulfilment(ctx, id, ferr.Error())
		s.auditOrder(ctx, orderActorSystem, "order.fulfil_failed", claimed, map[string]any{"error": ferr.Error()})
		return ferr
	}
	done, err := s.Store.MarkFulfilled(ctx, id, accountID)
	if err != nil {
		return err
	}
	s.auditOrder(ctx, orderActorSystem, "order.fulfilled", done, nil)
	s.emitOrderEvent(ctx, events.OrderFulfilled, done, nil)
	return nil
}

// deliverOrder performs the account-side effect of a claimed order.
func (s *Server) deliverOrder(ctx context.Context, o store.Order) (*string, error) {
	plan, err := planSnapshot(o)
	if err != nil {
		return nil, err
	}
	switch o.Kind {
	case store.KindAccountRenew:
		if o.AccountID == nil {
			return nil, errors.New("order has no account")
		}
		acc, err := s.Store.GetAccount(ctx, *o.AccountID, nil)
		if err != nil {
			return nil, fmt.Errorf("account: %w", err)
		}
		// The owner's own namespace is used on purpose: a reseller's limits (disabled,
		// plan expiry, data credit) still apply to what is added to its customer.
		if _, err := s.renewByBytes(ctx, acc.ID, acc.OwnerKeyNamespace, plan.DataQuotaBytes, plan.DurationDays); err != nil {
			return nil, err
		}
		return o.AccountID, nil
	case store.KindAccountPurchase:
		ref := "order:" + o.ID
		// Idempotent even across a crash: external_ref is globally unique, so an account
		// created by an earlier, interrupted attempt is found and reused, never doubled.
		if existing, err := s.Store.FindAccountsByExternalRef(ctx, ref, nil); err == nil && len(existing) > 0 {
			id := existing[0].ID
			return &id, nil
		}
		spec, aerr := resolveAccountSpec(createAccountRequest{ExternalRef: &ref}, &plan, time.Now())
		if aerr != nil {
			return nil, aerr
		}
		label := purchaseLabel(o)
		acc, aerr := s.createFromSpecUncharged(ctx, &CallerIdentity{IsAdmin: true, AdminUsername: orderActorSystem}, spec, label)
		if aerr != nil {
			return nil, aerr
		}
		s.emitAccountEvent(events.AccountCreated, acc)
		return &acc.ID, nil
	}
	return nil, fmt.Errorf("unknown order kind %q", o.Kind)
}

// purchaseLabel names a purchased account after the buyer's contact plus a short order id.
func purchaseLabel(o store.Order) string {
	c := strings.TrimSpace(o.Contact)
	if len(c) > 60 {
		c = c[:60]
	}
	if c == "" {
		c = "customer"
	}
	return c + "-" + o.ID[:6]
}

// renewByBytes is renewAccount with the quota given in bytes (a template stores bytes;
// going through GB floats could lose a byte). Same semantics: quota is ADDED, and for an
// on-hold account the days lengthen the hold instead of moving the expiry.
func (s *Server) renewByBytes(ctx context.Context, id string, ns *string, addQuotaBytes *int64, extendDays *int) (store.Account, error) {
	var newExpiry *time.Time
	var addHold *int
	if extendDays != nil {
		existing, err := s.Store.GetAccount(ctx, id, ns)
		if err != nil {
			return store.Account{}, err
		}
		if existing.OnHold() {
			addHold = extendDays
		} else {
			base := time.Now()
			if existing.ExpiryAt != nil && existing.ExpiryAt.After(base) {
				base = *existing.ExpiryAt
			}
			t := base.Add(time.Duration(*extendDays) * 24 * time.Hour)
			newExpiry = &t
		}
	}
	return s.Store.RenewAccountHold(ctx, id, ns, addQuotaBytes, newExpiry, addHold)
}
