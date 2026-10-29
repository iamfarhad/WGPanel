package store

import (
	"os"
	"testing"

	"wgpanel-api/internal/testdblock"
)

// TestMain queues this package's integration tests behind other packages' ones that
// share WGPANEL_TEST_POSTGRES_DSN (see testdblock). A no-op when the DSN is unset.
func TestMain(m *testing.M) {
	release := testdblock.Acquire()
	code := m.Run()
	release()
	os.Exit(code)
}
