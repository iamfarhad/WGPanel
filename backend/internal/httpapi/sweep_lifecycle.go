package httpapi

import (
	"context"
	"time"
)

// lifecycleSweepEveryNTicks runs the lifecycle sweep about once a minute (the loop ticks
// every 5s): reset schedules are measured in days, a minute of lateness is irrelevant.
const lifecycleSweepEveryNTicks = 12

// resetBatch bounds one sweep's resets so a backlog (e.g. after downtime) is worked off
// over several ticks instead of one long transaction.
const resetBatch = 200

// runLifecycleSweep activates on-hold accounts whose deadline passed and performs due
// scheduled usage resets, writing an audit row for each (actor "system").
func (s *Server) runLifecycleSweep(ctx context.Context) {
	activated, err := s.Store.ActivateOverdueHolds(ctx)
	if err != nil {
		s.Logger.Error("hold_deadline_sweep_failed", "error", err)
	}
	for _, a := range activated {
		_ = s.Store.InsertAuditLog(ctx, "system", "account.activated", a.ID,
			map[string]any{"trigger": a.Trigger, "expiry_at": a.ExpiryAt.Format(time.RFC3339)}, "")
	}

	for {
		resets, err := s.Store.ResetDueUsage(ctx, resetBatch)
		if err != nil {
			s.Logger.Error("scheduled_reset_failed", "error", err)
			return
		}
		for _, r := range resets {
			_ = s.Store.InsertAuditLog(ctx, "system", "account.usage_reset", r.AccountID, map[string]any{
				"scheduled":           true,
				"strategy":            r.Strategy,
				"previous_used_bytes": r.PreviousUsedBytes,
				"next_reset_at":       r.NextResetAt.Format(time.RFC3339),
			}, "")
		}
		if len(resets) < resetBatch {
			return
		}
	}
}
