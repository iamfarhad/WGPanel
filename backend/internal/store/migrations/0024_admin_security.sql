-- Admin sign-in security: TOTP two-factor, one-time recovery codes, WebAuthn passkeys,
-- plus the panel settings for the login lockout and the admin IP allowlist.

-- One TOTP secret per admin. secret_enc is AES-GCM encrypted with
-- ACCOUNT_KEY_ENCRYPTION_KEY. A row with confirmed = false is an enrolment that was
-- started but never proven with a code; it does not gate the login.
CREATE TABLE admin_totp (
    admin_id       UUID PRIMARY KEY REFERENCES admins(id) ON DELETE CASCADE,
    secret_enc     TEXT        NOT NULL,
    confirmed      BOOLEAN     NOT NULL DEFAULT false,
    last_used_step BIGINT      NOT NULL DEFAULT 0, -- blocks replay of an already-used code
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Recovery codes are stored only as SHA-256 hashes (they are 80-bit random values, so a
-- fast hash is sufficient) and are single-use.
CREATE TABLE admin_recovery_codes (
    id         BIGSERIAL PRIMARY KEY,
    admin_id   UUID        NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    code_hash  TEXT        NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX admin_recovery_codes_admin_idx ON admin_recovery_codes (admin_id);

CREATE TABLE admin_passkeys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id        UUID        NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    credential_id   BYTEA       NOT NULL UNIQUE,
    public_key      BYTEA       NOT NULL,
    sign_count      BIGINT      NOT NULL DEFAULT 0,
    aaguid          BYTEA       NOT NULL DEFAULT ''::bytea,
    transports      TEXT[]      NOT NULL DEFAULT '{}',
    attestation_type TEXT       NOT NULL DEFAULT '',
    backup_eligible BOOLEAN     NOT NULL DEFAULT false,
    backup_state    BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ
);
CREATE INDEX admin_passkeys_admin_idx ON admin_passkeys (admin_id);

-- Empty allowlist = no restriction. login_max_failures consecutive failed sign-ins
-- lock the username (and, at 4x that, the client IP) for login_lockout_minutes, doubling
-- on each repeat lockout.
ALTER TABLE panel_settings
    ADD COLUMN admin_ip_allowlist   TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN login_max_failures   INT    NOT NULL DEFAULT 5,
    ADD COLUMN login_lockout_minutes INT   NOT NULL DEFAULT 15;
