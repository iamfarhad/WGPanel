package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// Authenticated order and wallet endpoints: the staff Orders page, the reseller's own
// wallet and top-ups, and the read-only Bot API views. Everything is scoped by the
// caller's namespace: staff see every order; a reseller (session or key) only orders
// whose owner_namespace is its own; a plain bot key only those of the accounts it owns.

type orderResponse struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Method           string    `json:"method"`
	AmountToman      int64     `json:"amount_toman"`
	TemplateID       *string   `json:"template_id"`
	PlanName         string    `json:"plan_name"`
	AccountID        *string   `json:"account_id"`
	AccountLabel     *string   `json:"account_label"`
	ResellerID       *string   `json:"reseller_id"`
	ResellerUsername *string   `json:"reseller_username"`
	Owner            string    `json:"owner"`
	Contact          string    `json:"contact"`
	TrackingCode     *string   `json:"tracking_code"`
	HasReceipt       bool      `json:"has_receipt"`
	Card             *cardView `json:"card,omitempty"`
	GatewayRef       *string   `json:"gateway_ref"`
	Reviewer         string    `json:"reviewer"`
	ReviewNote       string    `json:"review_note"`
	FulfilError      string    `json:"fulfil_error"`
	FulfilAttempts   int       `json:"fulfil_attempts"`
	// FulfilInProgress: claimed but not finished - while recent it is simply running,
	// when old the process died mid-delivery and a staff "retry" is needed.
	FulfilInProgress bool    `json:"fulfil_in_progress"`
	PaidAt           *string `json:"paid_at"`
	FulfilledAt      *string `json:"fulfilled_at"`
	ExpiresAt        string  `json:"expires_at"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func (s *Server) toOrderResponse(o store.Order) orderResponse {
	r := orderResponse{
		ID: o.ID, Kind: o.Kind, Status: o.Status, Method: o.Method, AmountToman: o.AmountToman, TemplateID: o.TemplateID,
		PlanName: orderPlanName(o), AccountID: o.AccountID, AccountLabel: o.AccountLabel, ResellerID: o.AdminID,
		ResellerUsername: o.AdminUsername, Owner: o.OwnerNamespace, Contact: o.Contact, TrackingCode: o.TrackingCode,
		HasReceipt: o.ReceiptSize != nil, GatewayRef: o.GatewayRef, Reviewer: o.Reviewer, ReviewNote: o.ReviewNote,
		FulfilError: o.FulfilError, FulfilAttempts: o.FulfilAttempts, FulfilInProgress: o.FulfilClaimedAt != nil && o.Status == store.OrderPaid,
		PaidAt: rfc3339Ptr(o.PaidAt), FulfilledAt: rfc3339Ptr(o.FulfilledAt),
		ExpiresAt: o.ExpiresAt.Format(time.RFC3339), CreatedAt: o.CreatedAt.Format(time.RFC3339), UpdatedAt: o.UpdatedAt.Format(time.RFC3339),
	}
	var gd struct {
		Card *cardView `json:"card"`
	}
	if json.Unmarshal(o.GatewayData, &gd) == nil {
		r.Card = gd.Card
	}
	return r
}

// scopedOrder loads {id} and applies the caller's namespace: an order outside it is 404.
func (s *Server) scopedOrder(w http.ResponseWriter, r *http.Request) (store.Order, *CallerIdentity, bool) {
	identity, _ := callerIdentityFromContext(r.Context())
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusNotFound, "order_not_found", "no order with that id")
		return store.Order{}, nil, false
	}
	o, err := s.Store.GetOrder(r.Context(), id)
	if err == nil {
		if ns := callerNamespaceArg(identity); ns != nil && o.OwnerNamespace != *ns {
			err = store.ErrOrderNotFound
		}
	}
	if errors.Is(err, store.ErrOrderNotFound) {
		writeJSONError(w, http.StatusNotFound, "order_not_found", "no order with that id")
		return store.Order{}, nil, false
	}
	if err != nil {
		s.Logger.Error("get_order_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the order")
		return store.Order{}, nil, false
	}
	return o, identity, true
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	q := r.URL.Query()
	f := store.OrderFilter{OwnerNamespace: callerNamespaceArg(identity), Status: q.Get("status"), Kind: q.Get("kind")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if f.Status != "" && !validOrderStatus(f.Status) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown status")
		return
	}
	if v := q.Get("account_id"); v != "" {
		if !uuidRE.MatchString(v) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "account_id must be a UUID")
			return
		}
		f.AccountID = v
	}
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "before must be an RFC3339 timestamp")
			return
		}
		f.Before = &t
	}
	orders, err := s.Store.ListOrders(ctx, f)
	if err != nil {
		s.Logger.Error("list_orders_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list orders")
		return
	}
	items := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		items = append(items, s.toOrderResponse(o))
	}
	resp := map[string]any{"items": items}
	if len(orders) == limitOrDefault(f.Limit) {
		resp["next_before"] = orders[len(orders)-1].CreatedAt.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}

func validOrderStatus(s string) bool {
	switch s {
	case store.OrderPending, store.OrderAwaitingReview, store.OrderPaid, store.OrderFulfilled, store.OrderFailed,
		store.OrderExpired, store.OrderRejected, store.OrderCancelled:
		return true
	}
	return false
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, _, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(o))
}

// handleGetOrderReceipt streams the receipt image. It is served inline for the admin UI
// with headers that stop a browser treating it as anything but an image.
func (s *Server) handleGetOrderReceipt(w http.ResponseWriter, r *http.Request) {
	o, _, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	ctype, data, err := s.Store.GetReceipt(r.Context(), o.ID)
	if errors.Is(err, store.ErrReceiptNotFound) {
		writeJSONError(w, http.StatusNotFound, "receipt_unavailable", "this order has no receipt image (none was sent, or it was removed by retention or a restore)")
		return
	}
	if err != nil {
		s.Logger.Error("get_receipt_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the receipt")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}

type reviewRequest struct {
	Note string `json:"note"`
}

func decodeReview(w http.ResponseWriter, r *http.Request) (reviewRequest, bool) {
	var req reviewRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil && err.Error() != "EOF" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
			return req, false
		}
	}
	req.Note = strings.TrimSpace(req.Note)
	if len([]rune(req.Note)) > 300 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "note is at most 300 characters")
		return req, false
	}
	return req, true
}

func writeOrderStateError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, store.ErrIllegalTransition) {
		writeJSONError(w, http.StatusConflict, "invalid_order_state", "the order is not in a state that allows this: "+err.Error())
		return true
	}
	if errors.Is(err, store.ErrOrderNotFound) {
		writeJSONError(w, http.StatusNotFound, "order_not_found", "no order with that id")
		return true
	}
	return false
}

// handleApproveOrder: awaiting_review -> paid -> fulfilled, in one request. A repeated
// approve (double click, two admins) gets 409 and changes nothing: the order is locked
// during the transition, so exactly one caller sees changed=true.
func (s *Server) handleApproveOrder(w http.ResponseWriter, r *http.Request) {
	o, identity, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	req, ok := decodeReview(w, r)
	if !ok {
		return
	}
	if o.Status != store.OrderAwaitingReview {
		writeJSONError(w, http.StatusConflict, "invalid_order_state", "only an order awaiting review can be approved (it is "+o.Status+")")
		return
	}
	updated, changed, err := s.payOrder(r.Context(), o.ID, auditActor(identity), store.TransitionParams{Reviewer: auditActor(identity), Note: req.Note})
	if writeOrderStateError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("approve_order_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not approve the order")
		return
	}
	if !changed {
		writeJSONError(w, http.StatusConflict, "invalid_order_state", "this order was already processed")
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(updated))
}

func (s *Server) handleRejectOrder(w http.ResponseWriter, r *http.Request) {
	o, identity, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	req, ok := decodeReview(w, r)
	if !ok {
		return
	}
	if o.Status != store.OrderAwaitingReview {
		writeJSONError(w, http.StatusConflict, "invalid_order_state", "only an order awaiting review can be rejected (it is "+o.Status+")")
		return
	}
	updated, err := s.rejectOrder(r.Context(), o.ID, auditActor(identity), req.Note)
	if writeOrderStateError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("reject_order_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reject the order")
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(updated))
}

// handleCancelOrder lets staff close an order that is still waiting (pending/awaiting_review).
func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	o, identity, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	updated, err := s.closeOrder(r.Context(), o.ID, store.OrderCancelled, auditActor(identity), "cancelled by staff")
	if writeOrderStateError(w, err) {
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not cancel the order")
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(updated))
}

// handleRetryOrder re-runs fulfilment of a paid order (e.g. no node had capacity when it
// was paid). ?force=true first re-opens a claim that never finished; that is only safe
// when the previous attempt did not deliver, which the staff member must have checked.
func (s *Server) handleRetryOrder(w http.ResponseWriter, r *http.Request) {
	o, identity, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	if o.Status != store.OrderPaid {
		writeJSONError(w, http.StatusConflict, "invalid_order_state", "only a paid order can be retried (it is "+o.Status+")")
		return
	}
	if r.URL.Query().Get("force") == "true" {
		if err := s.Store.ForceReleaseFulfilment(r.Context(), o.ID); err != nil {
			if writeOrderStateError(w, err) {
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not re-open the order")
			return
		}
		s.auditOrder(r.Context(), auditActor(identity), "order.fulfil_forced", o, nil)
	}
	if err := s.fulfilOrder(r.Context(), o.ID); err != nil {
		s.Logger.Warn("order_retry_failed", "order", o.ID, "error", err)
	}
	fresh, err := s.Store.GetOrder(r.Context(), o.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reload the order")
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(fresh))
}

// ---- reseller wallet (self) and Bot API -------------------------------------------

// walletOwner is the reseller whose wallet the caller acts on: a reseller session, or a
// key owned by a reseller. Staff and plain bot keys have no wallet.
func walletOwner(identity *CallerIdentity) (string, bool) {
	if identity.ResellerID != "" {
		return identity.ResellerID, true
	}
	return "", false
}

type walletResponse struct {
	BalanceToman    int64    `json:"balance_toman"`
	ChargeWallet    bool     `json:"charge_wallet"`
	DiscountPercent int      `json:"discount_percent"`
	SelfService     bool     `json:"self_service"`
	Methods         []string `json:"methods"`
}

func (s *Server) handleGetWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	id, ok := walletOwner(identity)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no_wallet", "only reseller accounts and their API keys have a wallet")
		return
	}
	bal, err := s.Store.WalletBalance(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the wallet")
		return
	}
	rp, err := s.Store.GetResellerPricing(ctx, id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the wallet")
		return
	}
	resp := walletResponse{BalanceToman: bal, ChargeWallet: rp.ChargeWallet, DiscountPercent: rp.DiscountPercent, SelfService: rp.SelfService, Methods: []string{}}
	if cfg, err := s.loadPaymentConfig(ctx); err == nil {
		resp.Methods = append(resp.Methods, enabledMethods(cfg)...)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWalletLedger(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	id, ok := walletOwner(identity)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no_wallet", "only reseller accounts and their API keys have a wallet")
		return
	}
	s.writeLedger(w, r, id)
}

type topupRequest struct {
	AmountToman int64  `json:"amount_toman"`
	Method      string `json:"method"`
}

// handleCreateTopup starts a wallet top-up order. The amount is the reseller's own
// choice (bounded) - it is the thing being bought - but it is stored on the order and
// every later check, gateway call and credit uses the stored value.
func (s *Server) handleCreateTopup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	id, ok := walletOwner(identity)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no_wallet", "only reseller accounts and their API keys have a wallet")
		return
	}
	var req topupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.AmountToman < minTopupToman || req.AmountToman > maxTopupToman {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "amount_toman must be between 10,000 and 1,000,000,000")
		return
	}
	cfg, err := s.loadPaymentConfig(ctx)
	if err != nil {
		s.Logger.Error("load_payment_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create the order")
		return
	}
	if n, err := s.Store.CountOpenOrdersForIP(ctx, clientIP(r)); err == nil && n >= maxOpenOrdersPerIPSub {
		writeJSONError(w, http.StatusTooManyRequests, "too_many_open_orders", "you have unpaid orders: finish or wait for them to expire")
		return
	}
	o, aerr := s.startOrder(ctx, cfg, orderRequest{
		Kind: store.KindWalletTopup, Method: req.Method, Amount: req.AmountToman, AdminID: &id,
		OwnerNS: store.ResellerNamespace(id), IP: clientIP(r),
		Return: func(o store.Order) string { return s.orderLandingURL(ctx, o) },
	})
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order": s.toOrderResponse(o), "payment": s.toPublicOrder(ctx, o)})
}

// handleUploadOrderReceipt is the reseller's receipt upload for its own card-to-card
// top-up (staff review the order from the Orders page).
func (s *Server) handleUploadOrderReceipt(w http.ResponseWriter, r *http.Request) {
	o, identity, ok := s.scopedOrder(w, r)
	if !ok {
		return
	}
	if _, has := walletOwner(identity); !has || o.Kind != store.KindWalletTopup {
		writeJSONError(w, http.StatusForbidden, "forbidden", "receipts can only be uploaded for your own wallet top-ups")
		return
	}
	s.acceptReceipt(w, r, o, false)
}
