package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies any migration files under migrations/ that haven't been applied
// yet, in filename order, each inside its own transaction, tracked in a
// schema_migrations table. It's intentionally small and dependency-free rather than
// pulling in a full migration framework for three files.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	names, err := migrationFilenames()
	if err != nil {
		return err
	}

	for _, name := range names {
		var alreadyApplied bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name,
		).Scan(&alreadyApplied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if alreadyApplied {
			continue
		}

		sqlBytes, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		if isNoTxMigration(string(sqlBytes)) {
			if err := s.applyNoTx(ctx, name, string(sqlBytes)); err != nil {
				return err
			}
			continue
		}

		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}

// noTxMarker, as the first line of a migration, makes the runner execute it WITHOUT a
// wrapping transaction. TimescaleDB refuses CREATE MATERIALIZED VIEW ... WITH
// (timescaledb.continuous) and CALL refresh_continuous_aggregate inside a transaction
// block, so those migrations cannot use the default all-or-nothing path.
//
// The trade-off is that such a file is not atomic: a failure midway leaves the earlier
// statements applied and the file unrecorded, so every statement in it MUST be
// idempotent (IF NOT EXISTS / if_not_exists => true) so the next start can re-run it.
// Statements are split on ';', run one at a time (a multi-statement simple query is
// itself an implicit transaction), so a no-tx file must not contain ';' inside string
// literals or comments.
const noTxMarker = "-- +notx"

func isNoTxMigration(sql string) bool {
	first, _, _ := strings.Cut(sql, "\n")
	return strings.TrimSpace(first) == noTxMarker
}

func (s *Store) applyNoTx(ctx context.Context, name, sql string) error {
	for _, stmt := range splitStatements(sql) {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply migration %s (no-tx): %w", name, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return nil
}

// splitStatements drops whole-line `--` comments and splits on ';'.
func splitStatements(sql string) []string {
	var kept []string
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(kept, "\n"), ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, strings.TrimSpace(stmt))
		}
	}
	return out
}

// MigrationsApplied reports whether every embedded migration file has a matching
// row in schema_migrations - used by GET /internal/healthz for readiness.
func (s *Store) MigrationsApplied(ctx context.Context) (bool, error) {
	names, err := migrationFilenames()
	if err != nil {
		return false, err
	}

	var applied int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		// schema_migrations doesn't exist yet if Migrate() hasn't run at all - not ready yet, not a hard error.
		return false, nil
	}
	return applied == len(names), nil
}

func migrationFilenames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
