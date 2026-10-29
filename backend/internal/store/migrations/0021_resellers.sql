-- Resellers: human panel logins (admins.role = 'reseller') that own a private slice
-- of accounts. Their accounts are tagged owner_key_namespace = 'reseller:<admin id>',
-- so the existing namespace scoping (namespaceFilter) isolates them exactly like an
-- API key - this migration only adds the *limits* a reseller operates under.
--
-- Why a separate table instead of columns on admins: (1) six reseller-only columns
-- would sit NULL on every operator/support row; (2) quota enforcement locks this row
-- with SELECT ... FOR UPDATE to serialise concurrent account creates for one reseller -
-- locking a dedicated row never contends with login/refresh reads of admins; (3)
-- backup/restore and the admins listing stay untouched. admin_id cascades so deleting
-- the admin can never strand a limits row.
--
-- NULL means "unlimited" for the three numeric limits. node_groups is the opposite
-- convention to api_keys.node_groups on purpose: an empty array means "every node
-- group" (a reseller is a trusted customer-facing tenant; an empty allowlist that
-- silently blocked everything would be a footgun in a UI form).
CREATE TABLE reseller_limits (
    admin_id         UUID PRIMARY KEY REFERENCES admins (id) ON DELETE CASCADE,
    max_accounts     INTEGER CHECK (max_accounts IS NULL OR max_accounts >= 0),
    -- Allocation-based traffic credit: sum(data_quota_bytes) over the reseller's
    -- non-deleted accounts may not exceed this. Not consumption-based.
    data_limit_bytes BIGINT CHECK (data_limit_bytes IS NULL OR data_limit_bytes >= 0),
    node_groups      TEXT[] NOT NULL DEFAULT '{}',
    max_expiry_days  INTEGER CHECK (max_expiry_days IS NULL OR max_expiry_days >= 1),
    disabled         BOOLEAN NOT NULL DEFAULT false,
    notes            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- accounts.suspend_reason gains 'reseller_suspended' (set only by the super-admin
-- bulk suspend; the matching bulk reactivate touches only that reason). The column
-- is free text, so no constraint change is needed.
