package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// Staff-facing sales configuration: payment settings, template prices, per-reseller
// pricing and manual wallet adjustments.

const (
	maxPriceToman   = 10_000_000_000 // 10 billion Toman: far above any real plan, far below int64 overflow when multiplied by 10
	maxCards        = 10
	minTopupToman   = 10_000
	maxTopupToman   = 1_000_000_000
	maxWalletAdjust = 1_000_000_000
)

// ---- payment settings -------------------------------------------------------------

type paymentCardBody struct {
	ID      string `json:"id,omitempty"`
	Number  string `json:"number"`
	Holder  string `json:"holder"`
	Bank    string `json:"bank"`
	Enabled bool   `json:"enabled"`
}

type paymentSettingsBody struct {
	Card struct {
		Enabled bool              `json:"enabled"`
		Cards   []paymentCardBody `json:"cards"`
	} `json:"card"`
	Zarinpal struct {
		Enabled       bool   `json:"enabled"`
		Sandbox       bool   `json:"sandbox"`
		MerchantID    string `json:"merchant_id,omitempty"` // write-only; empty on update = keep
		HasMerchantID bool   `json:"has_merchant_id"`
	} `json:"zarinpal"`
	NowPayments struct {
		Enabled      bool   `json:"enabled"`
		Sandbox      bool   `json:"sandbox"`
		APIKey       string `json:"api_key,omitempty"`    // write-only
		IPNSecret    string `json:"ipn_secret,omitempty"` // write-only
		HasAPIKey    bool   `json:"has_api_key"`
		HasIPNSecret bool   `json:"has_ipn_secret"`
		TomanPerUSD  int64  `json:"toman_per_usd"`
	} `json:"nowpayments"`
	CallbackBaseURL  string `json:"callback_base_url"`
	PublicBuyEnabled bool   `json:"public_buy_enabled"`
	// Read-only: where gateways will call, so the operator can paste the IPN URL into
	// the NowPayments dashboard and whitelist the ZarinPal callback.
	EffectiveCallbackBase string `json:"effective_callback_base"`
	ZarinpalCallbackURL   string `json:"zarinpal_callback_url"`
	NowPaymentsIPNURL     string `json:"nowpayments_ipn_url"`
}

func (s *Server) paymentSettingsResponse(r *http.Request) (paymentSettingsBody, error) {
	ctx := r.Context()
	cfg, err := s.loadPaymentConfigNoDecrypt(ctx)
	if err != nil {
		return paymentSettingsBody{}, err
	}
	var b paymentSettingsBody
	b.Card.Enabled = cfg.CardEnabled
	b.Card.Cards = []paymentCardBody{}
	for _, c := range cfg.Cards {
		b.Card.Cards = append(b.Card.Cards, paymentCardBody{ID: c.ID, Number: c.Number, Holder: c.Holder, Bank: c.Bank, Enabled: c.Enabled})
	}
	b.Zarinpal.Enabled, b.Zarinpal.Sandbox, b.Zarinpal.HasMerchantID = cfg.ZarinpalEnabled, cfg.ZarinpalSandbox, cfg.ZarinpalMerchantEncrypted != ""
	b.NowPayments.Enabled, b.NowPayments.Sandbox = cfg.NowPaymentsEnabled, cfg.NowPaymentsSandbox
	b.NowPayments.HasAPIKey, b.NowPayments.HasIPNSecret = cfg.NowPaymentsAPIKeyEncrypted != "", cfg.NowPaymentsIPNSecretEncrypted != ""
	b.NowPayments.TomanPerUSD = cfg.NowPaymentsTomanPerUSD
	b.CallbackBaseURL, b.PublicBuyEnabled = cfg.CallbackBaseURL, cfg.PublicBuyEnabled
	b.EffectiveCallbackBase = cfg.CallbackBase
	if cfg.CallbackBase != "" {
		b.ZarinpalCallbackURL = cfg.CallbackBase + "/api/v1/sub/pay/zarinpal"
		b.NowPaymentsIPNURL = cfg.CallbackBase + "/api/v1/sub/pay/nowpayments"
	}
	return b, nil
}

// loadPaymentConfigNoDecrypt is the settings view without touching ciphertext, so the
// settings page still loads (and can be used to re-enter secrets) after the encryption
// key changed.
func (s *Server) loadPaymentConfigNoDecrypt(ctx context.Context) (*paymentConfig, error) {
	ps, err := s.Store.GetPaymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &paymentConfig{PaymentSettings: ps, CallbackBase: s.paymentCallbackBase(ctx, ps)}
	cfg.Cards, err = s.Store.ListPaymentCards(ctx)
	return cfg, err
}

func (s *Server) handleGetPaymentSettings(w http.ResponseWriter, r *http.Request) {
	b, err := s.paymentSettingsResponse(r)
	if err != nil {
		s.Logger.Error("get_payment_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load payment settings")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

var cardNumberRE = regexp.MustCompile(`^[0-9]{12,19}$`)

func (s *Server) handleUpdatePaymentSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req paymentSettingsBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	bad := func(msg string) { writeJSONError(w, http.StatusBadRequest, "invalid_request", msg) }
	cur, err := s.Store.GetPaymentSettings(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load payment settings")
		return
	}

	// Cards: normalise digits, bound sizes.
	if len(req.Card.Cards) > maxCards {
		bad("at most 10 cards")
		return
	}
	cards := make([]store.PaymentCard, 0, len(req.Card.Cards))
	anyEnabledCard := false
	for _, c := range req.Card.Cards {
		num := strings.NewReplacer(" ", "", "-", "").Replace(asciiDigits(c.Number))
		holder, bank := strings.TrimSpace(c.Holder), strings.TrimSpace(c.Bank)
		if !cardNumberRE.MatchString(num) {
			bad("a card number must be 12 to 19 digits")
			return
		}
		if len([]rune(holder)) > 80 || len([]rune(bank)) > 40 {
			bad("card holder (80) or bank (40) is too long")
			return
		}
		anyEnabledCard = anyEnabledCard || c.Enabled
		cards = append(cards, store.PaymentCard{Number: num, Holder: holder, Bank: bank, Enabled: c.Enabled})
	}
	if req.Card.Enabled && !anyEnabledCard {
		bad("card-to-card needs at least one enabled card")
		return
	}

	enc := func(plain, current string) (string, error) {
		if plain == "" {
			return current, nil
		}
		return wgkeys.Encrypt(s.AccountKeyEncryptionKey, plain)
	}
	next := cur
	next.CardEnabled = req.Card.Enabled
	next.ZarinpalEnabled, next.ZarinpalSandbox = req.Zarinpal.Enabled, req.Zarinpal.Sandbox
	next.NowPaymentsEnabled, next.NowPaymentsSandbox = req.NowPayments.Enabled, req.NowPayments.Sandbox
	next.NowPaymentsTomanPerUSD = req.NowPayments.TomanPerUSD
	next.PublicBuyEnabled = req.PublicBuyEnabled

	if m := strings.TrimSpace(req.Zarinpal.MerchantID); m != "" && !uuidRE.MatchString(m) {
		bad("the ZarinPal merchant id must be a UUID (36 characters)")
		return
	}
	if next.ZarinpalMerchantEncrypted, err = enc(strings.TrimSpace(req.Zarinpal.MerchantID), cur.ZarinpalMerchantEncrypted); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not encrypt the merchant id")
		return
	}
	if next.NowPaymentsAPIKeyEncrypted, err = enc(strings.TrimSpace(req.NowPayments.APIKey), cur.NowPaymentsAPIKeyEncrypted); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not encrypt the API key")
		return
	}
	if next.NowPaymentsIPNSecretEncrypted, err = enc(strings.TrimSpace(req.NowPayments.IPNSecret), cur.NowPaymentsIPNSecretEncrypted); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not encrypt the IPN secret")
		return
	}
	if next.ZarinpalEnabled && next.ZarinpalMerchantEncrypted == "" {
		bad("ZarinPal needs a merchant id")
		return
	}
	if next.NowPaymentsTomanPerUSD < 0 || next.NowPaymentsTomanPerUSD > 100_000_000 {
		bad("toman_per_usd is out of range")
		return
	}
	if next.NowPaymentsEnabled && (next.NowPaymentsAPIKeyEncrypted == "" || next.NowPaymentsIPNSecretEncrypted == "" || next.NowPaymentsTomanPerUSD == 0) {
		bad("NowPayments needs an API key, an IPN secret and the Toman-per-USD rate")
		return
	}

	// The callback origin must be a bare https origin: gateways redirect real buyers there.
	base := strings.TrimRight(strings.TrimSpace(req.CallbackBaseURL), "/")
	if base != "" {
		u, perr := url.Parse(base)
		if perr != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
			bad("callback_base_url must be a bare https origin like https://pay.example.com")
			return
		}
	}
	next.CallbackBaseURL = base

	if err := s.Store.SavePaymentSettings(ctx, next); err != nil {
		s.Logger.Error("save_payment_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save payment settings")
		return
	}
	if err := s.Store.ReplacePaymentCards(ctx, cards); err != nil {
		s.Logger.Error("save_payment_cards_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save the cards")
		return
	}
	identity, _ := callerIdentityFromContext(ctx)
	// Never log secrets - only which switches are on.
	_ = s.Store.InsertAuditLog(ctx, auditActor(identity), "payments.settings_updated", "payment_settings", map[string]any{
		"card": next.CardEnabled, "cards": len(cards), "zarinpal": next.ZarinpalEnabled, "nowpayments": next.NowPaymentsEnabled,
		"public_buy": next.PublicBuyEnabled,
	}, r.RemoteAddr)
	s.handleGetPaymentSettings(w, r)
}

// ---- template prices --------------------------------------------------------------

type templatePriceBody struct {
	TemplateID  string `json:"template_id,omitempty"`
	PriceToman  int64  `json:"price_toman"`
	Purchasable bool   `json:"purchasable"`
}

func (s *Server) handleListTemplatePrices(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	rows, err := s.Store.ListTemplatePrices(r.Context(), callerNamespaceArg(identity))
	if err != nil {
		s.Logger.Error("list_template_prices_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load prices")
		return
	}
	items := make([]templatePriceBody, 0, len(rows))
	for _, p := range rows {
		items = append(items, templatePriceBody{TemplateID: p.TemplateID, PriceToman: p.PriceToman, Purchasable: p.Purchasable})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSetTemplatePrice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "template id must be a UUID")
		return
	}
	var req templatePriceBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.PriceToman < 0 || req.PriceToman > maxPriceToman {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "price_toman must be a whole number between 0 and 10,000,000,000")
		return
	}
	if req.Purchasable && req.PriceToman == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "a purchasable template needs a price above zero")
		return
	}
	identity, _ := callerIdentityFromContext(ctx)
	p, err := s.Store.SetTemplatePrice(ctx, id, callerNamespaceArg(identity), req.PriceToman, req.Purchasable)
	if errors.Is(err, store.ErrTemplateNotFound) {
		writeJSONError(w, http.StatusNotFound, "template_not_found", "no template with that id")
		return
	}
	if err != nil {
		s.Logger.Error("set_template_price_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save the price")
		return
	}
	_ = s.Store.InsertAuditLog(ctx, auditActor(identity), "template.price_set", id, map[string]any{"price_toman": p.PriceToman, "purchasable": p.Purchasable}, r.RemoteAddr)
	writeJSON(w, http.StatusOK, templatePriceBody{TemplateID: p.TemplateID, PriceToman: p.PriceToman, Purchasable: p.Purchasable})
}

// ---- per-reseller pricing and wallet (super admin) --------------------------------

type resellerOverrideBody struct {
	TemplateID string `json:"template_id"`
	PriceToman int64  `json:"price_toman"`
}

type resellerSalesBody struct {
	DiscountPercent int                    `json:"discount_percent"`
	ChargeWallet    bool                   `json:"charge_wallet"`
	SelfService     bool                   `json:"self_service"`
	Overrides       []resellerOverrideBody `json:"overrides"`
	BalanceToman    int64                  `json:"balance_toman"`
}

func (s *Server) requireResellerID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "reseller id must be a UUID")
		return "", false
	}
	if _, err := s.Store.GetResellerLimits(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrResellerNotFound) {
			writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
			return "", false
		}
		s.Logger.Error("get_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the reseller")
		return "", false
	}
	return id, true
}

func (s *Server) resellerSalesResponse(r *http.Request, id string) (resellerSalesBody, error) {
	rp, err := s.Store.GetResellerPricing(r.Context(), id)
	if err != nil {
		return resellerSalesBody{}, err
	}
	bal, err := s.Store.WalletBalance(r.Context(), id)
	if err != nil {
		return resellerSalesBody{}, err
	}
	b := resellerSalesBody{DiscountPercent: rp.DiscountPercent, ChargeWallet: rp.ChargeWallet, SelfService: rp.SelfService,
		Overrides: []resellerOverrideBody{}, BalanceToman: bal}
	for tid, p := range rp.Overrides {
		b.Overrides = append(b.Overrides, resellerOverrideBody{TemplateID: tid, PriceToman: p})
	}
	return b, nil
}

func (s *Server) handleGetResellerSales(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireResellerID(w, r)
	if !ok {
		return
	}
	b, err := s.resellerSalesResponse(r, id)
	if err != nil {
		s.Logger.Error("get_reseller_sales_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load pricing")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleUpdateResellerSales(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := s.requireResellerID(w, r)
	if !ok {
		return
	}
	var req resellerSalesBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.DiscountPercent < 0 || req.DiscountPercent > 100 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "discount_percent must be between 0 and 100")
		return
	}
	rp := store.ResellerPricing{AdminID: id, DiscountPercent: req.DiscountPercent, ChargeWallet: req.ChargeWallet, SelfService: req.SelfService, Overrides: map[string]int64{}}
	for _, o := range req.Overrides {
		if !uuidRE.MatchString(o.TemplateID) || o.PriceToman < 0 || o.PriceToman > maxPriceToman {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "each override needs a template UUID and a price between 0 and 10,000,000,000")
			return
		}
		rp.Overrides[o.TemplateID] = o.PriceToman
	}
	if err := s.Store.SaveResellerPricing(ctx, rp); err != nil {
		s.Logger.Error("save_reseller_pricing_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save pricing")
		return
	}
	identity, _ := callerIdentityFromContext(ctx)
	_ = s.Store.InsertAuditLog(ctx, auditActor(identity), "reseller.pricing_updated", id, map[string]any{
		"discount_percent": rp.DiscountPercent, "charge_wallet": rp.ChargeWallet, "self_service": rp.SelfService, "overrides": len(rp.Overrides),
	}, r.RemoteAddr)
	s.handleGetResellerSales(w, r)
}

type ledgerEntryResponse struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	AmountToman  int64   `json:"amount_toman"`
	BalanceAfter int64   `json:"balance_after"`
	Reason       string  `json:"reason"`
	OrderID      *string `json:"order_id"`
	Actor        string  `json:"actor"`
	CreatedAt    string  `json:"created_at"`
}

func (s *Server) writeLedger(w http.ResponseWriter, r *http.Request, adminID string) {
	ctx := r.Context()
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "before must be an RFC3339 timestamp")
			return
		}
		before = &t
	}
	entries, err := s.Store.ListWalletLedger(ctx, adminID, limit, before)
	if err != nil {
		s.Logger.Error("list_ledger_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the ledger")
		return
	}
	bal, err := s.Store.WalletBalance(ctx, adminID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the balance")
		return
	}
	items := make([]ledgerEntryResponse, 0, len(entries))
	for _, e := range entries {
		items = append(items, ledgerEntryResponse{ID: e.ID, Kind: e.Kind, AmountToman: e.AmountToman, BalanceAfter: e.BalanceAfter,
			Reason: e.Reason, OrderID: e.OrderID, Actor: e.Actor, CreatedAt: e.CreatedAt.Format(time.RFC3339Nano)})
	}
	resp := map[string]any{"balance_toman": bal, "items": items}
	if len(entries) == limitOrDefault(limit) {
		resp["next_before"] = entries[len(entries)-1].CreatedAt.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}

func limitOrDefault(l int) int {
	if l <= 0 || l > 200 {
		return 50
	}
	return l
}

func (s *Server) handleResellerLedger(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireResellerID(w, r)
	if !ok {
		return
	}
	s.writeLedger(w, r, id)
}

type walletAdjustRequest struct {
	AmountToman int64  `json:"amount_toman"` // positive credits, negative debits
	Reason      string `json:"reason"`
}

// handleAdjustResellerWallet is the super admin's manual credit/debit. A reason is
// mandatory (it lands in the ledger and the audit log) and a debit that would take the
// balance below zero is refused, like any other debit.
func (s *Server) handleAdjustResellerWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := s.requireResellerID(w, r)
	if !ok {
		return
	}
	var req walletAdjustRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.AmountToman == 0 || req.AmountToman > maxWalletAdjust || req.AmountToman < -maxWalletAdjust {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "amount_toman must be a non-zero whole number up to 1,000,000,000 in either direction")
		return
	}
	if req.Reason == "" || len([]rune(req.Reason)) > 200 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "a reason (up to 200 characters) is required")
		return
	}
	identity, _ := callerIdentityFromContext(ctx)
	kind := store.LedgerManualCredit
	if req.AmountToman < 0 {
		kind = store.LedgerManualDebit
	}
	bal, err := s.Store.WalletApply(ctx, id, req.AmountToman, kind, req.Reason, nil, auditActor(identity))
	if errors.Is(err, store.ErrInsufficientBalance) {
		writeJSONError(w, http.StatusPaymentRequired, "insufficient_balance", "the debit is larger than the balance")
		return
	}
	if err != nil {
		s.Logger.Error("wallet_adjust_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not adjust the wallet")
		return
	}
	_ = s.Store.InsertAuditLog(ctx, auditActor(identity), "wallet."+kind, id, map[string]any{"amount_toman": req.AmountToman, "reason": req.Reason, "balance_toman": bal}, r.RemoteAddr)
	if req.AmountToman > 0 {
		s.emit(events.WalletCredited, store.ResellerNamespace(id), map[string]any{"reseller_id": id, "amount_toman": req.AmountToman, "balance_toman": bal, "reason": req.Reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance_toman": bal})
}
