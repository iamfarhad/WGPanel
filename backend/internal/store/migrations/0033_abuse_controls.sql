-- Abuse controls: account notes/tags, auto-cleanup of dead accounts, IP-based device
-- limit + sharing detection, and torrent blocking on nodes.

-- ---- Notes and tags -------------------------------------------------------------
-- Free text for the seller ("paid via card, friend of Ali") and normalized tags for
-- filtering ("vip", "trial"). Tags are normalized by the API (lowercase, trimmed,
-- deduplicated), never trusted from the client; the GIN index serves ?tag= filters.
ALTER TABLE accounts
    ADD COLUMN note TEXT NOT NULL DEFAULT '',
    ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}',
    -- Set by the sharing sweep while the account is active from >= N distinct networks
    -- (see abuse_settings.sharing_min_networks); NULL otherwise.
    ADD COLUMN sharing_suspected_at TIMESTAMPTZ,
    -- When the account entered its current suspension. Maintained by the trigger below,
    -- so no write path can forget it. Auto-cleanup measures "dead for N days" from here.
    ADD COLUMN suspended_at TIMESTAMPTZ;
CREATE INDEX idx_accounts_tags ON accounts USING gin (tags);
CREATE INDEX idx_accounts_sharing ON accounts (sharing_suspected_at) WHERE sharing_suspected_at IS NOT NULL;

-- accounts.updated_at moves on every edit (a label rename would restart a "30 days
-- suspended" clock), so the suspension time gets its own column. The clock restarts
-- when the suspension reason changes too (expired -> quota_exceeded is a new state).
UPDATE accounts SET suspended_at = updated_at WHERE status = 'suspended';

CREATE FUNCTION accounts_track_suspended_at() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'suspended' THEN
        IF OLD.status IS DISTINCT FROM 'suspended' OR OLD.suspend_reason IS DISTINCT FROM NEW.suspend_reason THEN
            NEW.suspended_at := now();
        END IF;
    ELSE
        NEW.suspended_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- BEFORE UPDATE only: a backup restore (INSERT) keeps the stored suspended_at.
CREATE TRIGGER accounts_suspended_at
    BEFORE UPDATE OF status, suspend_reason ON accounts
    FOR EACH ROW EXECUTE FUNCTION accounts_track_suspended_at();

CREATE INDEX idx_accounts_auto_cleanup ON accounts (suspended_at)
    WHERE status = 'suspended' AND suspend_reason IN ('expired', 'quota_exceeded');

-- ---- Panel-wide abuse settings (singleton) -------------------------------------
-- Kept out of panel_settings on purpose: that table's single wide row is edited by
-- several features at once. Everything defaults to off / the pre-existing behavior.
CREATE TABLE abuse_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),

    -- Delete accounts that are suspended as expired/quota_exceeded for N days. Never
    -- touches manual, abuse_flag, device_limit, torrent_abuse or reseller suspensions.
    auto_cleanup_enabled BOOLEAN NOT NULL DEFAULT false,
    auto_cleanup_days    INTEGER NOT NULL DEFAULT 30 CHECK (auto_cleanup_days BETWEEN 1 AND 3650),

    -- What the per-account device_limit counts: distinct 'endpoint' (ip:port, the
    -- original behavior) or distinct source 'ip'. Window = how recently a sighting
    -- must have been to count (the original behavior is 5 minutes).
    device_limit_mode     TEXT NOT NULL DEFAULT 'endpoint' CHECK (device_limit_mode IN ('endpoint', 'ip')),
    device_window_minutes INTEGER NOT NULL DEFAULT 5 CHECK (device_window_minutes BETWEEN 1 AND 1440),

    -- Sharing heuristic: flag an account seen from >= sharing_min_networks distinct
    -- IPv4 /24 (IPv6 /48) networks within sharing_window_minutes.
    sharing_detection_enabled BOOLEAN NOT NULL DEFAULT false,
    sharing_min_networks      INTEGER NOT NULL DEFAULT 3 CHECK (sharing_min_networks BETWEEN 2 AND 50),
    sharing_window_minutes    INTEGER NOT NULL DEFAULT 10 CHECK (sharing_window_minutes BETWEEN 1 AND 1440),

    -- Torrent blocking on nodes (global default; per-node override below). Hits are
    -- matching packets, so one attempt can count several (TCP retransmits).
    torrent_block_enabled          BOOLEAN NOT NULL DEFAULT false,
    torrent_suspend_enabled        BOOLEAN NOT NULL DEFAULT false,
    torrent_suspend_hits           INTEGER NOT NULL DEFAULT 50 CHECK (torrent_suspend_hits >= 1),
    torrent_suspend_window_minutes INTEGER NOT NULL DEFAULT 60 CHECK (torrent_suspend_window_minutes BETWEEN 1 AND 10080),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO abuse_settings (id) VALUES (1);

-- A row = this node ignores the global torrent_block_enabled; no row = inherit.
CREATE TABLE node_abuse_overrides (
    node_id       UUID PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    torrent_block BOOLEAN NOT NULL
);

-- Torrent signature hits per account, one row per heartbeat that reported any. 'acted'
-- marks hits already used for an auto-suspend, so re-enabling the account by hand does
-- not instantly re-suspend it on the old hits (the history stays visible).
CREATE TABLE account_torrent_hits (
    id         BIGSERIAL PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    node_id    UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    hits       INTEGER NOT NULL CHECK (hits > 0),
    acted      BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX idx_torrent_hits_account_ts ON account_torrent_hits (account_id, ts DESC);
CREATE INDEX idx_torrent_hits_ts ON account_torrent_hits (ts);
