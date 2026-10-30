package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"wgpanel-api/internal/routing"
	"wgpanel-api/internal/store"
)

// Split-tunnel routing, per-account PresharedKey and per-node IPv6 (migration 0029):
// request/response shapes and validation shared by accounts, bulk create and plan
// templates, plus the node IPv6 endpoints. The renderer side is routing_render.go, the
// agent side routing_agent.go.

// routingRequest is the routing part of a create/update request body. It is embedded
// (so its JSON fields sit flat next to the others) in createAccountRequest,
// updateAccountRequest and templateRequest.
type routingRequest struct {
	// RoutingMode: full | bypass_iran | custom. Omitted/null = inherit the template's
	// (create) or leave unchanged (update); on update "" returns the account to the
	// default (full tunnel).
	RoutingMode *string `json:"routing_mode"`
	// RoutingExcludedCIDRs: the IPv4 CIDRs kept OUT of the tunnel, used by mode custom.
	RoutingExcludedCIDRs *[]string `json:"routing_excluded_cidrs"`
	// RoutingPrecision: compact (default; AllowedIPs capped near 1000 IPv4 entries) or
	// exact (every prefix, ~8000 entries for bypass_iran). On update "" clears it.
	RoutingPrecision *string `json:"routing_precision"`
}

// routingResponse is the routing part of account and template responses.
type routingResponse struct {
	// RoutingMode is null for an account on the default (full tunnel).
	RoutingMode          *string  `json:"routing_mode"`
	RoutingExcludedCIDRs []string `json:"routing_excluded_cidrs"`
	RoutingPrecision     *string  `json:"routing_precision"`
}

func routingResponseOf(mode *string, cidrs []string, precision *string) routingResponse {
	if cidrs == nil {
		cidrs = []string{}
	}
	return routingResponse{RoutingMode: mode, RoutingExcludedCIDRs: cidrs, RoutingPrecision: precision}
}

// resolvedRouting is a validated routing setting ready to store.
type resolvedRouting struct {
	Mode      *string
	CIDRs     []string
	Precision *string
}

// normalizeExcludedCIDRs validates and canonicalises the admin's list: IPv4 only, masked,
// de-duplicated, bounded, and never 0.0.0.0/0 (excluding everything leaves a tunnel that
// carries nothing - almost certainly a mistake).
func normalizeExcludedCIDRs(in []string) ([]string, *apiError) {
	ps, err := routing.ParseIPv4CIDRs(in)
	if err != nil {
		return nil, badRequest("routing_excluded_cidrs: " + err.Error())
	}
	if len(ps) > routing.MaxCustomExcluded {
		return nil, badRequest(fmt.Sprintf("routing_excluded_cidrs may list at most %d CIDRs", routing.MaxCustomExcluded))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Bits() == 0 {
			return nil, badRequest("routing_excluded_cidrs must not contain 0.0.0.0/0: that would keep every IPv4 address out of the tunnel")
		}
		if k := p.String(); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// resolveRoutingForCreate applies "explicit request field > template value > default"
// and validates the combination. Pure, so it is unit-testable.
func resolveRoutingForCreate(req routingRequest, tmpl *store.PlanTemplate) (resolvedRouting, *apiError) {
	var out resolvedRouting
	var tmplMode, tmplPrecision *string
	var tmplCIDRs []string
	if tmpl != nil {
		tmplMode, tmplCIDRs, tmplPrecision = tmpl.RoutingMode, tmpl.RoutingExcludedCIDRs, tmpl.RoutingPrecision
	}

	mode := tmplMode
	if req.RoutingMode != nil && *req.RoutingMode != "" {
		if !routing.ValidMode(*req.RoutingMode) {
			return out, badRequest("routing_mode must be full, bypass_iran or custom")
		}
		m := *req.RoutingMode
		mode = &m
	}
	var cidrs []string
	switch {
	case req.RoutingExcludedCIDRs != nil:
		cidrs = *req.RoutingExcludedCIDRs
	case tmplMode != nil && mode != nil && *tmplMode == *mode:
		cidrs = tmplCIDRs // same mode as the template: its list comes along
	}
	if mode != nil && *mode == string(routing.ModeCustom) {
		if len(cidrs) == 0 {
			return out, badRequest("routing_mode custom needs routing_excluded_cidrs")
		}
		n, aerr := normalizeExcludedCIDRs(cidrs)
		if aerr != nil {
			return out, aerr
		}
		out.CIDRs = n
	} else if req.RoutingExcludedCIDRs != nil && len(*req.RoutingExcludedCIDRs) > 0 {
		return out, badRequest("routing_excluded_cidrs is only used with routing_mode custom")
	}
	out.Mode = mode

	precision := tmplPrecision
	if req.RoutingPrecision != nil && *req.RoutingPrecision != "" {
		if !routing.ValidPrecision(*req.RoutingPrecision) {
			return out, badRequest("routing_precision must be compact or exact")
		}
		p := *req.RoutingPrecision
		precision = &p
	}
	out.Precision = precision
	return out, nil
}

// templateRouting validates the routing fields of a template body.
func (r routingRequest) templateRouting() (resolvedRouting, *apiError) {
	return resolveRoutingForCreate(r, nil)
}

// prepareRoutingUpdate validates a PATCH's routing fields against the account's current
// settings and returns the store update, or nil when the body carries none. It runs
// BEFORE the rest of the PATCH is applied, so a bad routing value cannot leave a
// half-applied update behind.
func (s *Server) prepareRoutingUpdate(ctx context.Context, accountID string, ns *string, req routingRequest) (*store.RoutingUpdate, *apiError) {
	if req.RoutingMode == nil && req.RoutingExcludedCIDRs == nil && req.RoutingPrecision == nil {
		return nil, nil
	}
	cur, err := s.Store.GetAccountRouting(ctx, accountID)
	if err != nil {
		return nil, toAPIError(err, "update")
	}
	// Same visibility rule as every account route: a foreign id is a plain 404.
	if _, err := s.Store.GetAccount(ctx, accountID, ns); err != nil {
		return nil, toAPIError(err, "update")
	}

	u := &store.RoutingUpdate{}
	mode := cur.Mode
	if req.RoutingMode != nil {
		if *req.RoutingMode == "" {
			u.ClearMode = true
			mode = nil
		} else {
			if !routing.ValidMode(*req.RoutingMode) {
				return nil, badRequest("routing_mode must be full, bypass_iran or custom")
			}
			m := *req.RoutingMode
			u.Mode = &m
			mode = &m
		}
	}
	cidrs := cur.ExcludedCIDRs
	if req.RoutingExcludedCIDRs != nil {
		cidrs = *req.RoutingExcludedCIDRs
	}
	custom := mode != nil && *mode == string(routing.ModeCustom)
	switch {
	case custom:
		if len(cidrs) == 0 {
			return nil, badRequest("routing_mode custom needs routing_excluded_cidrs")
		}
		n, aerr := normalizeExcludedCIDRs(cidrs)
		if aerr != nil {
			return nil, aerr
		}
		u.ExcludedCIDRs = &n
	case req.RoutingExcludedCIDRs != nil && len(*req.RoutingExcludedCIDRs) > 0:
		return nil, badRequest("routing_excluded_cidrs is only used with routing_mode custom")
	case len(cur.ExcludedCIDRs) > 0:
		// Leaving custom: drop the stale list so it cannot silently come back.
		empty := []string{}
		u.ExcludedCIDRs = &empty
	}
	if req.RoutingPrecision != nil {
		if *req.RoutingPrecision == "" {
			u.ClearPrecision = true
		} else {
			if !routing.ValidPrecision(*req.RoutingPrecision) {
				return nil, badRequest("routing_precision must be compact or exact")
			}
			p := *req.RoutingPrecision
			u.Precision = &p
		}
	}
	return u, nil
}

// fillRouting adds the routing fields to account responses with a single query for the
// whole slice. A read failure degrades to the default (null mode) rather than failing
// the account response.
func (s *Server) fillRouting(ctx context.Context, resps []*accountResponse) {
	if len(resps) == 0 {
		return
	}
	ids := make([]string, len(resps))
	for i, r := range resps {
		ids[i] = r.ID
	}
	m, err := s.Store.ListAccountRouting(ctx, ids)
	if err != nil {
		s.Logger.Warn("list_account_routing_failed", "error", err)
		m = nil
	}
	for _, r := range resps {
		ar := m[r.ID]
		r.routingResponse = routingResponseOf(ar.Mode, ar.ExcludedCIDRs, ar.Precision)
	}
}

// ---- node IPv6 ----------------------------------------------------------------------

type nodeIPv6Response struct {
	NodeID string `json:"node_id"`
	// IPv6Enabled / IPv6ULAPrefix are the admin's settings.
	IPv6Enabled   bool    `json:"ipv6_enabled"`
	IPv6ULAPrefix *string `json:"ipv6_ula_prefix"`
	// HostGlobalV6 and Ready are what the agent reported on its last heartbeat: the host
	// has a global IPv6 route, and forwarding + NAT66 + the tunnel address are applied.
	HostGlobalV6 bool `json:"ipv6_host_global"`
	Ready        bool `json:"ipv6_ready"`
	// Active is the one condition under which configs carry IPv6 for this node.
	Active bool `json:"ipv6_active"`
	// PSKSupported: the agent can set a PresharedKey on its peers.
	PSKSupported bool `json:"psk_supported"`
}

func toNodeIPv6Response(n store.NodeRouting) nodeIPv6Response {
	return nodeIPv6Response{
		NodeID: n.NodeID, IPv6Enabled: n.IPv6Enabled, IPv6ULAPrefix: n.IPv6ULAPrefix,
		HostGlobalV6: n.IPv6HostGlobal, Ready: n.IPv6Ready, Active: n.IPv6Active(), PSKSupported: n.PSKSupported,
	}
}

func (s *Server) registerRoutingRoutes(mux *http.ServeMux) {
	// Staff only (not on the reseller allowlist): the node's network is the operator's.
	mux.Handle("GET /api/v1/node-ipv6", s.requireAdmin(http.HandlerFunc(s.handleListNodeIPv6)))
	mux.Handle("PUT /api/v1/node-ipv6/{id}", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleSetNodeIPv6))))
}

func (s *Server) handleListNodeIPv6(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListNodeRouting(r.Context())
	if err != nil {
		s.Logger.Error("list_node_ipv6_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list node IPv6 state")
		return
	}
	out := make([]nodeIPv6Response, 0, len(list))
	for _, n := range list {
		out = append(out, toNodeIPv6Response(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

type setNodeIPv6Request struct {
	Enabled bool `json:"enabled"`
	// ULAPrefix: an admin-chosen /64 inside fc00::/7. Omitted/empty keeps the stored one,
	// and enabling a node that has none generates a random one.
	ULAPrefix *string `json:"ula_prefix"`
}

// handleSetNodeIPv6 is the opt-in switch. Enabling only asks the agent to prepare the
// node; configs gain IPv6 once the agent reports it working (see NodeRouting.IPv6Active).
// The prefix is deliberately not editable while peers exist on an enabled node: changing
// it would change every peer's v6 address under clients that already imported it.
func (s *Server) handleSetNodeIPv6(w http.ResponseWriter, r *http.Request) {
	var req setNodeIPv6Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	cur, err := s.Store.GetNodeRouting(ctx, id)
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_node_routing_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load node")
		return
	}

	var prefix *string
	if req.ULAPrefix != nil && strings.TrimSpace(*req.ULAPrefix) != "" {
		p, perr := routing.NormalizeULAPrefix(strings.TrimSpace(*req.ULAPrefix))
		if perr != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", perr.Error())
			return
		}
		ps := p.String()
		if cur.IPv6ULAPrefix != nil && *cur.IPv6ULAPrefix != ps && cur.IPv6Enabled {
			writeJSONError(w, http.StatusConflict, "ipv6_prefix_locked",
				"disable IPv6 on this node before changing its prefix: clients that imported a config hold addresses from the old one")
			return
		}
		prefix = &ps
	}
	if req.Enabled && prefix == nil && (cur.IPv6ULAPrefix == nil || *cur.IPv6ULAPrefix == "") {
		p, gerr := routing.GenerateULAPrefix()
		if gerr != nil {
			s.Logger.Error("generate_ula_failed", "error", gerr)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate a prefix")
			return
		}
		ps := p.String()
		prefix = &ps
	}

	n, err := s.Store.SetNodeIPv6(ctx, id, req.Enabled, prefix)
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if err != nil {
		s.Logger.Error("set_node_ipv6_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update node")
		return
	}
	if admin, ok := adminFromContext(ctx); ok {
		detail := map[string]any{"ipv6_enabled": n.IPv6Enabled}
		if n.IPv6ULAPrefix != nil {
			detail["ipv6_ula_prefix"] = *n.IPv6ULAPrefix
		}
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "node.ipv6_updated", id, detail, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, toNodeIPv6Response(n))
}
