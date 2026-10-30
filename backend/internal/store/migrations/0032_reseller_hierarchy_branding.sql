-- Sub-resellers and per-reseller branding.
--
-- 1. Hierarchy. The market works admin -> agent -> sub-agent, so a reseller may own
--    child resellers. The tree lives on reseller_limits (no new table): the limits row
--    is already the reseller's identity inside the panel and the row every quota write
--    locks, so the parent/child relationship sits next to the numbers it constrains.
--
--    parent_id  - the direct parent; NULL = a top-level reseller created by staff.
--                 It is never changed after creation (no re-parenting), which is what
--                 lets the application take the parent row lock before the child's
--                 without racing a move. Plain NO ACTION on purpose: a parent cannot be
--                 deleted from under its children; DeleteResellerTree removes a whole
--                 subtree in one statement.
--    ancestors  - materialised path: every ancestor's *namespace* ('reseller:<id>'),
--                 root first. Namespaces rather than UUIDs so `ancestors @> ARRAY[$1]`
--                 can be matched against the caller namespace text directly (the
--                 namespace filters take a text parameter that is not always a UUID -
--                 API key namespaces are key ids). "All descendants of X" is a GIN
--                 containment lookup; depth is cardinality(ancestors) + 1.
--    disabled_by_staff - set when a staff admin disables a reseller, so a parent
--                 reseller cannot re-enable a child that the owner switched off.
ALTER TABLE reseller_limits ADD COLUMN parent_id UUID REFERENCES admins (id);
ALTER TABLE reseller_limits ADD COLUMN ancestors TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE reseller_limits ADD COLUMN disabled_by_staff BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX idx_reseller_limits_parent_id ON reseller_limits (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX idx_reseller_limits_ancestors ON reseller_limits USING GIN (ancestors);

-- A reseller is blocked when it, or any ancestor, is disabled or past its plan end.
-- These two functions are the single definition of "effective" state, shared by the
-- per-request gate, the quota checks and the on-hold activation clamp (lifecycle.go).
CREATE FUNCTION reseller_effective_expires_at(p_admin UUID) RETURNS TIMESTAMPTZ
LANGUAGE sql STABLE AS $$
    SELECT min(r.expires_at) FROM reseller_limits r
    WHERE r.admin_id = p_admin
       OR ('reseller:' || r.admin_id::text) = ANY (
            COALESCE((SELECT x.ancestors FROM reseller_limits x WHERE x.admin_id = p_admin), '{}'))
$$;

CREATE FUNCTION reseller_ancestor_disabled(p_admin UUID) RETURNS BOOLEAN
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(bool_or(r.disabled), false) FROM reseller_limits r
    WHERE ('reseller:' || r.admin_id::text) = ANY (
            COALESCE((SELECT x.ancestors FROM reseller_limits x WHERE x.admin_id = p_admin), '{}'))
$$;

-- 2. Branding. One row per reseller plus one global default (admin_id NULL), kept out of
--    panel_settings so the singleton settings row stays small and other features can
--    keep adding columns to it. The public subscription page renders the nearest
--    ancestor's profile that has any content, else the global one (whole profile, never
--    field-by-field mixing, so a customer never sees reseller A's name with reseller
--    B's support link).
--
--    The logo is either an uploaded image stored here (logo_data, hard-capped at 64 KiB
--    - a logo is a small mark, and the bytes ride along in every backup) or an https
--    URL (logo_url). PNG/JPEG/WebP only: SVG can carry script and is served from the
--    panel's own origin. Text limits are CHECKs too; the API validates first.
CREATE TABLE brand_profiles (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id     UUID REFERENCES admins (id) ON DELETE CASCADE,
    brand_name   TEXT NOT NULL DEFAULT '' CHECK (char_length(brand_name) <= 40),
    support_link TEXT NOT NULL DEFAULT '' CHECK (char_length(support_link) <= 200),
    announcement TEXT NOT NULL DEFAULT '' CHECK (char_length(announcement) <= 280),
    accent_color TEXT NOT NULL DEFAULT '' CHECK (accent_color = '' OR accent_color ~ '^#[0-9a-fA-F]{6}$'),
    logo_url     TEXT NOT NULL DEFAULT '' CHECK (char_length(logo_url) <= 300),
    logo_type    TEXT NOT NULL DEFAULT '' CHECK (logo_type IN ('', 'image/png', 'image/jpeg', 'image/webp')),
    logo_data    BYTEA CHECK (logo_data IS NULL OR octet_length(logo_data) <= 65536),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_brand_profiles_admin ON brand_profiles (admin_id) WHERE admin_id IS NOT NULL;
CREATE UNIQUE INDEX uq_brand_profiles_global ON brand_profiles ((true)) WHERE admin_id IS NULL;
