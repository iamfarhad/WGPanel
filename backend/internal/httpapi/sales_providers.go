package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// Payment providers. Each method of payment implements paymentProvider; gateways that
// call the panel back (or that the panel calls to verify) also implement
// callbackProvider. The HTTP layer, the state machine and fulfilment know nothing about
// any particular gateway: adding one means one more provider and one more settings
// block.
//
// Money rules every provider obeys:
//   - the amount sent to a gateway and the amount a callback is checked against are
//     ALWAYS the order's stored amount_toman - nothing the buyer's browser or a callback
//     query string says about money is ever used;
//   - a callback is only believed after it is authenticated (ZarinPal: we call verify
//     ourselves with our merchant id; NowPayments: HMAC-SHA512 signature);
//   - gateway endpoints are fixed constants (no admin- or buyer-supplied URL is ever
//     fetched), so the SSRF guard the webhook package needs is not applicable here.

// gatewayURLs are the outbound gateway origins. A package variable so tests can point
// them at httptest fakes - production never changes it.
type gatewayURLs struct {
	ZarinpalAPI        string
	ZarinpalWeb        string
	ZarinpalSandboxAPI string
	ZarinpalSandboxWeb string
	NowAPI             string
	NowSandboxAPI      string
}

var gateways = gatewayURLs{
	ZarinpalAPI:        "https://payment.zarinpal.com",
	ZarinpalWeb:        "https://payment.zarinpal.com",
	ZarinpalSandboxAPI: "https://sandbox.zarinpal.com",
	ZarinpalSandboxWeb: "https://sandbox.zarinpal.com",
	NowAPI:             "https://api.nowpayments.io",
	NowSandboxAPI:      "https://api-sandbox.nowpayments.io",
}

// gatewayHTTP is the client for every outbound gateway call (tests may replace it).
var gatewayHTTP = &http.Client{Timeout: 15 * time.Second}

// paymentConfig is the decrypted, ready-to-use view of payment settings.
type paymentConfig struct {
	store.PaymentSettings
	ZarinpalMerchant string
	NowAPIKey        string
	NowIPNSecret     string
	// CallbackBase is the public origin gateways call back; empty when none is known.
	CallbackBase string
	Cards        []store.PaymentCard
}

func (s *Server) loadPaymentConfig(ctx context.Context) (*paymentConfig, error) {
	ps, err := s.Store.GetPaymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &paymentConfig{PaymentSettings: ps}
	dec := func(c string) (string, error) {
		if c == "" {
			return "", nil
		}
		return wgkeys.Decrypt(s.AccountKeyEncryptionKey, c)
	}
	if cfg.ZarinpalMerchant, err = dec(ps.ZarinpalMerchantEncrypted); err != nil {
		return nil, err
	}
	if cfg.NowAPIKey, err = dec(ps.NowPaymentsAPIKeyEncrypted); err != nil {
		return nil, err
	}
	if cfg.NowIPNSecret, err = dec(ps.NowPaymentsIPNSecretEncrypted); err != nil {
		return nil, err
	}
	if cfg.Cards, err = s.Store.ListPaymentCards(ctx); err != nil {
		return nil, err
	}
	cfg.CallbackBase = s.paymentCallbackBase(ctx, ps)
	return cfg, nil
}

// paymentCallbackBase picks the origin gateways redirect/call back to: an explicit
// override, else the subscription domain (the public face of the panel, which the
// reverse proxy exposes for /api/v1/sub/* and /s/*), else the panel domain.
func (s *Server) paymentCallbackBase(ctx context.Context, ps store.PaymentSettings) string {
	if ps.CallbackBaseURL != "" {
		return strings.TrimRight(ps.CallbackBaseURL, "/")
	}
	if settings, err := s.Store.GetSettings(ctx); err == nil {
		if b := subscriptionBaseURL(settings); b != nil {
			return *b
		}
		if settings.PanelDomain != nil && *settings.PanelDomain != "" {
			return "https://" + *settings.PanelDomain
		}
	}
	if s.BootPanelDomain != "" {
		return "https://" + s.BootPanelDomain
	}
	return ""
}

func (c *paymentConfig) enabledCards() []store.PaymentCard {
	var out []store.PaymentCard
	for _, card := range c.Cards {
		if card.Enabled {
			out = append(out, card)
		}
	}
	return out
}

// startResult is what a provider hands back for a new order.
type startResult struct {
	GatewayRef  string
	RedirectURL string
	Data        map[string]any
}

type paymentProvider interface {
	Method() string
	Enabled(cfg *paymentConfig) bool
	// Start prepares the payment for a stored order. returnURL is the absolute URL the
	// buyer should land on afterwards, for gateways that take a success/cancel URL.
	Start(ctx context.Context, cfg *paymentConfig, o store.Order, returnURL string) (startResult, error)
}

type settleOutcome int

const (
	outcomeIgnore settleOutcome = iota // authentic but no state change (e.g. NowPayments "waiting")
	outcomePaid
	outcomeFailed
	outcomeExpired
	outcomeReview // needs a human: e.g. partially_paid
)

type settleResult struct {
	Order   store.Order
	Outcome settleOutcome
	Note    string
	Data    map[string]any
}

// errCallbackRejected marks a callback that must be answered with an HTTP error
// (bad signature, amount mismatch, unknown order); Status/Code/Message go to the caller.
type errCallbackRejected struct {
	Status  int
	Code    string
	Message string
}

func (e *errCallbackRejected) Error() string { return e.Code + ": " + e.Message }

type callbackInput struct {
	Query  url.Values
	Header http.Header
	Body   []byte
}

type orderLoader func(ctx context.Context, id string) (store.Order, error)

type callbackProvider interface {
	paymentProvider
	Settle(ctx context.Context, cfg *paymentConfig, in callbackInput, load orderLoader) (settleResult, error)
}

func providerFor(method string) paymentProvider {
	switch method {
	case store.MethodCard:
		return cardProvider{}
	case store.MethodZarinpal:
		return zarinpalProvider{}
	case store.MethodNowPayments:
		return nowPaymentsProvider{}
	}
	return nil
}

// enabledMethods lists the methods currently usable, in display order.
func enabledMethods(cfg *paymentConfig) []string {
	var out []string
	for _, m := range []string{store.MethodCard, store.MethodZarinpal, store.MethodNowPayments} {
		if p := providerFor(m); p != nil && p.Enabled(cfg) {
			out = append(out, m)
		}
	}
	return out
}

// ---- card to card -----------------------------------------------------------------

type cardProvider struct{}

func (cardProvider) Method() string { return store.MethodCard }
func (cardProvider) Enabled(cfg *paymentConfig) bool {
	return cfg.CardEnabled && len(cfg.enabledCards()) > 0
}

// Start picks one enabled card (spreading load across the admin's cards) and records
// it on the order, so the reviewer knows which card the money should have reached.
func (cardProvider) Start(_ context.Context, cfg *paymentConfig, o store.Order, _ string) (startResult, error) {
	cards := cfg.enabledCards()
	if len(cards) == 0 {
		return startResult{}, errors.New("no card configured")
	}
	// Derived from the order id so the choice is stable across retries, no RNG needed.
	idx := 0
	for _, b := range []byte(o.ID) {
		idx = (idx*31 + int(b)) % len(cards)
	}
	c := cards[idx]
	return startResult{Data: map[string]any{"card": map[string]any{"number": c.Number, "holder": c.Holder, "bank": c.Bank}}}, nil
}

// ---- ZarinPal ---------------------------------------------------------------------

type zarinpalProvider struct{}

func (zarinpalProvider) Method() string { return store.MethodZarinpal }
func (zarinpalProvider) Enabled(cfg *paymentConfig) bool {
	return cfg.ZarinpalEnabled && cfg.ZarinpalMerchant != "" && cfg.CallbackBase != ""
}

// ZarinPal REST v4 takes the amount in Rials unless the request says otherwise, and
// verify.json must repeat the SAME amount in the same unit. The panel's prices are
// Toman, so we always convert (x10) and say currency "IRR" explicitly rather than rely
// on the account default or on the optional "IRT" mode - one unambiguous unit end to
// end, and a mismatch is caught by ZarinPal itself (verify refuses a different amount).
const tomanToRial = 10

func zarinpalBases(cfg *paymentConfig) (api, web string) {
	if cfg.ZarinpalSandbox {
		return gateways.ZarinpalSandboxAPI, gateways.ZarinpalSandboxWeb
	}
	return gateways.ZarinpalAPI, gateways.ZarinpalWeb
}

type zarinpalEnvelope struct {
	// data is an object on success and an empty array on error, so keep it raw.
	Data   json.RawMessage `json:"data"`
	Errors json.RawMessage `json:"errors"`
}

func (e zarinpalEnvelope) errorText() string {
	if len(e.Errors) == 0 || string(e.Errors) == "[]" || string(e.Errors) == "null" {
		return ""
	}
	var obj struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Errors, &obj) == nil && obj.Code != 0 {
		return fmt.Sprintf("zarinpal error %d: %s", obj.Code, obj.Message)
	}
	return "zarinpal error: " + string(e.Errors)
}

func (zarinpalProvider) Start(ctx context.Context, cfg *paymentConfig, o store.Order, _ string) (startResult, error) {
	api, web := zarinpalBases(cfg)
	body := map[string]any{
		"merchant_id": cfg.ZarinpalMerchant,
		"amount":      o.AmountToman * tomanToRial,
		"currency":    "IRR",
		"description": "Order " + o.ID[:8],
		// The callback carries the order id; everything else is looked up server-side.
		"callback_url": cfg.CallbackBase + "/api/v1/sub/pay/zarinpal?order=" + url.QueryEscape(o.ID),
	}
	var env zarinpalEnvelope
	if _, err := gatewayPostJSON(ctx, api+"/pg/v4/payment/request.json", nil, body, &env); err != nil {
		return startResult{}, err
	}
	var d struct {
		Code      int    `json:"code"`
		Authority string `json:"authority"`
	}
	if msg := env.errorText(); msg != "" {
		return startResult{}, errors.New(msg)
	}
	if err := json.Unmarshal(env.Data, &d); err != nil || d.Code != 100 || d.Authority == "" {
		return startResult{}, errors.New("zarinpal: unexpected payment request response")
	}
	return startResult{GatewayRef: d.Authority, RedirectURL: web + "/pg/StartPay/" + url.PathEscape(d.Authority)}, nil
}

// Settle handles the buyer's browser coming back from ZarinPal (?Authority=&Status=).
// The query string only says "the buyer says they paid": the money is confirmed by
// calling verify.json ourselves with the stored authority and the stored amount.
func (p zarinpalProvider) Settle(ctx context.Context, cfg *paymentConfig, in callbackInput, load orderLoader) (settleResult, error) {
	id := in.Query.Get("order")
	o, err := load(ctx, id)
	if err != nil || o.Method != store.MethodZarinpal {
		return settleResult{}, &errCallbackRejected{Status: http.StatusNotFound, Code: "order_not_found", Message: "unknown order"}
	}
	res := settleResult{Order: o, Outcome: outcomeIgnore}
	// Anything already beyond pending is a repeated callback: nothing to verify again.
	if o.Status != store.OrderPending && o.Status != store.OrderExpired {
		return res, nil
	}
	if o.GatewayRef == nil || in.Query.Get("Authority") != *o.GatewayRef {
		return settleResult{}, &errCallbackRejected{Status: http.StatusBadRequest, Code: "authority_mismatch", Message: "callback does not belong to this order"}
	}
	if !strings.EqualFold(in.Query.Get("Status"), "OK") {
		res.Outcome, res.Note = outcomeFailed, "buyer cancelled or the payment failed at the gateway"
		return res, nil
	}
	return p.verify(ctx, cfg, o)
}

// verify asks ZarinPal to confirm the payment of o. Codes 100 (verified now) and 101
// (already verified - e.g. our retry after a lost response) both mean the money is ours.
func (zarinpalProvider) verify(ctx context.Context, cfg *paymentConfig, o store.Order) (settleResult, error) {
	api, _ := zarinpalBases(cfg)
	body := map[string]any{
		"merchant_id": cfg.ZarinpalMerchant,
		"amount":      o.AmountToman * tomanToRial,
		"authority":   *o.GatewayRef,
	}
	var env zarinpalEnvelope
	if _, err := gatewayPostJSON(ctx, api+"/pg/v4/payment/verify.json", nil, body, &env); err != nil {
		return settleResult{}, err // transient: the order stays pending and is retried
	}
	res := settleResult{Order: o}
	var d struct {
		Code  int             `json:"code"`
		RefID json.RawMessage `json:"ref_id"`
	}
	_ = json.Unmarshal(env.Data, &d)
	if (d.Code == 100 || d.Code == 101) && env.errorText() == "" {
		res.Outcome = outcomePaid
		res.Data = map[string]any{"ref_id": strings.Trim(string(d.RefID), `"`), "verify_code": d.Code}
		return res, nil
	}
	res.Outcome = outcomeFailed
	res.Note = env.errorText()
	if res.Note == "" {
		res.Note = fmt.Sprintf("zarinpal verify code %d", d.Code)
	}
	return res, nil
}

// ---- NowPayments ------------------------------------------------------------------

type nowPaymentsProvider struct{}

func (nowPaymentsProvider) Method() string { return store.MethodNowPayments }
func (nowPaymentsProvider) Enabled(cfg *paymentConfig) bool {
	return cfg.NowPaymentsEnabled && cfg.NowAPIKey != "" && cfg.NowIPNSecret != "" &&
		cfg.NowPaymentsTomanPerUSD > 0 && cfg.CallbackBase != ""
}

func nowBase(cfg *paymentConfig) string {
	if cfg.NowPaymentsSandbox {
		return gateways.NowSandboxAPI
	}
	return gateways.NowAPI
}

// usdAmount converts Toman to a USD figure with cents, rounding up so the invoice never
// under-collects. The rate is the admin's setting; it is frozen on the order.
func usdAmount(toman, tomanPerUSD int64) float64 {
	if tomanPerUSD <= 0 {
		return 0
	}
	cents := (toman*100 + tomanPerUSD - 1) / tomanPerUSD
	return float64(cents) / 100
}

func (nowPaymentsProvider) Start(ctx context.Context, cfg *paymentConfig, o store.Order, returnURL string) (startResult, error) {
	usd := usdAmount(o.AmountToman, cfg.NowPaymentsTomanPerUSD)
	body := map[string]any{
		"price_amount":      usd,
		"price_currency":    "usd",
		"order_id":          o.ID,
		"order_description": "Order " + o.ID[:8],
		"ipn_callback_url":  cfg.CallbackBase + "/api/v1/sub/pay/nowpayments",
		"success_url":       returnURL,
		"cancel_url":        returnURL,
	}
	var resp struct {
		ID         json.RawMessage `json:"id"`
		InvoiceURL string          `json:"invoice_url"`
	}
	status, err := gatewayPostJSON(ctx, nowBase(cfg)+"/v1/invoice", map[string]string{"x-api-key": cfg.NowAPIKey}, body, &resp)
	if err != nil {
		return startResult{}, err
	}
	if status >= 400 {
		return startResult{}, fmt.Errorf("nowpayments: invoice refused with HTTP %d", status)
	}
	ref := strings.Trim(string(resp.ID), `"`)
	if ref == "" || resp.InvoiceURL == "" {
		return startResult{}, errors.New("nowpayments: unexpected invoice response")
	}
	return startResult{GatewayRef: ref, RedirectURL: resp.InvoiceURL,
		Data: map[string]any{"usd_amount": usd, "toman_per_usd": cfg.NowPaymentsTomanPerUSD}}, nil
}

// canonicalJSON renders v the way NowPayments signs it: JSON with object keys sorted
// at every depth (Go's map encoding sorts keys) and no HTML escaping (JSON.stringify
// leaves < > & alone, Go's default would not).
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// nowSignature is the hex HMAC-SHA512 of the sorted-key JSON, keyed with the IPN secret.
func nowSignature(secret string, payload map[string]any) (string, error) {
	canon, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(canon)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (nowPaymentsProvider) Settle(ctx context.Context, cfg *paymentConfig, in callbackInput, load orderLoader) (settleResult, error) {
	reject := func(status int, code, msg string) (settleResult, error) {
		return settleResult{}, &errCallbackRejected{Status: status, Code: code, Message: msg}
	}
	var payload map[string]any
	if err := json.Unmarshal(in.Body, &payload); err != nil || payload == nil {
		return reject(http.StatusBadRequest, "invalid_request", "malformed JSON body")
	}
	// Authenticate FIRST, before looking anything up: an unsigned or wrongly signed
	// request must learn nothing about our orders.
	want, err := nowSignature(cfg.NowIPNSecret, payload)
	got := strings.ToLower(strings.TrimSpace(in.Header.Get("x-nowpayments-sig")))
	if err != nil || got == "" || !hmac.Equal([]byte(got), []byte(want)) {
		return reject(http.StatusUnauthorized, "invalid_signature", "bad IPN signature")
	}
	orderID, _ := payload["order_id"].(string)
	o, err := load(ctx, orderID)
	if err != nil || o.Method != store.MethodNowPayments {
		return reject(http.StatusNotFound, "order_not_found", "unknown order")
	}
	res := settleResult{Order: o, Outcome: outcomeIgnore}

	// The signed amount must be the amount we asked for, in the currency we asked for.
	var gd struct {
		USD float64 `json:"usd_amount"`
	}
	_ = json.Unmarshal(o.GatewayData, &gd)
	priceAmount, _ := toFloat(payload["price_amount"])
	cur, _ := payload["price_currency"].(string)
	if gd.USD <= 0 || math.Abs(priceAmount-gd.USD) > 0.005 || !strings.EqualFold(cur, "usd") {
		return reject(http.StatusBadRequest, "amount_mismatch", "price does not match the order")
	}
	if inv, ok := payload["invoice_id"]; ok && o.GatewayRef != nil && fmt.Sprint(inv) != *o.GatewayRef {
		// invoice_id may arrive as a JSON number: compare as plain integers.
		if f, ok := toFloat(inv); !ok || strconv.FormatInt(int64(f), 10) != *o.GatewayRef {
			return reject(http.StatusBadRequest, "invoice_mismatch", "invoice does not belong to this order")
		}
	}

	status, _ := payload["payment_status"].(string)
	res.Data = map[string]any{"payment_status": status}
	if pid, ok := payload["payment_id"]; ok {
		res.Data["payment_id"] = fmt.Sprint(pid)
	}
	paid, _ := toFloat(payload["actually_paid"])
	payAmount, _ := toFloat(payload["pay_amount"])
	switch status {
	case "finished":
		// "finished" is NowPayments' final success. Belt and braces: if the figures
		// still show an underpayment, treat it as partial rather than as paid.
		if payAmount > 0 && paid > 0 && paid+1e-9 < payAmount {
			res.Outcome, res.Note = outcomeReview, fmt.Sprintf("underpaid: received %v of %v", paid, payAmount)
		} else {
			res.Outcome = outcomePaid
		}
	case "partially_paid":
		// Never auto-fulfilled and never silently dropped: the buyer sent SOME crypto,
		// so a human decides (top it up, accept it, or reject and refund by hand).
		res.Outcome, res.Note = outcomeReview, fmt.Sprintf("partially paid: received %v of %v", paid, payAmount)
	case "failed", "refunded":
		res.Outcome, res.Note = outcomeFailed, "nowpayments status "+status
	case "expired":
		res.Outcome = outcomeExpired
	default:
		// waiting, confirming, confirmed, sending: not final yet.
	}
	return res, nil
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// gatewayPostJSON POSTs body as JSON and decodes the JSON answer into out, returning the
// HTTP status. Error bodies are not echoed to the buyer (they can name merchant
// details); callers get a generic error and the HTTP layer logs the cause.
func gatewayPostJSON(ctx context.Context, endpoint string, headers map[string]string, body, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := gatewayHTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gateway request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, fmt.Errorf("gateway returned HTTP %d", resp.StatusCode)
	}
	// ZarinPal answers business errors with a 4xx AND a JSON body worth parsing, so a
	// 4xx is not an error here - callers inspect the status and the body.
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("gateway returned HTTP %d with an unreadable body", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
