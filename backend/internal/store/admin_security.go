package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// SecuritySettings are the admin sign-in hardening knobs in panel_settings
// (migration 0024). Kept apart from PanelSettings so the general Settings code path
// is untouched.
type SecuritySettings struct {
	AdminIPAllowlist    []string
	LoginMaxFailures    int
	LoginLockoutMinutes int
}

func (s *Store) GetSecuritySettings(ctx context.Context) (SecuritySettings, error) {
	var p SecuritySettings
	err := s.pool.QueryRow(ctx, `
		SELECT admin_ip_allowlist, login_max_failures, login_lockout_minutes
		FROM panel_settings WHERE id = 1
	`).Scan(&p.AdminIPAllowlist, &p.LoginMaxFailures, &p.LoginLockoutMinutes)
	if p.AdminIPAllowlist == nil {
		p.AdminIPAllowlist = []string{}
	}
	return p, err
}

// UpdateSecuritySettings: nil leaves a field unchanged; a non-nil (possibly empty)
// allowlist replaces it.
func (s *Store) UpdateSecuritySettings(ctx context.Context, allowlist []string, maxFailures, lockoutMinutes *int) (SecuritySettings, error) {
	_, err := s.pool.Exec(ctx, `
		UPDATE panel_settings SET
			admin_ip_allowlist = COALESCE($1::text[], admin_ip_allowlist),
			login_max_failures = COALESCE($2, login_max_failures),
			login_lockout_minutes = COALESCE($3, login_lockout_minutes),
			updated_at = now()
		WHERE id = 1
	`, allowlist, maxFailures, lockoutMinutes)
	if err != nil {
		return SecuritySettings{}, err
	}
	return s.GetSecuritySettings(ctx)
}

// ClearAdminIPAllowlist is the break-glass path (`wgpanel clear-ip-allowlist`).
func (s *Store) ClearAdminIPAllowlist(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE panel_settings SET admin_ip_allowlist = '{}', updated_at = now() WHERE id = 1`)
	return err
}

// ---- TOTP ----

var ErrTOTPNotFound = errors.New("totp not enrolled")

type AdminTOTP struct {
	AdminID      string
	SecretEnc    string
	Confirmed    bool
	LastUsedStep int64
}

func (s *Store) GetAdminTOTP(ctx context.Context, adminID string) (AdminTOTP, error) {
	var t AdminTOTP
	err := s.pool.QueryRow(ctx, `
		SELECT admin_id, secret_enc, confirmed, last_used_step FROM admin_totp WHERE admin_id = $1
	`, adminID).Scan(&t.AdminID, &t.SecretEnc, &t.Confirmed, &t.LastUsedStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminTOTP{}, ErrTOTPNotFound
	}
	return t, err
}

// SetPendingTOTP stores a fresh unconfirmed secret, replacing any earlier unconfirmed
// one. It never overwrites a confirmed enrolment (returns false in that case).
func (s *Store) SetPendingTOTP(ctx context.Context, adminID, secretEnc string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO admin_totp (admin_id, secret_enc) VALUES ($1, $2)
		ON CONFLICT (admin_id) DO UPDATE SET secret_enc = EXCLUDED.secret_enc, created_at = now(), last_used_step = 0
		WHERE admin_totp.confirmed = false
	`, adminID, secretEnc)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ConfirmTOTP flips the pending enrolment to active and replaces the recovery codes
// atomically. step is the accepted code's time step (replay guard).
func (s *Store) ConfirmTOTP(ctx context.Context, adminID string, step int64, recoveryHashes []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE admin_totp SET confirmed = true, last_used_step = $2 WHERE admin_id = $1 AND confirmed = false`, adminID, step)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrTOTPNotFound
	}
	if err := replaceRecoveryCodes(ctx, tx, adminID, recoveryHashes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdvanceTOTPStep records a used step; false means the step was already used (replay).
func (s *Store) AdvanceTOTPStep(ctx context.Context, adminID string, step int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE admin_totp SET last_used_step = $2 WHERE admin_id = $1 AND last_used_step < $2`, adminID, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// DisableTOTP removes the secret and the recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, adminID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM admin_totp WHERE admin_id = $1`, adminID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, adminID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, adminID string, hashes []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, adminID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO admin_recovery_codes (admin_id, code_hash) VALUES ($1, $2)`, adminID, h); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReplaceRecoveryCodes(ctx context.Context, adminID string, hashes []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := replaceRecoveryCodes(ctx, tx, adminID, hashes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConsumeRecoveryCode marks a matching unused code as used; false when none matched.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, adminID, codeHash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE admin_recovery_codes SET used_at = now()
		WHERE id = (SELECT id FROM admin_recovery_codes WHERE admin_id = $1 AND code_hash = $2 AND used_at IS NULL LIMIT 1)
	`, adminID, codeHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) CountRecoveryCodes(ctx context.Context, adminID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE admin_id = $1 AND used_at IS NULL`, adminID).Scan(&n)
	return n, err
}

// ---- passkeys ----

var ErrPasskeyNotFound = errors.New("passkey not found")

type Passkey struct {
	ID              string
	AdminID         string
	Name            string
	CredentialID    []byte
	PublicKey       []byte
	SignCount       int64
	AAGUID          []byte
	Transports      []string
	AttestationType string
	BackupEligible  bool
	BackupState     bool
	CreatedAt       time.Time
	LastUsedAt      *time.Time
}

const passkeyCols = `id, admin_id, name, credential_id, public_key, sign_count, aaguid, transports, attestation_type, backup_eligible, backup_state, created_at, last_used_at`

func scanPasskey(row pgx.Row) (Passkey, error) {
	var p Passkey
	err := row.Scan(&p.ID, &p.AdminID, &p.Name, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.AAGUID, &p.Transports, &p.AttestationType, &p.BackupEligible, &p.BackupState, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Passkey{}, ErrPasskeyNotFound
	}
	return p, err
}

func (s *Store) CreatePasskey(ctx context.Context, p Passkey) (Passkey, error) {
	if p.Transports == nil {
		p.Transports = []string{}
	}
	return scanPasskey(s.pool.QueryRow(ctx, `
		INSERT INTO admin_passkeys (admin_id, name, credential_id, public_key, sign_count, aaguid, transports, attestation_type, backup_eligible, backup_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+passkeyCols,
		p.AdminID, p.Name, p.CredentialID, p.PublicKey, p.SignCount, p.AAGUID, p.Transports, p.AttestationType, p.BackupEligible, p.BackupState))
}

func (s *Store) ListPasskeys(ctx context.Context, adminID string) ([]Passkey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+passkeyCols+` FROM admin_passkeys WHERE admin_id = $1 ORDER BY created_at`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPasskeyByCredentialID(ctx context.Context, credID []byte) (Passkey, error) {
	return scanPasskey(s.pool.QueryRow(ctx, `SELECT `+passkeyCols+` FROM admin_passkeys WHERE credential_id = $1`, credID))
}

// TouchPasskey stores the new sign count / backup state after a successful assertion.
func (s *Store) TouchPasskey(ctx context.Context, id string, signCount int64, backupState bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE admin_passkeys SET sign_count = $2, backup_state = $3, last_used_at = now() WHERE id = $1`, id, signCount, backupState)
	return err
}

// DeletePasskey removes a passkey owned by adminID (ErrPasskeyNotFound otherwise).
func (s *Store) DeletePasskey(ctx context.Context, adminID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM admin_passkeys WHERE id::text = $1 AND admin_id = $2`, id, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPasskeyNotFound
	}
	return nil
}

// ResetAdminMFA removes every second factor of an admin (TOTP, recovery codes, passkeys).
func (s *Store) ResetAdminMFA(ctx context.Context, adminID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM admin_totp WHERE admin_id = $1`,
		`DELETE FROM admin_recovery_codes WHERE admin_id = $1`,
		`DELETE FROM admin_passkeys WHERE admin_id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, adminID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// MFAStatus is the per-admin 2FA summary shown on the Admin Users page.
type MFAStatus struct {
	TOTPEnabled  bool
	PasskeyCount int
}

// MFAStatusByAdmin returns the summary for every admin that has any factor.
func (s *Store) MFAStatusByAdmin(ctx context.Context) (map[string]MFAStatus, error) {
	out := map[string]MFAStatus{}
	rows, err := s.pool.Query(ctx, `SELECT admin_id FROM admin_totp WHERE confirmed`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		st := out[id]
		st.TOTPEnabled = true
		out[id] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT admin_id, count(*) FROM admin_passkeys GROUP BY admin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		st := out[id]
		st.PasskeyCount = n
		out[id] = st
	}
	return out, rows.Err()
}

// HasSecondFactor reports whether the admin has a confirmed TOTP and/or passkeys.
func (s *Store) HasSecondFactor(ctx context.Context, adminID string) (totp bool, passkeys int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM admin_totp WHERE admin_id = $1 AND confirmed),
		       (SELECT count(*) FROM admin_passkeys WHERE admin_id = $1)
	`, adminID).Scan(&totp, &passkeys)
	return
}
