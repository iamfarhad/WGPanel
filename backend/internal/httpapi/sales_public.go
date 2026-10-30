package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"wgpanel-api/internal/store"
)

// Public (unauthenticated) sales endpoints. They all live under /api/v1/sub/ because
// that is the only API prefix a separate subscription domain exposes (see caddyadmin):
//
//	GET  /api/v1/sub/{token}/shop                    plans + methods for this account
//	POST /api/v1/sub/{token}/orders                  buy a renewal
//	GET  /api/v1/sub/{token}/orders/{ref}            order status
//	POST /api/v1/sub/{token}/orders/{ref}/receipt    card-to-card receipt
//	GET  /api/v1/sub/buy/shop, POST /api/v1/sub/buy/orders, ...   the same for /buy
//	GET  /api/v1/sub/pay/zarinpal                    ZarinPal browser callback
//	POST /api/v1/sub/pay/nowpayments                 NowPayments IPN
//
// "buy" and "pay" cannot collide with a subscription token (48 hex characters).
//
// Authority model. The subscription token is the capability for everything about its
// account: it can only ever act on that account (renewals), and an order ref - itself a
// 192-bit secret - is only honoured together with the token of the account the order
// belongs to. /buy has no account yet, so there the order ref alone is the capability
// and is only honoured for purchase orders. Amounts are never taken from the request.

const (
	maxOpenOrdersPerAccount = 5
	maxOpenOrdersPerIPSub   = 10
	maxOpenOrdersPerIPBuy   = 5
	maxReceiptBytes         = 2 << 20 // 2 MiB: a phone screenshot of a transfer is a few hundred KB
	maxBodyBytes            = 16 << 10
)

// salesGate is a per-IP token bucket for one class of public endpoint, in the style of
// subRateGate (Redis-shared when available, in-memory otherwise).
func (s *Server) salesGate(class string, rate float64, burst int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res := s.takeToken(r.Context(), class+":"+clientIP(r), rate, burst, 1)
		if !res.Allowed {
			writeRateLimited(w, res)
			return
		}
		next(w, r)
	}
}

// Gate for the endpoints that cost a row or a gateway call: order creation and receipt
// upload are slow-refill buckets, callbacks a roomier one (gateways retry in bursts).
func (s *Server) gateReads(next http.HandlerFunc) http.HandlerFunc { return s.subRateGate(next) }
func (s *Server) gateCreate(next http.HandlerFunc) http.HandlerFunc {
	return s.subRateGate(s.salesGate("payorder", 0.2, 8, next))
}
func (s *Server) gateReceipt(next http.HandlerFunc) http.HandlerFunc {
	return s.subRateGate(s.salesGate("payrcpt", 0.1, 6, next))
}
func (s *Server) gateCallback(next http.HandlerFunc) http.HandlerFunc {
	return s.salesGate("paycb", 5, 60, next)
}

type shopPlan struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PriceToman     int64  `json:"price_toman"`
	DataQuotaBytes *int64 `json:"data_quota_bytes"`
	DurationDays   *int   `json:"duration_days"`
	DeviceLimit    *int   `json:"device_limit"`
}

type shopResponse struct {
	Enabled        bool                  `json:"enabled"`
	Plans          []shopPlan            `json:"plans"`
	Methods        []string              `json:"methods"`
	Orders         []publicOrderResponse `json:"orders"`
	SupportContact *string               `json:"support_contact"`
}

func (s *Server) buildShop(r *http.Request, ownerNS *string, acct *store.Account) (shopResponse, bool) {
	ctx := r.Context()
	resp := shopResponse{Plans: []shopPlan{}, Methods: []string{}, Orders: []publicOrderResponse{}}
	cfg, err := s.loadPaymentConfig(ctx)
	if err != nil {
		s.Logger.Error("load_payment_config_failed", "error", err)
		return resp, false
	}
	if acct == nil && !cfg.PublicBuyEnabled {
		return resp, true // /buy switched off: an empty, disabled shop
	}
	plans, err := s.Store.PurchasableTemplates(ctx, ownerNS)
	if err != nil {
		s.Logger.Error("purchasable_templates_failed", "error", err)
		return resp, false
	}
	for _, p := range plans {
		resp.Plans = append(resp.Plans, shopPlan{ID: p.Template.ID, Name: p.Template.Name, PriceToman: p.PriceToman,
			DataQuotaBytes: p.Template.DataQuotaBytes, DurationDays: p.Template.DurationDays, DeviceLimit: p.Template.DeviceLimit})
	}
	resp.Methods = append(resp.Methods, enabledMethods(cfg)...)
	resp.Enabled = len(resp.Plans) > 0 && len(resp.Methods) > 0
	if acct != nil {
		orders, err := s.Store.ListOrders(ctx, store.OrderFilter{AccountID: acct.ID, Limit: 5})
		if err == nil {
			for _, o := range orders {
				resp.Orders = append(resp.Orders, s.toPublicOrder(ctx, o))
			}
		}
	}
	if settings, err := s.Store.GetSettings(ctx); err == nil {
		resp.SupportContact = settings.SupportContact
	}
	return resp, true
}

func (s *Server) handleSubShop(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}
	resp, ok := s.buildShop(r, acct.OwnerKeyNamespace, &acct)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the shop")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBuyShop(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.buildShop(r, nil, nil)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the shop")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

type createPublicOrderRequest struct {
	TemplateID string `json:"template_id"`
	Method     string `json:"method"`
	Contact    string `json:"contact"`
}

func (s *Server) handleSubCreateOrder(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}
	s.createPublicOrder(w, r, &acct)
}

func (s *Server) handleBuyCreateOrder(w http.ResponseWriter, r *http.Request) {
	s.createPublicOrder(w, r, nil)
}

// createPublicOrder serves both flows: acct != nil renews that account, nil buys a new one.
func (s *Server) createPublicOrder(w http.ResponseWriter, r *http.Request, acct *store.Account) {
	ctx := r.Context()
	var req createPublicOrderRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if !uuidRE.MatchString(req.TemplateID) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "template_id must be a UUID")
		return
	}
	contact, ok := cleanContact(req.Contact, acct != nil)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_contact", "enter a Telegram username (e.g. @name) or a phone number")
		return
	}
	cfg, err := s.loadPaymentConfig(ctx)
	if err != nil {
		s.Logger.Error("load_payment_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create the order")
		return
	}
	if acct == nil && !cfg.PublicBuyEnabled {
		writeJSONError(w, http.StatusNotFound, "not_found", "purchases are not open")
		return
	}
	var ownerNS *string
	kind, ownerStr := store.KindAccountPurchase, ""
	if acct != nil {
		ownerNS, kind = acct.OwnerKeyNamespace, store.KindAccountRenew
		if ownerNS != nil {
			ownerStr = *ownerNS
		}
	}
	// The price is read from the database for a template this account (or /buy) is
	// allowed to buy. A template of another namespace is "not found", never priced.
	pt, err := s.Store.GetPurchasableTemplate(ctx, req.TemplateID, ownerNS)
	if errors.Is(err, store.ErrTemplateNotFound) {
		writeJSONError(w, http.StatusNotFound, "template_not_found", "this plan is not for sale")
		return
	}
	if err != nil {
		s.Logger.Error("purchasable_template_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create the order")
		return
	}
	ip := clientIP(r)
	capIP := maxOpenOrdersPerIPSub
	if acct == nil {
		capIP = maxOpenOrdersPerIPBuy
	}
	if n, err := s.Store.CountOpenOrdersForIP(ctx, ip); err == nil && n >= capIP {
		writeJSONError(w, http.StatusTooManyRequests, "too_many_open_orders", "you have unpaid orders: finish or wait for them to expire")
		return
	}
	if acct != nil {
		if n, err := s.Store.CountOpenOrdersForAccount(ctx, acct.ID); err == nil && n >= maxOpenOrdersPerAccount {
			writeJSONError(w, http.StatusTooManyRequests, "too_many_open_orders", "this subscription has unpaid orders: finish or wait for them to expire")
			return
		}
	}
	o, aerr := s.startOrder(ctx, cfg, orderRequest{
		Kind: kind, Method: req.Method, Amount: pt.PriceToman, Template: &pt, Account: acct,
		OwnerNS: ownerStr, Contact: contact, IP: ip,
		Return: func(o store.Order) string { return cfg.CallbackBase + s.orderLandingURL(ctx, o) },
	})
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, s.toPublicOrder(ctx, o))
}

// lookupPublicOrder resolves {ref} for a buyer, enforcing the authority model above. A
// miss charges the caller's unknown-token allowance like a bad subscription token.
func (s *Server) lookupPublicOrder(w http.ResponseWriter, r *http.Request, acct *store.Account) (store.Order, bool) {
	o, err := s.Store.GetOrderByToken(r.Context(), r.PathValue("ref"))
	valid := err == nil
	if valid {
		if acct != nil {
			valid = o.Kind == store.KindAccountRenew && o.AccountID != nil && *o.AccountID == acct.ID
		} else {
			valid = o.Kind == store.KindAccountPurchase
		}
	}
	if !valid {
		if err != nil && !errors.Is(err, store.ErrOrderNotFound) {
			s.Logger.Error("get_order_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the order")
			return store.Order{}, false
		}
		s.noteSubscriptionMiss(r)
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown order")
		return store.Order{}, false
	}
	return o, true
}

func (s *Server) handleSubGetOrder(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}
	o, ok := s.lookupPublicOrder(w, r, &acct)
	if !ok {
		return
	}
	s.respondPublicOrder(w, r, o)
}

func (s *Server) handleBuyGetOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.lookupPublicOrder(w, r, nil)
	if !ok {
		return
	}
	s.respondPublicOrder(w, r, o)
}

func (s *Server) respondPublicOrder(w http.ResponseWriter, r *http.Request, o store.Order) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.toPublicOrder(r.Context(), o))
}

func (s *Server) handleSubReceipt(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}
	o, ok := s.lookupPublicOrder(w, r, &acct)
	if !ok {
		return
	}
	s.acceptReceipt(w, r, o, true)
}

func (s *Server) handleBuyReceipt(w http.ResponseWriter, r *http.Request) {
	o, ok := s.lookupPublicOrder(w, r, nil)
	if !ok {
		return
	}
	s.acceptReceipt(w, r, o, true)
}

// allowedReceiptTypes are decided by sniffing the bytes, never by the client's
// Content-Type or file name: a receipt must be a real raster image, which also keeps
// scriptable formats (SVG, HTML) out of the database and out of the admin's browser.
var allowedReceiptTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// acceptReceipt reads a multipart upload (fields tracking_code, receipt) for a
// card-to-card order and moves it to awaiting_review. public selects the buyer-facing
// response shape.
func (s *Server) acceptReceipt(w http.ResponseWriter, r *http.Request, o store.Order, public bool) {
	ctx := r.Context()
	if o.Method != store.MethodCard {
		writeJSONError(w, http.StatusConflict, "order_not_card", "this order is paid through a gateway, not by receipt")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxReceiptBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "receipt_too_large", "the receipt image must be at most 2 MB")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "expected a multipart form with tracking_code and receipt")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll() //nolint:errcheck
	}
	tracking := strings.TrimSpace(asciiDigits(r.FormValue("tracking_code")))
	if !trackingRE.MatchString(tracking) {
		writeJSONError(w, http.StatusBadRequest, "invalid_tracking_code", "the tracking code must be 4-32 letters, digits or dashes")
		return
	}
	f, _, err := r.FormFile("receipt")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "receipt_required", "attach the receipt image")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not read the receipt")
		return
	}
	if len(data) > maxReceiptBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "receipt_too_large", "the receipt image must be at most 2 MB")
		return
	}
	ctype := http.DetectContentType(data)
	if !allowedReceiptTypes[ctype] {
		writeJSONError(w, http.StatusUnsupportedMediaType, "receipt_type_unsupported", "the receipt must be a JPEG, PNG or WebP image")
		return
	}
	updated, err := s.Store.SubmitReceipt(ctx, o.ID, tracking, ctype, sha256Hex(data), data)
	switch {
	case errors.Is(err, store.ErrDuplicateReceipt):
		writeJSONError(w, http.StatusConflict, "receipt_already_used", "this tracking code or image was already submitted for another order")
		return
	case errors.Is(err, store.ErrIllegalTransition):
		writeJSONError(w, http.StatusConflict, "order_not_awaiting_payment", "a receipt can only be sent for an order that is waiting for payment")
		return
	case errors.Is(err, store.ErrOrderNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown order")
		return
	case err != nil:
		s.Logger.Error("submit_receipt_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save the receipt")
		return
	}
	s.announceAwaitingReview(ctx, updated, "card-to-card receipt submitted")
	if public {
		writeJSON(w, http.StatusOK, s.toPublicOrder(ctx, updated))
		return
	}
	writeJSON(w, http.StatusOK, s.toOrderResponse(updated))
}

// ---- gateway callbacks ------------------------------------------------------------

func (s *Server) loadOrderForCallback(ctx context.Context, id string) (store.Order, error) {
	if !uuidRE.MatchString(id) {
		return store.Order{}, store.ErrOrderNotFound
	}
	return s.Store.GetOrder(ctx, id)
}

// handleZarinpalCallback is where ZarinPal sends the buyer's browser. It always ends in a
// redirect back to the page that started the order; the outcome is read from the order,
// not from the redirect.
func (s *Server) handleZarinpalCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := s.loadPaymentConfig(ctx)
	if err != nil {
		s.Logger.Error("load_payment_config_failed", "error", err)
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	res, err := (zarinpalProvider{}).Settle(ctx, cfg, callbackInput{Query: r.URL.Query()}, s.loadOrderForCallback)
	var rej *errCallbackRejected
	if errors.As(err, &rej) {
		s.Logger.Warn("payment_callback_rejected", "provider", "zarinpal", "code", rej.Code)
		writeJSONError(w, rej.Status, rej.Code, rej.Message)
		return
	}
	o := res.Order
	if err != nil {
		// Verification could not complete (gateway unreachable): the order stays pending,
		// flagged so the worker retries the verification instead of losing the payment.
		s.Logger.Warn("zarinpal_verify_failed", "order", r.URL.Query().Get("order"), "error", err)
		if id := r.URL.Query().Get("order"); uuidRE.MatchString(id) {
			_ = s.Store.SetOrderGateway(ctx, id, "", map[string]any{"callback_ok": true})
			if fresh, gerr := s.Store.GetOrder(ctx, id); gerr == nil {
				o = fresh
			}
		}
	} else if applied, aerr := s.applySettle(ctx, res); aerr != nil {
		s.Logger.Error("apply_settle_failed", "order", o.ID, "error", aerr)
	} else {
		o = applied
	}
	if o.ID == "" {
		http.Error(w, "unknown order", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, s.orderLandingURL(ctx, o), http.StatusSeeOther)
}

// handleNowPaymentsIPN receives NowPayments' signed instant payment notifications.
func (s *Server) handleNowPaymentsIPN(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not read body")
		return
	}
	cfg, err := s.loadPaymentConfig(ctx)
	if err != nil {
		s.Logger.Error("load_payment_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "unavailable")
		return
	}
	if cfg.NowIPNSecret == "" {
		writeJSONError(w, http.StatusNotFound, "not_found", "not configured")
		return
	}
	res, err := (nowPaymentsProvider{}).Settle(ctx, cfg, callbackInput{Header: r.Header, Body: body}, s.loadOrderForCallback)
	var rej *errCallbackRejected
	if errors.As(err, &rej) {
		s.Logger.Warn("payment_callback_rejected", "provider", "nowpayments", "code", rej.Code)
		if rej.Code == "amount_mismatch" || rej.Code == "invoice_mismatch" {
			_ = s.Store.InsertAuditLog(ctx, orderActorSystem, "order.callback_rejected", "nowpayments", map[string]any{"code": rej.Code}, clientIP(r))
		}
		writeJSONError(w, rej.Status, rej.Code, rej.Message)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not process")
		return
	}
	if _, err := s.applySettle(ctx, res); err != nil {
		s.Logger.Error("apply_settle_failed", "order", res.Order.ID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not process")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
