package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// Node maintenance mode. While on, a node:
//   - keeps serving every peer it already has (nothing is removed from its agent's
//     desired state), so an admin can drain it gently;
//   - is skipped by steering (store.SteerCandidatesForAccount), which also drives the
//     subscription's default node pick and the node picker list;
//   - gets no new peers: automatic placement skips it and pinning an account to it is
//     refused (store.CreateAccountWithPeers);
//   - does not raise node.down alerts (threshold_sweep.go).
//
// Peers an account missed during maintenance are filled in when it ends, by the same
// backfill a freshly registered node gets.

func (n *nodeResponse) setMaintenance(since time.Time) {
	n.Maintenance = true
	t := since.Format(time.RFC3339)
	n.MaintenanceSince = &t
}

// nodeResponseWithMaintenance is toNodeResponse plus the maintenance fields; used by
// the single-node handlers (the list handler batches the lookup).
func (s *Server) nodeResponseWithMaintenance(ctx context.Context, n store.Node) nodeResponse {
	resp := toNodeResponse(n)
	if m, err := s.Store.NodeMaintenanceSince(ctx); err == nil {
		if t, ok := m[n.ID]; ok {
			resp.setMaintenance(t)
		}
	}
	return resp
}

type setMaintenanceRequest struct {
	Enabled *bool `json:"enabled"`
}

// handleSetNodeMaintenance is operator-or-above, like editing a node.
func (s *Server) handleSetNodeMaintenance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setMaintenanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "enabled (boolean) is required")
		return
	}
	ctx := r.Context()
	name, changed, _, err := s.Store.SetNodeMaintenance(ctx, id, *req.Enabled)
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if err != nil {
		s.Logger.Error("set_node_maintenance_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not change maintenance mode")
		return
	}

	if changed {
		action, evType := "node.maintenance_started", events.NodeMaintStarted
		if !*req.Enabled {
			action, evType = "node.maintenance_ended", events.NodeMaintEnded
			// Accounts created while the node was out of rotation have no peer on it yet.
			if err := s.Store.BackfillAccountPeersForNode(ctx, id); err != nil {
				s.Logger.Error("maintenance_backfill_failed", "node_id", id, "error", err)
			}
		}
		if admin, ok := adminFromContext(ctx); ok {
			if err := s.Store.InsertAuditLog(ctx, admin.Username, action, name, nil, clientIP(r)); err != nil {
				s.Logger.Error("audit_log_failed", "error", err)
			}
		}
		s.emit(evType, "", map[string]any{"node_id": id, "node_name": name})
	}

	node, err := s.Store.GetNode(ctx, id)
	if err != nil {
		s.Logger.Error("get_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node")
		return
	}
	writeJSON(w, http.StatusOK, s.nodeResponseWithMaintenance(ctx, node))
}
