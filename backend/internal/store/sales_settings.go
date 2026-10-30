package store

import "context"

// Payment configuration (migration 0030): one singleton row plus the card list. The
// *Encrypted fields hold wgkeys ciphertext; callers decrypt them where a provider needs
// the plaintext and never expose them through an API.

type PaymentSettings struct {
	CardEnabled                   bool
	ZarinpalEnabled               bool
	ZarinpalMerchantEncrypted     string
	ZarinpalSandbox               bool
	NowPaymentsEnabled            bool
	NowPaymentsAPIKeyEncrypted    string
	NowPaymentsIPNSecretEncrypted string
	NowPaymentsSandbox            bool
	NowPaymentsTomanPerUSD        int64
	CallbackBaseURL               string
	PublicBuyEnabled              bool
}

type PaymentCard struct {
	ID      string
	Number  string
	Holder  string
	Bank    string
	Enabled bool
}

func (s *Store) GetPaymentSettings(ctx context.Context) (PaymentSettings, error) {
	var p PaymentSettings
	// The row is seeded by the migration; this only matters after restoring a backup
	// that predates it, where the singleton would otherwise be missing.
	if _, err := s.pool.Exec(ctx, `INSERT INTO payment_settings (id) VALUES (1) ON CONFLICT DO NOTHING`); err != nil {
		return p, err
	}
	err := s.pool.QueryRow(ctx, `
		SELECT card_enabled, zarinpal_enabled, zarinpal_merchant_encrypted, zarinpal_sandbox,
		       nowpayments_enabled, nowpayments_api_key_encrypted, nowpayments_ipn_secret_encrypted,
		       nowpayments_sandbox, nowpayments_toman_per_usd, callback_base_url, public_buy_enabled
		FROM payment_settings WHERE id = 1`).Scan(
		&p.CardEnabled, &p.ZarinpalEnabled, &p.ZarinpalMerchantEncrypted, &p.ZarinpalSandbox,
		&p.NowPaymentsEnabled, &p.NowPaymentsAPIKeyEncrypted, &p.NowPaymentsIPNSecretEncrypted,
		&p.NowPaymentsSandbox, &p.NowPaymentsTomanPerUSD, &p.CallbackBaseURL, &p.PublicBuyEnabled)
	return p, err
}

// SavePaymentSettings overwrites every column; the caller merges unchanged secrets in
// first (an omitted secret in the API means "keep").
func (s *Store) SavePaymentSettings(ctx context.Context, p PaymentSettings) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE payment_settings SET card_enabled = $1, zarinpal_enabled = $2, zarinpal_merchant_encrypted = $3,
			zarinpal_sandbox = $4, nowpayments_enabled = $5, nowpayments_api_key_encrypted = $6,
			nowpayments_ipn_secret_encrypted = $7, nowpayments_sandbox = $8, nowpayments_toman_per_usd = $9,
			callback_base_url = $10, public_buy_enabled = $11, updated_at = now()
		WHERE id = 1`,
		p.CardEnabled, p.ZarinpalEnabled, p.ZarinpalMerchantEncrypted, p.ZarinpalSandbox, p.NowPaymentsEnabled,
		p.NowPaymentsAPIKeyEncrypted, p.NowPaymentsIPNSecretEncrypted, p.NowPaymentsSandbox,
		p.NowPaymentsTomanPerUSD, p.CallbackBaseURL, p.PublicBuyEnabled)
	return err
}

func (s *Store) ListPaymentCards(ctx context.Context) ([]PaymentCard, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, number, holder, bank, enabled FROM payment_cards ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentCard{}
	for rows.Next() {
		var c PaymentCard
		if err := rows.Scan(&c.ID, &c.Number, &c.Holder, &c.Bank, &c.Enabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplacePaymentCards swaps the whole card list in one transaction (the settings form
// edits it as a unit).
func (s *Store) ReplacePaymentCards(ctx context.Context, cards []PaymentCard) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM payment_cards`); err != nil {
		return err
	}
	for _, c := range cards {
		if _, err := tx.Exec(ctx, `INSERT INTO payment_cards (number, holder, bank, enabled) VALUES ($1, $2, $3, $4)`,
			c.Number, c.Holder, c.Bank, c.Enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
