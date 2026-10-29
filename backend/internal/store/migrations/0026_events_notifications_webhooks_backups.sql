-- Events, notifications, webhooks and scheduled off-box backups.

-- Edge-trigger state for threshold events (quota, expiry, node health, reseller credit).
-- active = the condition currently holds; last_fired_at implements the suppression
-- window so a flapping condition cannot spam. See store.EventTransition.
CREATE TABLE event_state (
    key           TEXT PRIMARY KEY,
    active        BOOLEAN NOT NULL DEFAULT false,
    last_fired_at TIMESTAMPTZ
);

-- Notification channels (Telegram / email). The secret (bot token / SMTP password) is
-- encrypted with ACCOUNT_KEY_ENCRYPTION_KEY and never returned by any read API.
CREATE TABLE notification_channels (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                 TEXT NOT NULL CHECK (kind IN ('telegram', 'email')),
    name                 TEXT NOT NULL,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    config               JSONB NOT NULL DEFAULT '{}',
    secret_encrypted     TEXT NOT NULL DEFAULT '',
    events               TEXT[] NOT NULL DEFAULT '{}',
    lang                 TEXT NOT NULL DEFAULT 'en' CHECK (lang IN ('en', 'fa')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error           TEXT NOT NULL DEFAULT '',
    last_success_at      TIMESTAMPTZ,
    last_failure_at      TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- In-panel notification center. namespace '' = global (staff only); 'reseller:<id>' is
-- also visible to that reseller.
CREATE TABLE notifications (
    id         BIGSERIAL PRIMARY KEY,
    event      TEXT NOT NULL,
    severity   TEXT NOT NULL DEFAULT 'info',
    namespace  TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    data       JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notifications_ns_created ON notifications (namespace, id DESC);

-- Per-admin read state: everything at or below read_up_to is read, plus single ids.
CREATE TABLE notification_read_state (
    admin_id   UUID PRIMARY KEY REFERENCES admins (id) ON DELETE CASCADE,
    read_up_to BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE notification_reads (
    admin_id        UUID NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    notification_id BIGINT NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    PRIMARY KEY (admin_id, notification_id)
);

CREATE TABLE webhooks (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace        TEXT, -- NULL = global (staff-managed), 'reseller:<id>' = that reseller's
    url              TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    secret_encrypted TEXT NOT NULL,
    events           TEXT[] NOT NULL DEFAULT '{}', -- empty = every event
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The delivery log doubles as the persistent retry queue: rows in pending/retrying
-- whose next_attempt_at has passed are claimed by the worker (FOR UPDATE SKIP LOCKED).
CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event           TEXT NOT NULL,
    event_id        TEXT NOT NULL,
    payload         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'retrying', 'success', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    response_status INTEGER,
    latency_ms      INTEGER,
    response_body   TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_webhook_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC);
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE status IN ('pending', 'retrying');

-- Singleton: staff-only override of the webhook SSRF guard.
CREATE TABLE notify_settings (
    id                            INTEGER PRIMARY KEY CHECK (id = 1),
    allow_private_webhook_targets BOOLEAN NOT NULL DEFAULT false
);
INSERT INTO notify_settings (id) VALUES (1);

-- Singleton: scheduled off-box backup configuration. Secrets are encrypted with
-- ACCOUNT_KEY_ENCRYPTION_KEY. This table is intentionally NOT part of panel backups.
CREATE TABLE backup_schedule (
    id                       INTEGER PRIMARY KEY CHECK (id = 1),
    enabled                  BOOLEAN NOT NULL DEFAULT false,
    interval_hours           INTEGER NOT NULL DEFAULT 24 CHECK (interval_hours >= 1),
    retention_count          INTEGER NOT NULL DEFAULT 7 CHECK (retention_count >= 1),
    password_encrypted       TEXT NOT NULL DEFAULT '',
    telegram_enabled         BOOLEAN NOT NULL DEFAULT false,
    telegram_token_encrypted TEXT NOT NULL DEFAULT '',
    telegram_chat_id         TEXT NOT NULL DEFAULT '',
    s3_enabled               BOOLEAN NOT NULL DEFAULT false,
    s3_endpoint              TEXT NOT NULL DEFAULT '',
    s3_region                TEXT NOT NULL DEFAULT 'us-east-1',
    s3_bucket                TEXT NOT NULL DEFAULT '',
    s3_prefix                TEXT NOT NULL DEFAULT '',
    s3_access_key            TEXT NOT NULL DEFAULT '',
    s3_secret_encrypted      TEXT NOT NULL DEFAULT '',
    s3_path_style            BOOLEAN NOT NULL DEFAULT true,
    last_run_at              TIMESTAMPTZ
);
INSERT INTO backup_schedule (id) VALUES (1);

CREATE TABLE backup_runs (
    id          BIGSERIAL PRIMARY KEY,
    run_id      UUID NOT NULL,
    destination TEXT NOT NULL CHECK (destination IN ('telegram', 's3')),
    trigger     TEXT NOT NULL DEFAULT 'schedule',
    status      TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    filename    TEXT NOT NULL DEFAULT '',
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_backup_runs_dest ON backup_runs (destination, id DESC);
