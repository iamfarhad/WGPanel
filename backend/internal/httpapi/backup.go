// Backup & restore via the panel (Settings -> Backup & restore): a single JSON
// file containing every config table plus the node-mTLS CA keypair - everything
// needed to rebuild the panel on a fresh server, EXCEPT deploy/.env. The backup is
// deliberately useless without that .env: account private keys inside it are
// still encrypted with ACCOUNT_KEY_ENCRYPTION_KEY (a key-canary field lets restore
// detect a mismatch up front instead of every config download failing later).
//
// The file is as sensitive as the database itself (admin password hashes, the CA
// private key, subscription tokens) - it is only ever produced for, and accepted
// from, a super_admin, and both directions are audit-logged.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"wgpanel-api/internal/wgkeys"
)

const backupFormat = "wgpanel-backup/1"

// backupCanaryPlaintext is a fixed string encrypted with ACCOUNT_KEY_ENCRYPTION_KEY
// into every backup; restore decrypts it to prove the current deployment holds the
// same key the backed-up account private keys were encrypted with.
const backupCanaryPlaintext = "wgpanel-key-canary"

type backupCA struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

type backupFile struct {
	Format     string                     `json:"format"`
	CreatedAt  string                     `json:"created_at"`
	Migrations []string                   `json:"migrations"`
	KeyCanary  string                     `json:"key_canary"`
	CA         *backupCA                  `json:"ca,omitempty"`
	Tables     map[string]json.RawMessage `json:"tables"`
}

// handleDownloadBackup streams the full panel backup as an attachment.
// super_admin-only (wired via requireRole in server.go).
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	migrations, err := s.Store.AppliedMigrations(ctx)
	if err != nil {
		s.Logger.Error("backup_migrations_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read schema version")
		return
	}
	tables, err := s.Store.DumpTables(ctx)
	if err != nil {
		s.Logger.Error("backup_dump_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not dump tables")
		return
	}
	canary, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, backupCanaryPlaintext)
	if err != nil {
		s.Logger.Error("backup_canary_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not build backup")
		return
	}

	backup := backupFile{
		Format:     backupFormat,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Migrations: migrations,
		KeyCanary:  canary,
		CA:         s.readCAForBackup(),
		Tables:     tables,
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "backup.downloaded", "panel", nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	filename := "wgpanel-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(backup); err != nil {
		s.Logger.Error("backup_write_failed", "error", err)
	}
}

// readCAForBackup best-effort reads the CA keypair off disk. A deployment without
// one (files missing, no volume) just produces a backup without a ca section -
// restoring such a backup simply leaves the current CA in place.
func (s *Server) readCAForBackup() *backupCA {
	if s.CADataDir == "" {
		return nil
	}
	cert, err := os.ReadFile(filepath.Join(s.CADataDir, "ca-cert.pem"))
	if err != nil {
		s.Logger.Warn("backup_ca_cert_read_failed", "error", err)
		return nil
	}
	key, err := os.ReadFile(filepath.Join(s.CADataDir, "ca-key.pem"))
	if err != nil {
		s.Logger.Warn("backup_ca_key_read_failed", "error", err)
		return nil
	}
	return &backupCA{CertPEM: string(cert), KeyPEM: string(key)}
}

type restoreBackupResponse struct {
	Restored map[string]int `json:"restored"`
	// CARestored is true when the backup carried a CA keypair and it was written
	// to disk. RestartRequired additionally means that keypair differs from the
	// one this API loaded at startup - node-agent mTLS keeps using the old CA
	// until the api container is restarted (`wgpanel restart`).
	CARestored      bool `json:"ca_restored"`
	RestartRequired bool `json:"restart_required"`
}

// handleRestoreBackup replaces ALL panel state with an uploaded backup file.
// super_admin-only. The three validations happen before anything is touched, in
// increasing order of specificity: file shape, schema version, encryption key.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Config tables without metrics history stay small; 256MB is far above any
	// realistic backup while still bounding a hostile upload.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)

	var backup backupFile
	if err := json.NewDecoder(r.Body).Decode(&backup); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request", "backup file exceeds the 256MB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "not a valid JSON backup file")
		return
	}
	if backup.Format != backupFormat {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("unrecognized backup format %q (expected %q)", backup.Format, backupFormat))
		return
	}

	migrations, err := s.Store.AppliedMigrations(ctx)
	if err != nil {
		s.Logger.Error("restore_migrations_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read schema version")
		return
	}
	if !slices.Equal(backup.Migrations, migrations) {
		// Exact match, not prefix-compatibility: json_populate_recordset against a
		// drifted schema fails in data-dependent ways (or silently NULLs columns),
		// so "same panel version as the backup" is the only safe contract.
		writeJSONError(w, http.StatusConflict, "schema_mismatch",
			"this backup was taken on a different panel version - run the same version the backup came from, then restore")
		return
	}

	if plaintext, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, backup.KeyCanary); err != nil || plaintext != backupCanaryPlaintext {
		writeJSONError(w, http.StatusConflict, "encryption_key_mismatch",
			"this backup's account keys were encrypted with a different ACCOUNT_KEY_ENCRYPTION_KEY - set the original key in deploy/.env before restoring")
		return
	}

	counts, err := s.Store.RestoreTables(ctx, backup.Tables)
	if err != nil {
		s.Logger.Error("restore_tables_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "restore failed - no changes were applied: "+err.Error())
		return
	}

	resp := restoreBackupResponse{Restored: counts}
	if backup.CA != nil && s.CADataDir != "" {
		if err := s.writeRestoredCA(*backup.CA); err != nil {
			// The database restore already committed - report the partial outcome
			// honestly rather than failing the whole request after the point of no
			// return. The old CA keeps working meanwhile.
			s.Logger.Error("restore_ca_write_failed", "error", err)
		} else {
			resp.CARestored = true
			resp.RestartRequired = !bytes.Equal(bytes.TrimSpace(s.CA.CertPEM), bytes.TrimSpace([]byte(backup.CA.CertPEM)))
		}
	}

	// Written after RestoreTables so this lands in the restored audit_log - the
	// restore event must be visible in the timeline the panel now shows.
	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "backup.restored", "panel", counts, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) writeRestoredCA(ca backupCA) error {
	if err := os.MkdirAll(s.CADataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.CADataDir, "ca-cert.pem"), []byte(ca.CertPEM), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.CADataDir, "ca-key.pem"), []byte(ca.KeyPEM), 0o600)
}
