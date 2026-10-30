-- Split tunnel ("Iran direct"), per-account PresharedKey, and opt-in IPv6 egress per node.
--
-- Routing: accounts and plan templates carry a routing mode. NULL on an account means
-- "default" (full tunnel, today's behaviour); a template's mode is copied onto the
-- account at creation, like every other template field. 'custom' uses the CIDR list as
-- the EXCLUDED set (AllowedIPs is its complement). routing_precision NULL = 'compact'
-- (an AllowedIPs list capped at ~1000 IPv4 entries), 'exact' keeps every prefix (~8000
-- entries for Iran) - see internal/routing.
ALTER TABLE accounts
    ADD COLUMN routing_mode TEXT CHECK (routing_mode IN ('full', 'bypass_iran', 'custom')),
    ADD COLUMN routing_excluded_cidrs TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN routing_precision TEXT CHECK (routing_precision IN ('compact', 'exact'));

ALTER TABLE plan_templates
    ADD COLUMN routing_mode TEXT CHECK (routing_mode IN ('full', 'bypass_iran', 'custom')),
    ADD COLUMN routing_excluded_cidrs TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN routing_precision TEXT CHECK (routing_precision IN ('compact', 'exact'));

-- PresharedKey: encrypted with ACCOUNT_KEY_ENCRYPTION_KEY exactly like
-- private_key_encrypted. NULL until generated: SQL cannot encrypt with the app key, so
-- existing accounts are backfilled in Go at API start (cmd/api psk backfill).
--
-- account_peers.psk_issued_at is the rollout safety latch. WireGuard needs the PSK on
-- BOTH ends or the handshake fails, so an existing client that imported a config
-- without a PSK must keep working until it is handed one. A peer gets the PSK on its
-- node only after a client config carrying it was served for that (account, node), and
-- only on nodes whose agent reports psk support (nodes.psk_supported).
ALTER TABLE accounts ADD COLUMN preshared_key_encrypted TEXT;
ALTER TABLE account_peers ADD COLUMN psk_issued_at TIMESTAMPTZ;

ALTER TABLE nodes
    ADD COLUMN psk_supported BOOLEAN NOT NULL DEFAULT false,      -- agent heartbeat capability
    -- IPv6 egress, opt-in per node. ipv6_ula_prefix is the node's /64 (ULA, fd00::/8);
    -- every peer's v6 address is that prefix + the peer's IPv4 host index.
    ADD COLUMN ipv6_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN ipv6_ula_prefix TEXT,
    ADD COLUMN ipv6_host_global BOOLEAN NOT NULL DEFAULT false,              -- agent: host has a global v6 route
    ADD COLUMN ipv6_ready BOOLEAN NOT NULL DEFAULT false;                    -- agent: forwarding + NAT66 + wg address applied
