package events

// Sales events (payments, orders, reseller wallet). They live in their own file and
// join the catalogue through init so the sales package does not touch the shared event
// list. Keep in sync with frontend/src/lib/notificationEvents.ts.
const (
	PaymentAwaitingReview = "payment.awaiting_review"
	PaymentApproved       = "payment.approved"
	PaymentRejected       = "payment.rejected"
	OrderFulfilled        = "order.fulfilled"
	WalletCredited        = "wallet.credited"
)

func init() {
	catalog = append(catalog,
		// The only sales event that asks a human to act, so it is the only one that
		// rings the in-panel bell for staff by default.
		Meta{PaymentAwaitingReview, SeverityWarning, true},
		Meta{PaymentApproved, SeverityInfo, false},
		Meta{PaymentRejected, SeverityInfo, false},
		Meta{OrderFulfilled, SeverityInfo, false},
		// A reseller sees its own top-ups in its bell.
		Meta{WalletCredited, SeverityInfo, true},
	)
}
