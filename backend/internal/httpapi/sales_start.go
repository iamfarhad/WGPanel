package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// Creating an order and starting its payment. Shared by the public subscription page,
// the public /buy page and the reseller wallet top-up.

type orderRequest struct {
	Kind     string
	Method   string
	Amount   int64 // Toman, decided by the server (price table or a validated top-up amount)
	Template *store.PricedTemplate
	Account  *store.Account
	AdminID  *string
	OwnerNS  string
	Contact  string
	IP       string
	// Return builds the absolute URL the buyer lands on after paying.
	Return func(o store.Order) string
}

// startOrder stores the order, then asks the method's provider to prepare the payment.
// A provider failure closes the order as failed (nothing was charged) and is reported as
// 502 payment_gateway_error without the gateway's own message.
func (s *Server) startOrder(ctx context.Context, cfg *paymentConfig, rq orderRequest) (store.Order, *apiError) {
	prov := providerFor(rq.Method)
	if prov == nil || !prov.Enabled(cfg) {
		return store.Order{}, &apiError{Status: http.StatusConflict, Code: "payment_method_unavailable", Message: "this payment method is not available"}
	}
	token, err := newSubscriptionToken()
	if err != nil {
		return store.Order{}, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not create the order"}
	}
	ttl := gatewayOrderTTL
	if rq.Method == store.MethodCard {
		ttl = cardOrderTTL
	}
	n := store.NewOrder{PublicToken: token, Kind: rq.Kind, Method: rq.Method, AmountToman: rq.Amount, AdminID: rq.AdminID,
		OwnerNamespace: rq.OwnerNS, Contact: rq.Contact, BuyerIP: rq.IP, TTL: ttl}
	if rq.Template != nil {
		plan, _ := json.Marshal(rq.Template.Template)
		id := rq.Template.Template.ID
		n.TemplateID, n.Plan = &id, plan
	}
	if rq.Account != nil {
		id := rq.Account.ID
		n.AccountID = &id
	}
	o, err := s.Store.CreateOrder(ctx, n)
	if err != nil {
		s.Logger.Error("create_order_failed", "error", err)
		return store.Order{}, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not create the order"}
	}
	res, err := prov.Start(ctx, cfg, o, rq.Return(o))
	if err != nil {
		s.Logger.Warn("payment_start_failed", "order", o.ID, "method", o.Method, "error", err)
		_, _ = s.closeOrder(ctx, o.ID, store.OrderFailed, orderActorSystem, "could not start the payment")
		return store.Order{}, &apiError{Status: http.StatusBadGateway, Code: "payment_gateway_error", Message: "the payment gateway could not start this payment: try again or pick another method"}
	}
	data := res.Data
	if data == nil {
		data = map[string]any{}
	}
	if res.RedirectURL != "" {
		data["redirect_url"] = res.RedirectURL
	}
	if err := s.Store.SetOrderGateway(ctx, o.ID, res.GatewayRef, data); err != nil {
		s.Logger.Error("set_order_gateway_failed", "error", err)
		return store.Order{}, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not create the order"}
	}
	o, err = s.Store.GetOrder(ctx, o.ID)
	if err != nil {
		return store.Order{}, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not create the order"}
	}
	s.auditOrder(ctx, orderActorSystem, "order.created", o, nil)
	return o, nil
}

// panelBase is the origin of the admin panel (where a reseller is logged in).
func (s *Server) panelBase(ctx context.Context) string {
	if settings, err := s.Store.GetSettings(ctx); err == nil && settings.PanelDomain != nil && *settings.PanelDomain != "" {
		return "https://" + *settings.PanelDomain
	}
	if s.BootPanelDomain != "" {
		return "https://" + s.BootPanelDomain
	}
	return ""
}

// orderLandingURL is where the buyer's browser goes after a gateway callback: back to
// the page that started the order.
func (s *Server) orderLandingURL(ctx context.Context, o store.Order) string {
	switch o.Kind {
	case store.KindWalletTopup:
		return s.panelBase(ctx) + "/dashboard?order=" + url.QueryEscape(o.ID)
	case store.KindAccountPurchase:
		return "/buy?order=" + url.QueryEscape(o.PublicToken)
	}
	if o.AccountID != nil {
		if acc, err := s.Store.GetAccount(ctx, *o.AccountID, nil); err == nil {
			return "/s/" + url.PathEscape(acc.SubscriptionToken) + "?order=" + url.QueryEscape(o.PublicToken)
		}
	}
	return "/"
}

// ---- public representations -------------------------------------------------------

type cardView struct {
	Number string `json:"number"`
	Holder string `json:"holder"`
	Bank   string `json:"bank"`
}

// publicOrderResponse is all a buyer may learn about their order. No ids other than the
// order ref (their capability), no reviewer names, no gateway internals.
type publicOrderResponse struct {
	Ref              string    `json:"ref"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Method           string    `json:"method"`
	AmountToman      int64     `json:"amount_toman"`
	PlanName         string    `json:"plan_name"`
	CreatedAt        string    `json:"created_at"`
	ExpiresAt        string    `json:"expires_at"`
	ReviewNote       string    `json:"review_note,omitempty"`
	Card             *cardView `json:"card,omitempty"`
	RedirectURL      string    `json:"redirect_url,omitempty"`
	ReceiptSubmitted bool      `json:"receipt_submitted"`
	SubscriptionPath string    `json:"subscription_path,omitempty"`
}

func orderPlanName(o store.Order) string {
	if p, err := planSnapshot(o); err == nil {
		return p.Name
	}
	return ""
}

func (s *Server) toPublicOrder(ctx context.Context, o store.Order) publicOrderResponse {
	r := publicOrderResponse{
		Ref: o.PublicToken, Kind: o.Kind, Status: o.Status, Method: o.Method, AmountToman: o.AmountToman,
		PlanName: orderPlanName(o), CreatedAt: o.CreatedAt.Format(time.RFC3339), ExpiresAt: o.ExpiresAt.Format(time.RFC3339),
		ReceiptSubmitted: o.TrackingCode != nil,
	}
	if o.Status == store.OrderRejected {
		r.ReviewNote = o.ReviewNote
	}
	if o.Status == store.OrderPending {
		var gd struct {
			Card        *cardView `json:"card"`
			RedirectURL string    `json:"redirect_url"`
		}
		_ = json.Unmarshal(o.GatewayData, &gd)
		r.Card, r.RedirectURL = gd.Card, gd.RedirectURL
	}
	if o.Status == store.OrderFulfilled && o.Kind == store.KindAccountPurchase && o.AccountID != nil {
		if acc, err := s.Store.GetAccount(ctx, *o.AccountID, nil); err == nil {
			r.SubscriptionPath = "/s/" + url.PathEscape(acc.SubscriptionToken)
		}
	}
	return r
}

// ---- input hygiene ----------------------------------------------------------------

var digitFolder = strings.NewReplacer(
	"۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
	"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9",
)

// asciiDigits turns Persian and Arabic-Indic digits into ASCII ones: buyers type card
// numbers, phone numbers and tracking codes on Persian keyboards.
func asciiDigits(s string) string { return digitFolder.Replace(s) }

var (
	contactRE  = regexp.MustCompile(`^(@?[A-Za-z0-9_]{4,32}|\+?[0-9]{8,15})$`)
	trackingRE = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)
)

// cleanContact validates a Telegram username or phone number. Empty is allowed only
// when optional.
func cleanContact(raw string, optional bool) (string, bool) {
	c := strings.TrimSpace(asciiDigits(raw))
	if c == "" {
		return "", optional
	}
	if !contactRE.MatchString(c) {
		return "", false
	}
	return c, true
}
