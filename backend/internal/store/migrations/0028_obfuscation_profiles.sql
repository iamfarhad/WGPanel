-- AmneziaWG obfuscation profiles. A profile is a named AWG parameter set (junk, padding,
-- message headers, custom signature packets) that can be assigned to nodes; a node with
-- a profile runs AmneziaWG instead of kernel WireGuard and client configs for it carry
-- the same parameters.
--
-- Zero / empty means "not set" (the line is left out of configs, AWG treats it as 0),
-- which is why the numeric columns default to 0 instead of being nullable.
-- compat 'awg1' = 1.x parameters only (broadest client support); 'awg2' adds S3/S4,
-- H ranges and I1-I5 signature packets. Validation lives in internal/obfuscation.
-- revision is bumped on every parameter change/rotation; nodes report the revision
-- they actually run, and configs only carry AWG lines once those match.
CREATE TABLE obfuscation_profiles (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    compat     TEXT NOT NULL DEFAULT 'awg1' CHECK (compat IN ('awg1', 'awg2')),
    jc         INTEGER NOT NULL DEFAULT 0,
    jmin       INTEGER NOT NULL DEFAULT 0,
    jmax       INTEGER NOT NULL DEFAULT 0,
    s1         INTEGER NOT NULL DEFAULT 0,
    s2         INTEGER NOT NULL DEFAULT 0,
    s3         INTEGER NOT NULL DEFAULT 0,
    s4         INTEGER NOT NULL DEFAULT 0,
    h1         TEXT NOT NULL DEFAULT '',
    h2         TEXT NOT NULL DEFAULT '',
    h3         TEXT NOT NULL DEFAULT '',
    h4         TEXT NOT NULL DEFAULT '',
    i1         TEXT NOT NULL DEFAULT '',
    i2         TEXT NOT NULL DEFAULT '',
    i3         TEXT NOT NULL DEFAULT '',
    i4         TEXT NOT NULL DEFAULT '',
    i5         TEXT NOT NULL DEFAULT '',
    revision   INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- obfuscation_profile_id is the panel's intent (nullable = plain WireGuard); the other
-- columns are what the agent last reported actually running, refreshed every heartbeat.
-- Deleting a profile just un-assigns it: the node falls back to plain WireGuard.
ALTER TABLE nodes ADD COLUMN obfuscation_profile_id UUID REFERENCES obfuscation_profiles(id) ON DELETE SET NULL;
ALTER TABLE nodes ADD COLUMN obfuscation_mode TEXT NOT NULL DEFAULT 'wireguard' CHECK (obfuscation_mode IN ('wireguard', 'awg'));
ALTER TABLE nodes ADD COLUMN obfuscation_applied_profile_id UUID REFERENCES obfuscation_profiles(id) ON DELETE SET NULL;
ALTER TABLE nodes ADD COLUMN obfuscation_applied_revision INTEGER;
-- Whether the node image can run AWG at all (binaries and /dev/net/tun present), and
-- the agent's last apply error, so the UI can say why a node is stuck on plain WireGuard.
ALTER TABLE nodes ADD COLUMN obfuscation_supported BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE nodes ADD COLUMN obfuscation_error TEXT NOT NULL DEFAULT '';
