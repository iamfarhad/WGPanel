package httpapi

import (
	"context"
	"testing"

	"wgpanel-api/internal/store"
)

// relaxAPIRate lifts the Bot API per-key limit for an integration test that fires many
// requests in a burst, and restores the defaults afterwards.
func relaxAPIRate(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	big, bigBurst := 10000, 100000
	if _, err := st.UpdateSettings(ctx, store.UpdateSettingsParams{APIRateRPS: &big, APIRateBurst: &bigBurst}); err != nil {
		t.Fatal(err)
	}
	resetAPIRateCache()
	t.Cleanup(func() {
		rps, burst := defaultAPIRateRPS, defaultAPIRateBurst
		_, _ = st.UpdateSettings(context.Background(), store.UpdateSettingsParams{APIRateRPS: &rps, APIRateBurst: &burst})
		resetAPIRateCache()
	})
}
