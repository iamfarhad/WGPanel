-- Sales: template prices, orders and payments, per-reseller pricing, reseller wallet.
--
-- Everything is a NEW table. plan_templates and the reseller tables are deliberately
-- left untouched: prices live beside the template (template_prices) and wallet/pricing
-- state beside the reseller (reseller_pricing, reseller_wallets), keyed by the admin id.
-- Amounts are integer Toman everywhere; no floats ever touch money.

-- ---- Pricing ---------------------------------------------------------------------

-- One row per priced template. price_toman is the retail price for a global template
-- (staff, sold on /buy and to staff-owned accounts) or for a reseller's own template
-- (sold to that reseller's customers). `purchasable` additionally lets customers buy it
-- themselves; a priced but non-purchasable template is only used for wallet charging.
CREATE TABLE template_prices (
    template_id UUID PRIMARY KEY REFERENCES plan_templates (id) ON DELETE CASCADE,
    price_toman BIGINT NOT NULL CHECK (price_toman >= 0),
    purchasable BOOLEAN NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-reseller commercial settings (set by the super admin):
--   discount_percent  off every priced global template for this reseller
--   charge_wallet     creating/renewing from a priced global template debits the wallet
--   self_service      the reseller's customers may buy renewals of its own templates
--                     from the public subscription page
CREATE TABLE reseller_pricing (
    admin_id         UUID PRIMARY KEY REFERENCES admins (id) ON DELETE CASCADE,
    discount_percent INTEGER NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
    charge_wallet    BOOLEAN NOT NULL DEFAULT false,
    self_service     BOOLEAN NOT NULL DEFAULT false,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A per-template price for one reseller; wins over the percentage discount.
CREATE TABLE reseller_template_prices (
    admin_id    UUID NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    template_id UUID NOT NULL REFERENCES plan_templates (id) ON DELETE CASCADE,
    price_toman BIGINT NOT NULL CHECK (price_toman >= 0),
    PRIMARY KEY (admin_id, template_id)
);

-- ---- Wallet ----------------------------------------------------------------------

-- The balance can never go negative: the CHECK is the last line of defence behind the
-- guarded UPDATE in store.WalletDebit, so concurrent debits cannot overdraw.
CREATE TABLE reseller_wallets (
    admin_id       UUID PRIMARY KEY REFERENCES admins (id) ON DELETE CASCADE,
    balance_toman  BIGINT NOT NULL DEFAULT 0 CHECK (balance_toman >= 0),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only ledger: every balance change is one signed row written in the same
-- transaction as the balance update. No FK to admins on purpose: history must outlive a
-- deleted reseller. UUID (not BIGSERIAL) ids so backup restore needs no sequence resync.
CREATE TABLE wallet_ledger (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id      UUID NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('topup', 'manual_credit', 'manual_debit', 'purchase', 'refund')),
    amount_toman  BIGINT NOT NULL CHECK (amount_toman <> 0),
    balance_after BIGINT NOT NULL CHECK (balance_after >= 0),
    reason        TEXT NOT NULL DEFAULT '',
    order_id      UUID,
    actor         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wallet_ledger_admin ON wallet_ledger (admin_id, created_at DESC);
-- A top-up order credits the wallet at most once, whatever calls fulfilment.
CREATE UNIQUE INDEX uq_wallet_ledger_topup_order ON wallet_ledger (order_id) WHERE kind = 'topup';

CREATE FUNCTION wallet_ledger_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER wallet_ledger_no_update BEFORE UPDATE OR DELETE ON wallet_ledger
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_immutable();

-- ---- Payment configuration -------------------------------------------------------

-- Singleton. *_encrypted columns hold wgkeys ciphertext (ACCOUNT_KEY_ENCRYPTION_KEY),
-- like every other secret in the panel, and are never returned by a read API.
CREATE TABLE payment_settings (
    id                              INTEGER PRIMARY KEY CHECK (id = 1),
    card_enabled                    BOOLEAN NOT NULL DEFAULT false,
    zarinpal_enabled                BOOLEAN NOT NULL DEFAULT false,
    zarinpal_merchant_encrypted     TEXT NOT NULL DEFAULT '',
    zarinpal_sandbox                BOOLEAN NOT NULL DEFAULT false,
    nowpayments_enabled             BOOLEAN NOT NULL DEFAULT false,
    nowpayments_api_key_encrypted   TEXT NOT NULL DEFAULT '',
    nowpayments_ipn_secret_encrypted TEXT NOT NULL DEFAULT '',
    nowpayments_sandbox             BOOLEAN NOT NULL DEFAULT false,
    -- NowPayments prices in a fiat/crypto currency, not Toman: the admin states how
    -- many Toman one US dollar is worth and the invoice is raised in USD.
    nowpayments_toman_per_usd       BIGINT NOT NULL DEFAULT 0 CHECK (nowpayments_toman_per_usd >= 0),
    -- Public origin (https://host[:port]) that gateways call back; empty = derive from
    -- the subscription / panel domain.
    callback_base_url               TEXT NOT NULL DEFAULT '',
    -- The public /buy page is off until the operator turns it on.
    public_buy_enabled              BOOLEAN NOT NULL DEFAULT false,
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO payment_settings (id) VALUES (1);

-- Card-to-card destinations shown to buyers. A card number is not a secret (it is
-- printed on every invoice), so it is stored in clear.
CREATE TABLE payment_cards (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number     TEXT NOT NULL,
    holder     TEXT NOT NULL DEFAULT '',
    bank       TEXT NOT NULL DEFAULT '',
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---- Orders ----------------------------------------------------------------------

CREATE TABLE orders (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The buyer's capability for an order they own (public /buy flow and status polling).
    public_token    TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('account_renew', 'account_purchase', 'wallet_topup')),
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'awaiting_review', 'paid', 'fulfilled', 'failed', 'expired', 'rejected', 'cancelled')),
    method          TEXT NOT NULL CHECK (method IN ('card_to_card', 'zarinpal', 'nowpayments')),
    amount_toman    BIGINT NOT NULL CHECK (amount_toman > 0),
    -- Snapshot of the plan sold, so what is fulfilled is what was priced even if the
    -- template is edited or deleted meanwhile. NULL for wallet_topup.
    template_id     UUID REFERENCES plan_templates (id) ON DELETE SET NULL,
    plan            JSONB,
    -- account_renew: the account being renewed; account_purchase: the account created.
    account_id      UUID REFERENCES accounts (id) ON DELETE SET NULL,
    -- wallet_topup: the reseller being credited.
    admin_id        UUID REFERENCES admins (id) ON DELETE SET NULL,
    -- Namespace the order belongs to ('' = staff/global): scopes the reseller view, the
    -- Bot API and the events the order emits.
    owner_namespace TEXT NOT NULL DEFAULT '',
    contact         TEXT NOT NULL DEFAULT '',
    buyer_ip        TEXT NOT NULL DEFAULT '',
    tracking_code   TEXT,
    receipt_type    TEXT,
    receipt_size    INTEGER,
    receipt_sha256  TEXT,
    gateway_ref     TEXT,
    gateway_data    JSONB NOT NULL DEFAULT '{}',
    reviewer        TEXT NOT NULL DEFAULT '',
    review_note     TEXT NOT NULL DEFAULT '',
    -- Fulfilment is claimed with a single UPDATE (see store.ClaimFulfilment): a claimed
    -- order is never picked up twice, an errored one is released for a retry.
    fulfil_claimed_at TIMESTAMPTZ,
    fulfil_attempts   INTEGER NOT NULL DEFAULT 0,
    fulfil_error      TEXT NOT NULL DEFAULT '',
    paid_at         TIMESTAMPTZ,
    fulfilled_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_status ON orders (status, created_at DESC);
CREATE INDEX idx_orders_owner ON orders (owner_namespace, created_at DESC);
CREATE INDEX idx_orders_account ON orders (account_id);
CREATE UNIQUE INDEX uq_orders_gateway_ref ON orders (method, gateway_ref) WHERE gateway_ref IS NOT NULL;
-- One receipt / tracking code backs one live order: replaying the same transfer proof on
-- several orders is the classic card-to-card fraud.
CREATE UNIQUE INDEX uq_orders_tracking ON orders (lower(tracking_code))
    WHERE tracking_code IS NOT NULL AND status IN ('awaiting_review', 'paid', 'fulfilled');
CREATE UNIQUE INDEX uq_orders_receipt_hash ON orders (receipt_sha256)
    WHERE receipt_sha256 IS NOT NULL AND status IN ('awaiting_review', 'paid', 'fulfilled');

-- Receipt images live in their own table so the hot orders rows stay small and so a panel
-- backup can leave the blobs out (see store/sales_backup.go): they are evidence with a
-- short useful life, not configuration.
CREATE TABLE order_receipts (
    order_id     UUID PRIMARY KEY REFERENCES orders (id) ON DELETE CASCADE,
    content_type TEXT NOT NULL,
    data         BYTEA NOT NULL
);
