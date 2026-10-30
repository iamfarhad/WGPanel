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
-- Rollout rule: WireGuard needs the PSK on BOTH ends or the handshake fails, so a node
-- peer may require its PSK only if EVERY config ever served for that (account, node)
-- carried it. A customer's config is often on several devices, and a config imported
-- before this feature has no PSK, so:
--   plain_served  - a PSK-less config was (or must be assumed to have been) served for
--                   this (account, node). Sticky: once true the peer never requires a PSK.
--   psk_issued_at - a config carrying the PSK was served (only possible while
--                   plain_served is false, on a node whose agent reports psk support).
-- The node is told to require the PSK only for peers with psk_issued_at set and
-- plain_served false. Every peer that exists now is marked plain_served, since we cannot
-- know whether a PSK-less config was handed out; so existing accounts stay PSK-less on
-- their current nodes, and only peers created from now on can get a PSK.
ALTER TABLE accounts ADD COLUMN preshared_key_encrypted TEXT;
ALTER TABLE account_peers
    ADD COLUMN psk_issued_at TIMESTAMPTZ,
    ADD COLUMN plain_served BOOLEAN NOT NULL DEFAULT false;
UPDATE account_peers SET plain_served = true;

ALTER TABLE nodes
    ADD COLUMN psk_supported BOOLEAN NOT NULL DEFAULT false,      -- agent heartbeat capability
    -- IPv6 egress, opt-in per node. ipv6_ula_prefix is the node's /64 (ULA, fd00::/8);
    -- every peer's v6 address is that prefix + the peer's IPv4 host index.
    ADD COLUMN ipv6_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN ipv6_ula_prefix TEXT,
    ADD COLUMN ipv6_host_global BOOLEAN NOT NULL DEFAULT false,              -- agent: host has a global v6 route
    ADD COLUMN ipv6_ready BOOLEAN NOT NULL DEFAULT false;                    -- agent: forwarding + NAT66 + wg address applied
