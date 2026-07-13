package httpapi

import (
	"context"
	"time"
)

// heartbeatStaleAfter matches the 30s figure in docs/PRD-node-management.md §6.1/§7.
const heartbeatStaleAfter = 30 * time.Second

// RunOfflineSweepLoop periodically flips nodes whose heartbeat has gone stale to
// offline. Runs until ctx is cancelled - started as a goroutine from cmd/api/main.go
// alongside the two HTTP servers.
func (s *Server) RunOfflineSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.Store.SweepOfflineNodes(ctx, heartbeatStaleAfter)
			if err != nil {
				s.Logger.Error("offline_sweep_failed", "error", err)
				continue
			}
			if n > 0 {
				s.Logger.Info("offline_sweep", "nodes_marked_offline", n)
			}
		}
	}
}
