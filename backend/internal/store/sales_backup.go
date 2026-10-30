package store

// The sales tables join the panel backup. They are appended from init (not edited into
// the list in backup.go) so this package stays out of that shared file. Order satisfies
// the foreign keys: they reference admins, plan_templates and accounts, all listed
// earlier in backupTables.
//
// order_receipts (the receipt image blobs) is deliberately NOT included:
//   - a receipt is short-lived evidence for an admin's approve/reject decision, not
//     configuration - once an order is settled nobody needs the picture again;
//   - images would dominate the size of an otherwise tiny, Telegram-uploadable backup;
//   - the tables that matter for money (orders, wallets, the append-only ledger) ARE
//     in the backup, so a restore never loses a balance or a paid order. After a restore
//     an order that was still awaiting review simply has no image (the receipt endpoint
//     answers 404 receipt_unavailable) and the admin can reject it so the buyer re-sends.
//
// TRUNCATE ... CASCADE in RestoreTables empties order_receipts together with orders, so
// a restore never leaves blobs pointing at vanished orders.
func init() {
	backupTables = append(backupTables,
		"payment_settings",
		"payment_cards",
		"template_prices",
		"reseller_pricing",
		"reseller_template_prices",
		"reseller_wallets",
		"wallet_ledger",
		"orders",
	)
}
