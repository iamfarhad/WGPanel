-- Two reseller refinements on top of 0021.
--
-- 1. A reseller *plan expiry date* replaces the per-account max_expiry_days cap. The
--    owner sells a reseller plan that ends on a date; the reseller is blocked after it
--    (like `disabled`, but reported as reseller_expired) and none of its accounts may
--    outlive it. NULL = the reseller never expires. max_expiry_days is dropped (it was
--    unreleased; any value set on a dev DB is discarded).
ALTER TABLE reseller_limits ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE reseller_limits DROP COLUMN max_expiry_days;

-- 2. Resellers can own API keys for their bots. A reseller-owned key authenticates as
--    that reseller's namespace ('reseller:<admin id>') instead of its own key id, so
--    the bot sees and creates the reseller's accounts and runs through the same quota
--    checks. NULL = a staff-created key (namespace = key_id, unchanged). Deleting the
--    admin removes its keys.
ALTER TABLE api_keys ADD COLUMN reseller_id UUID REFERENCES admins (id) ON DELETE CASCADE;
CREATE INDEX idx_api_keys_reseller_id ON api_keys (reseller_id) WHERE reseller_id IS NOT NULL;
