-- Extra client-facing entry points per node (relays in front of a DPI-blocked node),
-- plus panel-wide client MTU and Bot API rate-limit settings.
--
-- A node keeps its single nodes.public_endpoint (the direct address, also what the
-- agent/installer use). node_endpoints adds alternatives: the highest-priority enabled
-- endpoint that is not known-down is what generated configs point clients at; with none,
-- configs use public_endpoint exactly as before.
CREATE TABLE node_endpoints (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    label           TEXT NOT NULL,
    address         TEXT NOT NULL,                      -- host:port, as in Endpoint =
    kind            TEXT NOT NULL DEFAULT 'relay' CHECK (kind IN ('direct', 'relay')),
    priority        INTEGER NOT NULL DEFAULT 100,       -- higher wins
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    mtu             INTEGER CHECK (mtu IS NULL OR (mtu BETWEEN 1200 AND 1420)),
    health_check    TEXT NOT NULL DEFAULT 'none' CHECK (health_check IN ('none', 'tcp')),
    last_status     TEXT NOT NULL DEFAULT 'unknown' CHECK (last_status IN ('unknown', 'up', 'down')),
    last_checked_at TIMESTAMPTZ,
    last_latency_ms INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (node_id, label)
);
CREATE INDEX idx_node_endpoints_node_id ON node_endpoints (node_id);

ALTER TABLE panel_settings ADD COLUMN client_mtu INTEGER NOT NULL DEFAULT 1280
    CHECK (client_mtu BETWEEN 1200 AND 1420);
ALTER TABLE panel_settings ADD COLUMN api_rate_rps INTEGER NOT NULL DEFAULT 20
    CHECK (api_rate_rps BETWEEN 1 AND 10000);
ALTER TABLE panel_settings ADD COLUMN api_rate_burst INTEGER NOT NULL DEFAULT 40
    CHECK (api_rate_burst BETWEEN 1 AND 100000);
