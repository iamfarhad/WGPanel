// Package testdblock lets test binaries that share one Postgres (the integration tests
// behind WGPANEL_TEST_POSTGRES_DSN) take turns.
//
// `go test ./...` runs each package's test binary in parallel, but those tests all
// migrate and mutate the same database, and one of them (the backup round trip)
// replaces its entire contents. Without serialising them they trample each other:
// concurrent first migrations fail, and accounts vanish mid-test. Each such package
// calls Acquire from TestMain; the Postgres advisory lock it takes is held for that
// package's whole run, so the packages queue instead of overlapping, whatever -p says.
package testdblock

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// lockKey is an arbitrary constant every participating package agrees on.
const lockKey int64 = 0x57475041 // "WGPA"

// Acquire blocks until it holds the lock and returns the function that releases it.
// With WGPANEL_TEST_POSTGRES_DSN unset (the common case: no integration tests will
// run) it does nothing. If the database cannot be reached it warns and carries on
// unlocked - the tests themselves will then report the connection problem.
func Acquire() (release func()) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		return func() {}
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdblock: not serialising against other packages: %v\n", err)
		return func() {}
	}
	// A package waiting its turn sits here; a crashed holder drops the session and with
	// it the lock, so this cannot wait forever on a dead process.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		fmt.Fprintf(os.Stderr, "testdblock: lock failed: %v\n", err)
		conn.Close(ctx)
		return func() {}
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(rctx, `SELECT pg_advisory_unlock($1)`, lockKey)
		conn.Close(rctx)
	}
}
