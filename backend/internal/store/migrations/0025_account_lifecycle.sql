-- Account lifecycle for sellers: on-hold accounts, scheduled quota resets, plan templates.
--
-- On hold: an account with hold_duration_days set has NOT started its validity clock.
-- It stays status='active' (so the node agents keep serving it and nothing that reads
-- status changes), carries expiry_at = NULL, and is activated by the first WireGuard
-- handshake (monitoring ingest) or, failing that, when hold_deadline_at passes (sweep):
-- activation sets expiry_at = activation time + hold_duration_days and clears the hold.
--
-- Scheduled reset: reset_strategy (none|day|week|month|year) + next_reset_at; the sweep
-- zeroes data_used_bytes through the same logic as POST /accounts/{id}/reset-usage.
ALTER TABLE accounts
    ADD COLUMN hold_duration_days INTEGER CHECK (hold_duration_days IS NULL OR hold_duration_days > 0),
    ADD COLUMN hold_deadline_at   TIMESTAMPTZ,
    ADD COLUMN activated_at       TIMESTAMPTZ,
    ADD COLUMN reset_strategy     TEXT NOT NULL DEFAULT 'none'
        CHECK (reset_strategy IN ('none', 'day', 'week', 'month', 'year')),
    ADD COLUMN next_reset_at      TIMESTAMPTZ,
    ADD COLUMN last_reset_at      TIMESTAMPTZ;

CREATE INDEX idx_accounts_next_reset ON accounts (next_reset_at)
    WHERE next_reset_at IS NOT NULL AND status != 'deleted';
CREATE INDEX idx_accounts_hold_deadline ON accounts (hold_deadline_at)
    WHERE hold_duration_days IS NOT NULL AND hold_deadline_at IS NOT NULL AND status != 'deleted';

-- Plan templates. owner_namespace NULL = global (staff); 'reseller:<id>' = a reseller's
-- own. Accounts copy template values at creation and keep no reference to it.
CREATE TABLE plan_templates (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   TEXT NOT NULL,
    owner_namespace        TEXT,
    available_to_resellers BOOLEAN NOT NULL DEFAULT false,
    data_quota_bytes       BIGINT CHECK (data_quota_bytes IS NULL OR data_quota_bytes >= 0),
    duration_days          INTEGER CHECK (duration_days IS NULL OR duration_days > 0),
    device_limit           INTEGER CHECK (device_limit IS NULL OR device_limit > 0),
    bandwidth_limit_mbps   INTEGER CHECK (bandwidth_limit_mbps IS NULL OR bandwidth_limit_mbps > 0),
    node_group             TEXT,
    reset_strategy         TEXT NOT NULL DEFAULT 'none'
        CHECK (reset_strategy IN ('none', 'day', 'week', 'month', 'year')),
    on_hold                BOOLEAN NOT NULL DEFAULT false,
    hold_deadline_days     INTEGER CHECK (hold_deadline_days IS NULL OR hold_deadline_days > 0),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_plan_templates_owner ON plan_templates (owner_namespace);
