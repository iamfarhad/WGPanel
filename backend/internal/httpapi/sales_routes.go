package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// registerSalesRoutes mounts the whole sales surface (payments, orders, wallet, public
// shop). server.go calls it with one line; everything else sales-related lives in
// sales_*.go files.
func (s *Server) registerSalesRoutes(mux *http.ServeMux) {
	// ---- public: subscription page shop, /buy, gateway callbacks (no auth) ----------
	mux.HandleFunc("GET /api/v1/sub/{token}/shop", s.gateReads(s.handleSubShop))
	mux.HandleFunc("POST /api/v1/sub/{token}/orders", s.gateCreate(s.handleSubCreateOrder))
	mux.HandleFunc("GET /api/v1/sub/{token}/orders/{ref}", s.gateReads(s.handleSubGetOrder))
	mux.HandleFunc("POST /api/v1/sub/{token}/orders/{ref}/receipt", s.gateReceipt(s.handleSubReceipt))
	mux.HandleFunc("GET /api/v1/sub/buy/shop", s.gateReads(s.handleBuyShop))
	mux.HandleFunc("POST /api/v1/sub/buy/orders", s.gateCreate(s.handleBuyCreateOrder))
	mux.HandleFunc("GET /api/v1/sub/buy/orders/{ref}", s.gateReads(s.handleBuyGetOrder))
	mux.HandleFunc("POST /api/v1/sub/buy/orders/{ref}/receipt", s.gateReceipt(s.handleBuyReceipt))
	mux.HandleFunc("GET /api/v1/sub/pay/zarinpal", s.gateCallback(s.handleZarinpalCallback))
	mux.HandleFunc("POST /api/v1/sub/pay/nowpayments", s.gateCallback(s.handleNowPaymentsIPN))

	// ---- payment settings (super admin) --------------------------------------------
	mux.Handle("GET /api/v1/payments/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGetPaymentSettings))))
	mux.Handle("PUT /api/v1/payments/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdatePaymentSettings))))

	// ---- template prices: staff price any template, a reseller or key its own -------
	mux.Handle("GET /api/v1/template-prices", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListTemplatePrices))))
	mux.Handle("PUT /api/v1/templates/{id}/price", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleSetTemplatePrice))))

	// ---- per-reseller pricing and wallet administration (super admin) ---------------
	mux.Handle("GET /api/v1/resellers/{id}/sales", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGetResellerSales))))
	mux.Handle("PUT /api/v1/resellers/{id}/sales", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateResellerSales))))
	mux.Handle("GET /api/v1/resellers/{id}/wallet", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleResellerLedger))))
	mux.Handle("POST /api/v1/resellers/{id}/wallet/adjust", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleAdjustResellerWallet))))

	// ---- orders ----------------------------------------------------------------------
	// Reads are shared with API keys (scoped to the key's namespace) and resellers;
	// review actions are operator-and-above staff only (a reseller cannot approve money
	// it is not the recipient of).
	mux.Handle("GET /api/v1/orders", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListOrders))))
	mux.Handle("GET /api/v1/orders/{id}", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetOrder))))
	mux.Handle("GET /api/v1/orders/{id}/receipt", s.requireAdmin(http.HandlerFunc(s.handleGetOrderReceipt)))
	mux.Handle("POST /api/v1/orders/{id}/receipt", s.requireAdmin(http.HandlerFunc(s.handleUploadOrderReceipt)))
	mux.Handle("POST /api/v1/orders/{id}/approve", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleApproveOrder))))
	mux.Handle("POST /api/v1/orders/{id}/reject", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleRejectOrder))))
	mux.Handle("POST /api/v1/orders/{id}/cancel", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleCancelOrder))))
	mux.Handle("POST /api/v1/orders/{id}/retry", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleRetryOrder))))

	// ---- reseller wallet (session or reseller-owned key) ----------------------------
	mux.Handle("GET /api/v1/wallet", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetWallet))))
	mux.Handle("GET /api/v1/wallet/ledger", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleWalletLedger))))
	mux.Handle("POST /api/v1/wallet/topups", s.requireAdminOrAPIKey(s.requirePermission("create", http.HandlerFunc(s.handleCreateTopup))))
}

// salesResellerRoutes joins the reseller session allowlist (reseller_gate.go) from init,
// so that file is not edited. Same fail-closed rule: anything not listed stays denied.
var salesResellerRoutes = []string{
	"GET /api/v1/template-prices",
	"PUT /api/v1/templates/{id}/price", // store-scoped: a reseller can price only its own templates
	"GET /api/v1/orders",
	"GET /api/v1/orders/{id}",
	"GET /api/v1/orders/{id}/receipt",
	"POST /api/v1/orders/{id}/receipt",
	"GET /api/v1/wallet",
	"GET /api/v1/wallet/ledger",
	"POST /api/v1/wallet/topups",
}

func init() {
	for _, p := range salesResellerRoutes {
		resellerRoutes[p] = true
	}
}

// redactOrderRef masks the order ref (a buyer capability) in /orders/{ref} access-log
// paths, as redactPath already masks the subscription token.
func redactOrderRef(p string) string {
	const marker = "/orders/"
	i := strings.Index(p, marker)
	if i < 0 {
		return p
	}
	rest := p[i+len(marker):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return p[:i+len(marker)] + "[redacted]" + rest[j:]
	}
	return p[:i+len(marker)] + "[redacted]"
}

// ---- background worker ------------------------------------------------------------

const salesWorkerEvery = 30 * time.Second

// RunSalesWorker expires unpaid orders, retries paid orders whose fulfilment failed or
// was interrupted before it was claimed, re-verifies ZarinPal payments whose verify call
// failed, and trims old receipt images. cmd/api/main.go starts it as a goroutine.
func (s *Server) RunSalesWorker(ctx context.Context) {
	t := time.NewTicker(salesWorkerEvery)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.salesTick(ctx)
			n++
			if n%120 == 0 { // hourly
				if c, err := s.Store.PurgeSettledReceipts(ctx, receiptRetention); err != nil {
					s.Logger.Warn("purge_receipts_failed", "error", err)
				} else if c > 0 {
					s.Logger.Info("purged_receipts", "count", c)
				}
			}
		}
	}
}

// receiptRetention is how long a settled order keeps its receipt image.
const receiptRetention = 90 * 24 * time.Hour

func (s *Server) salesTick(ctx context.Context) {
	if expired, err := s.Store.ExpirePendingOrders(ctx); err != nil {
		s.Logger.Warn("expire_orders_failed", "error", err)
	} else {
		for _, o := range expired {
			s.auditOrder(ctx, orderActorSystem, "order.expired", o, nil)
		}
	}
	s.reverifyZarinpal(ctx)
	paid, err := s.Store.PaidUnfulfilled(ctx, maxFulfilAttempts, 20)
	if err != nil {
		s.Logger.Warn("paid_unfulfilled_failed", "error", err)
		return
	}
	for _, o := range paid {
		if err := s.fulfilOrder(ctx, o.ID); err != nil {
			s.Logger.Warn("order_fulfil_retry_failed", "order", o.ID, "attempt", o.FulfilAttempts+1, "error", err)
		}
	}
}

// reverifyZarinpal finishes ZarinPal orders whose buyer came back (callback_ok) but whose
// verify call could not complete at the time.
func (s *Server) reverifyZarinpal(ctx context.Context) {
	orders, err := s.Store.ListOrders(ctx, store.OrderFilter{Status: store.OrderPending, Limit: 50})
	if err != nil {
		return
	}
	var cfg *paymentConfig
	for _, o := range orders {
		if o.Method != store.MethodZarinpal || o.GatewayRef == nil || !strings.Contains(string(o.GatewayData), `"callback_ok"`) {
			continue
		}
		if cfg == nil {
			if cfg, err = s.loadPaymentConfig(ctx); err != nil {
				return
			}
		}
		res, err := (zarinpalProvider{}).verify(ctx, cfg, o)
		if err != nil {
			continue // still unreachable: try again next tick, until the order expires
		}
		if _, err := s.applySettle(ctx, res); err != nil {
			s.Logger.Warn("reverify_apply_failed", "order", o.ID, "error", err)
		}
	}
}

// salesEventForNamespaces reports whether a sales event may be subscribed to by a
// namespaced webhook (reseller or bot key): every sales event except
// payment.awaiting_review, which is a staff-only call to action and is always emitted
// globally anyway.
func salesEventForNamespaces(e string) bool {
	return e != "payment.awaiting_review" &&
		(strings.HasPrefix(e, "order.") || strings.HasPrefix(e, "payment.") || strings.HasPrefix(e, "wallet."))
}
