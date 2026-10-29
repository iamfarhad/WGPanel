package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"wgpanel-api/internal/store"
)

// Per-node entry endpoints (migration 0027). Staff-only: none of these routes are on
// the reseller allowlist; resellers just benefit from the endpoint choice made inside
// renderPeerConfig.

const (
	endpointKindDirect = "direct"
	endpointKindRelay  = "relay"
	healthNone         = "none"
	healthTCP          = "tcp"

	// endpointHealthMaxAge is how old a "up" verdict may be before the endpoint stops
	// being preferred: a dead sweep must degrade to the node's public_endpoint, not keep
	// routing clients to a relay nobody has verified for a long while.
	endpointHealthMaxAge = 15 * time.Minute

	minClientMTU = 1200
	maxClientMTU = 1420
)

type endpointResponse struct {
	ID            string  `json:"id"`
	NodeID        string  `json:"node_id"`
	Label         string  `json:"label"`
	Address       string  `json:"address"`
	Kind          string  `json:"kind"`
	Priority      int     `json:"priority"`
	Enabled       bool    `json:"enabled"`
	MTU           *int    `json:"mtu"`
	HealthCheck   string  `json:"health_check"`
	LastStatus    string  `json:"last_status"`
	LastCheckedAt *string `json:"last_checked_at"`
	LastLatencyMs *int    `json:"last_latency_ms"`
	CreatedAt     string  `json:"created_at"`
}

func toEndpointResponse(e store.NodeEndpoint) endpointResponse {
	r := endpointResponse{
		ID: e.ID, NodeID: e.NodeID, Label: e.Label, Address: e.Address, Kind: e.Kind,
		Priority: e.Priority, Enabled: e.Enabled, MTU: e.MTU, HealthCheck: e.HealthCheck,
		LastStatus: e.LastStatus, LastLatencyMs: e.LastLatencyMs,
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
	}
	if e.LastCheckedAt != nil {
		v := e.LastCheckedAt.Format(time.RFC3339)
		r.LastCheckedAt = &v
	}
	return r
}

// endpointUsable reports whether an endpoint may be handed to clients automatically:
// enabled, and either unchecked by design (health_check none) or recently verified up.
func endpointUsable(e store.NodeEndpoint, now time.Time) bool {
	if !e.Enabled {
		return false
	}
	if e.HealthCheck == healthNone {
		return true
	}
	return e.LastStatus == "up" && e.LastCheckedAt != nil && now.Sub(*e.LastCheckedAt) <= endpointHealthMaxAge
}

// pickEndpoint returns the highest-priority usable endpoint (ties: the earliest
// created), or nil when the node's own public_endpoint should be used.
func pickEndpoint(eps []store.NodeEndpoint, now time.Time) *store.NodeEndpoint {
	var best *store.NodeEndpoint
	for i := range eps {
		e := &eps[i]
		if !endpointUsable(*e, now) {
			continue
		}
		if best == nil || e.Priority > best.Priority ||
			(e.Priority == best.Priority && e.CreatedAt.Before(best.CreatedAt)) {
			best = e
		}
	}
	return best
}

// normalizeEndpointAddress validates host:port the same way a node's public_endpoint
// is validated, with endpoint-specific wording.
func normalizeEndpointAddress(raw string) (string, error) {
	addr, err := normalizePublicEndpoint(raw)
	if err != nil {
		return "", errors.New("address must be host:port, e.g. relay.example.ir:51820 (IPv6: [2001:db8::1]:51820), with a port between 1 and 65535 and no scheme or path")
	}
	return addr, nil
}

func validEndpointKind(k string) bool { return k == endpointKindDirect || k == endpointKindRelay }
func validHealthMode(m string) bool   { return m == healthNone || m == healthTCP }

func validMTU(v int) bool { return v >= minClientMTU && v <= maxClientMTU }

type createEndpointRequest struct {
	Label       string `json:"label"`
	Address     string `json:"address"`
	Kind        string `json:"kind"`
	Priority    *int   `json:"priority"`
	Enabled     *bool  `json:"enabled"`
	MTU         *int   `json:"mtu"`
	HealthCheck string `json:"health_check"`
}

func (s *Server) handleListNodeEndpoints(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	if _, err := s.Store.GetNode(r.Context(), nodeID); errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node")
		return
	}
	eps, err := s.Store.ListNodeEndpoints(r.Context(), nodeID)
	if err != nil {
		s.Logger.Error("list_node_endpoints_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list endpoints")
		return
	}
	out := make([]endpointResponse, 0, len(eps))
	for _, e := range eps {
		out = append(out, toEndpointResponse(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": out})
}

func (s *Server) handleCreateNodeEndpoint(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	var req createEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Label == "" || len(req.Label) > 64 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "label is required (max 64 characters)")
		return
	}
	addr, err := normalizeEndpointAddress(req.Address)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Kind == "" {
		req.Kind = endpointKindRelay
	}
	if !validEndpointKind(req.Kind) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "kind must be direct or relay")
		return
	}
	if req.HealthCheck == "" {
		req.HealthCheck = healthNone
	}
	if !validHealthMode(req.HealthCheck) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "health_check must be none or tcp")
		return
	}
	if req.MTU != nil && !validMTU(*req.MTU) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "mtu must be between 1200 and 1420")
		return
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	ctx := r.Context()
	ep, err := s.Store.CreateNodeEndpoint(ctx, store.CreateNodeEndpointParams{
		NodeID: nodeID, Label: req.Label, Address: addr, Kind: req.Kind, Priority: priority,
		Enabled: enabled, MTU: req.MTU, HealthCheck: req.HealthCheck,
	})
	if s.writeEndpointStoreError(w, err, "create") {
		return
	}
	s.auditEndpoint(r, "node.endpoint_created", nodeID, ep.Label)
	writeJSON(w, http.StatusCreated, toEndpointResponse(ep))
}

// writeEndpointStoreError maps store errors to responses; true = a response was written.
func (s *Server) writeEndpointStoreError(w http.ResponseWriter, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNodeNotFound):
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
	case errors.Is(err, store.ErrEndpointNotFound):
		writeJSONError(w, http.StatusNotFound, "endpoint_not_found", "no endpoint with that id on this node")
	case errors.Is(err, store.ErrEndpointLabelTaken):
		writeJSONError(w, http.StatusConflict, "endpoint_label_taken", "this node already has an endpoint with that label")
	default:
		s.Logger.Error("node_endpoint_"+what+"_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not "+what+" endpoint")
	}
	return true
}

func (s *Server) auditEndpoint(r *http.Request, action, nodeID, label string) {
	ctx := r.Context()
	admin, ok := adminFromContext(ctx)
	if !ok {
		return
	}
	if err := s.Store.InsertAuditLog(ctx, admin.Username, action, nodeID+"/"+label, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}

type updateEndpointRequest struct {
	Label       *string `json:"label"`
	Address     *string `json:"address"`
	Kind        *string `json:"kind"`
	Priority    *int    `json:"priority"`
	Enabled     *bool   `json:"enabled"`
	MTU         *int    `json:"mtu"`
	ClearMTU    bool    `json:"clear_mtu"`
	HealthCheck *string `json:"health_check"`
}

func (s *Server) handleUpdateNodeEndpoint(w http.ResponseWriter, r *http.Request) {
	nodeID, id := r.PathValue("id"), r.PathValue("endpointId")
	var req updateEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Label != nil && (*req.Label == "" || len(*req.Label) > 64) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "label must be 1-64 characters")
		return
	}
	if req.Address != nil {
		addr, err := normalizeEndpointAddress(*req.Address)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		req.Address = &addr
	}
	if req.Kind != nil && !validEndpointKind(*req.Kind) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "kind must be direct or relay")
		return
	}
	if req.HealthCheck != nil && !validHealthMode(*req.HealthCheck) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "health_check must be none or tcp")
		return
	}
	if req.MTU != nil && !validMTU(*req.MTU) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "mtu must be between 1200 and 1420")
		return
	}
	ep, err := s.Store.UpdateNodeEndpoint(r.Context(), nodeID, id, store.UpdateNodeEndpointParams{
		Label: req.Label, Address: req.Address, Kind: req.Kind, Priority: req.Priority, Enabled: req.Enabled,
		MTU: req.MTU, ClearMTU: req.ClearMTU, HealthCheck: req.HealthCheck,
	})
	if s.writeEndpointStoreError(w, err, "update") {
		return
	}
	s.auditEndpoint(r, "node.endpoint_updated", nodeID, ep.Label)
	writeJSON(w, http.StatusOK, toEndpointResponse(ep))
}

func (s *Server) handleDeleteNodeEndpoint(w http.ResponseWriter, r *http.Request) {
	nodeID, id := r.PathValue("id"), r.PathValue("endpointId")
	ep, err := s.Store.GetNodeEndpoint(r.Context(), nodeID, id)
	if s.writeEndpointStoreError(w, err, "delete") {
		return
	}
	if s.writeEndpointStoreError(w, s.Store.DeleteNodeEndpoint(r.Context(), nodeID, id), "delete") {
		return
	}
	s.auditEndpoint(r, "node.endpoint_deleted", nodeID, ep.Label)
	w.WriteHeader(http.StatusNoContent)
}

// handleCheckNodeEndpoint runs one health check immediately ("test now" in the UI) and
// returns the refreshed endpoint. A check on an endpoint with health_check none is a
// 409 - there is nothing to run.
func (s *Server) handleCheckNodeEndpoint(w http.ResponseWriter, r *http.Request) {
	nodeID, id := r.PathValue("id"), r.PathValue("endpointId")
	ctx := r.Context()
	ep, err := s.Store.GetNodeEndpoint(ctx, nodeID, id)
	if s.writeEndpointStoreError(w, err, "check") {
		return
	}
	if ep.HealthCheck == healthNone {
		writeJSONError(w, http.StatusConflict, "endpoint_check_disabled", "this endpoint has health checks turned off")
		return
	}
	if err := s.checkEndpoint(ctx, ep); err != nil {
		s.Logger.Error("endpoint_check_record_failed", "error", err)
	}
	ep, err = s.Store.GetNodeEndpoint(ctx, nodeID, id)
	if s.writeEndpointStoreError(w, err, "check") {
		return
	}
	writeJSON(w, http.StatusOK, toEndpointResponse(ep))
}

// ---- health checks ----

const (
	endpointCheckTimeout   = 3 * time.Second
	endpointCheckInterval  = 60 * time.Second // minimum age between two checks of one endpoint
	endpointCheckWorkers   = 8
	endpointCheckSweepTick = 15 * time.Second
)

// tcpProbe dials address and reports reachability and connect latency. Overridable in
// tests.
var tcpProbe = func(ctx context.Context, address string) (bool, time.Duration) {
	d := net.Dialer{Timeout: endpointCheckTimeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return false, 0
	}
	_ = c.Close()
	return true, time.Since(start)
}

// checkEndpoint probes one endpoint per its health mode and records the verdict.
func (s *Server) checkEndpoint(ctx context.Context, ep store.NodeEndpoint) error {
	if ep.HealthCheck != healthTCP {
		return nil
	}
	ok, latency := tcpProbe(ctx, ep.Address)
	status := "down"
	var ms *int
	if ok {
		status = "up"
		v := int(latency.Milliseconds())
		ms = &v
	}
	return s.Store.RecordEndpointHealth(ctx, ep.ID, ep.Address, status, ms)
}

// RunEndpointHealthLoop probes due endpoints on a slow cadence with bounded
// concurrency, so a pile of dead relays (each costing a full timeout) can neither stall
// the loop for long nor flood anything: each endpoint is probed at most once per
// endpointCheckInterval, with one TCP connect per probe.
func (s *Server) RunEndpointHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(endpointCheckSweepTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepEndpointHealth(ctx)
		}
	}
}

func (s *Server) sweepEndpointHealth(ctx context.Context) {
	due, err := s.Store.ListEndpointsToCheck(ctx, endpointCheckInterval)
	if err != nil {
		s.Logger.Error("endpoint_health_list_failed", "error", err)
		return
	}
	if len(due) == 0 {
		return
	}
	sem := make(chan struct{}, endpointCheckWorkers)
	var wg sync.WaitGroup
	for _, ep := range due {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := s.checkEndpoint(ctx, ep); err != nil {
				s.Logger.Error("endpoint_health_record_failed", "error", err)
			}
		}()
	}
	wg.Wait()
}
