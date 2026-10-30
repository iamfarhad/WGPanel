package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/obfuscation"
	"wgpanel-api/internal/store"
)

// AmneziaWG obfuscation profiles (migration 0028): staff CRUD, rotation, assignment to
// nodes and node groups, and the heartbeat contract with the agent. Routes live here
// instead of server.go so the feature stays one self-contained unit; resellers cannot
// reach any of them (the reseller allowlist denies unlisted routes by default).

func (s *Server) registerObfuscationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/obfuscation/presets", s.requireAdmin(s.requireRole("support", http.HandlerFunc(s.handleListObfuscationPresets))))
	mux.Handle("GET /api/v1/obfuscation/profiles", s.requireAdmin(s.requireRole("support", http.HandlerFunc(s.handleListObfuscationProfiles))))
	mux.Handle("POST /api/v1/obfuscation/profiles", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleCreateObfuscationProfile))))
	mux.Handle("GET /api/v1/obfuscation/profiles/{id}", s.requireAdmin(s.requireRole("support", http.HandlerFunc(s.handleGetObfuscationProfile))))
	mux.Handle("PATCH /api/v1/obfuscation/profiles/{id}", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleUpdateObfuscationProfile))))
	mux.Handle("DELETE /api/v1/obfuscation/profiles/{id}", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleDeleteObfuscationProfile))))
	mux.Handle("POST /api/v1/obfuscation/profiles/{id}/rotate", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleRotateObfuscationProfile))))
	mux.Handle("POST /api/v1/obfuscation/assign-group", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleAssignObfuscationGroup))))
	mux.Handle("GET /api/v1/nodes/{id}/obfuscation", s.requireAdmin(s.requireRole("support", http.HandlerFunc(s.handleGetNodeObfuscation))))
	mux.Handle("PUT /api/v1/nodes/{id}/obfuscation", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleSetNodeObfuscation))))
}

type obfuscationProfileResponse struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	Compat                string             `json:"compat"`
	Params                obfuscation.Params `json:"params"`
	Revision              int                `json:"revision"`
	NodeCount             int                `json:"node_count"`
	StockClientCompatible bool               `json:"stock_client_compatible"`
	CreatedAt             string             `json:"created_at"`
	UpdatedAt             string             `json:"updated_at"`
}

func toObfuscationProfileResponse(p store.ObfuscationProfile) obfuscationProfileResponse {
	return obfuscationProfileResponse{
		ID: p.ID, Name: p.Name, Compat: p.Compat, Params: p.Params, Revision: p.Revision, NodeCount: p.NodeCount,
		// A junk-only profile leaves the wire format of everything a stock client sends
		// or receives untouched - see obfuscation.Preset.StockCompatible.
		StockClientCompatible: p.Params.JunkOnly(),
		CreatedAt:             p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:             p.UpdatedAt.Format(time.RFC3339),
	}
}

func (s *Server) handleListObfuscationPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": obfuscation.Presets()})
}

func (s *Server) handleListObfuscationProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Store.ListObfuscationProfiles(r.Context())
	if err != nil {
		s.Logger.Error("list_obfuscation_profiles_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list profiles")
		return
	}
	out := make([]obfuscationProfileResponse, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, toObfuscationProfileResponse(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *Server) handleGetObfuscationProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetObfuscationProfile(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrObfuscationProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch profile")
		return
	}
	writeJSON(w, http.StatusOK, toObfuscationProfileResponse(p))
}

type createObfuscationProfileRequest struct {
	Name string `json:"name"`
	// Preset (an id from /obfuscation/presets) generates fresh random parameters;
	// otherwise Compat and Params are taken as given.
	Preset string              `json:"preset"`
	Compat string              `json:"compat"`
	Params *obfuscation.Params `json:"params"`
}

func (s *Server) handleCreateObfuscationProfile(w http.ResponseWriter, r *http.Request) {
	var req createObfuscationProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name is required (at most 64 characters)")
		return
	}

	var params obfuscation.Params
	compat := req.Compat
	switch {
	case req.Preset != "":
		var err error
		if params, compat, err = obfuscation.FromPreset(req.Preset); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	case req.Params != nil:
		params = req.Params.Normalize()
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "give either preset or compat with params")
		return
	}
	if err := params.Validate(compat); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_params", err.Error())
		return
	}

	ctx := r.Context()
	p, err := s.Store.CreateObfuscationProfile(ctx, req.Name, compat, params)
	if errors.Is(err, store.ErrObfuscationNameTaken) {
		writeJSONError(w, http.StatusConflict, "profile_name_taken", "a profile with this name already exists")
		return
	}
	if err != nil {
		s.Logger.Error("create_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create profile")
		return
	}
	s.auditObfuscation(r, "obfuscation.profile_created", p.Name, map[string]any{"compat": p.Compat, "preset": req.Preset})
	writeJSON(w, http.StatusCreated, toObfuscationProfileResponse(p))
}

type updateObfuscationProfileRequest struct {
	Name   *string             `json:"name"`
	Compat *string             `json:"compat"`
	Params *obfuscation.Params `json:"params"` // the complete new parameter set
}

func (s *Server) handleUpdateObfuscationProfile(w http.ResponseWriter, r *http.Request) {
	var req updateObfuscationProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	ctx := r.Context()
	cur, err := s.Store.GetObfuscationProfile(ctx, r.PathValue("id"))
	if errors.Is(err, store.ErrObfuscationProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch profile")
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" || len(trimmed) > 64 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "name must be 1-64 characters")
			return
		}
		req.Name = &trimmed
	}
	// Validate the merged result: changing only compat must re-check the stored params.
	compat, params := cur.Compat, cur.Params
	if req.Compat != nil {
		compat = *req.Compat
	}
	if req.Params != nil {
		params = req.Params.Normalize()
		req.Params = &params
	}
	if err := params.Validate(compat); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_params", err.Error())
		return
	}

	p, err := s.Store.UpdateObfuscationProfile(ctx, cur.ID, req.Name, req.Compat, req.Params)
	if errors.Is(err, store.ErrObfuscationNameTaken) {
		writeJSONError(w, http.StatusConflict, "profile_name_taken", "a profile with this name already exists")
		return
	}
	if errors.Is(err, store.ErrObfuscationProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	}
	if err != nil {
		s.Logger.Error("update_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update profile")
		return
	}
	s.auditObfuscation(r, "obfuscation.profile_updated", p.Name, map[string]any{"revision": p.Revision})
	writeJSON(w, http.StatusOK, toObfuscationProfileResponse(p))
}

func (s *Server) handleDeleteObfuscationProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.GetObfuscationProfile(ctx, r.PathValue("id"))
	if err == nil {
		err = s.Store.DeleteObfuscationProfile(ctx, p.ID)
	}
	switch {
	case errors.Is(err, store.ErrObfuscationProfileNotFound):
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
	case errors.Is(err, store.ErrObfuscationProfileInUse):
		writeJSONError(w, http.StatusConflict, "profile_in_use", "this profile is assigned to nodes - unassign it first")
	case err != nil:
		s.Logger.Error("delete_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete profile")
	default:
		s.auditObfuscation(r, "obfuscation.profile_deleted", p.Name, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

type rotateObfuscationRequest struct {
	// CPS controls the awg2 I1-I5 signatures: keep (default), quic, dns or none.
	CPS string `json:"cps"`
}

// handleRotateObfuscationProfile regenerates the profile's parameters. Nodes pick the
// new revision up on their next heartbeat; every client config issued for those nodes
// stops working until it is re-imported, which is the point (a fingerprinted parameter
// set is burned) and why the UI asks for confirmation first.
func (s *Server) handleRotateObfuscationProfile(w http.ResponseWriter, r *http.Request) {
	var req rotateObfuscationRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
			return
		}
	}
	p, err := s.Store.RotateObfuscationProfile(r.Context(), r.PathValue("id"), req.CPS)
	if errors.Is(err, store.ErrObfuscationProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	}
	if err != nil {
		// The only non-database failure is an unknown cps mode.
		if strings.Contains(err.Error(), "cps must be") {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		s.Logger.Error("rotate_obfuscation_profile_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate profile")
		return
	}
	s.auditObfuscation(r, "obfuscation.profile_rotated", p.Name, map[string]any{"revision": p.Revision, "nodes": p.NodeCount})
	s.emit(events.ObfuscationProfileRotated, "", map[string]any{
		"profile_id": p.ID, "profile_name": p.Name, "revision": p.Revision, "node_count": p.NodeCount,
	})
	writeJSON(w, http.StatusOK, toObfuscationProfileResponse(p))
}

type assignObfuscationGroupRequest struct {
	NodeGroup string  `json:"node_group"`
	ProfileID *string `json:"profile_id"` // null clears the group's profile
}

func (s *Server) handleAssignObfuscationGroup(w http.ResponseWriter, r *http.Request) {
	var req assignObfuscationGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeGroup == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "node_group is required")
		return
	}
	n, err := s.Store.SetGroupObfuscationProfile(r.Context(), req.NodeGroup, req.ProfileID)
	if errors.Is(err, store.ErrObfuscationProfileNotFound) {
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	}
	if err != nil {
		s.Logger.Error("assign_obfuscation_group_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not assign profile")
		return
	}
	if n == 0 {
		writeJSONError(w, http.StatusNotFound, "node_group_not_found", "no node is in that group")
		return
	}
	s.auditObfuscation(r, "obfuscation.group_assigned", req.NodeGroup, map[string]any{"profile_id": req.ProfileID, "nodes": n})
	writeJSON(w, http.StatusOK, map[string]any{"node_group": req.NodeGroup, "profile_id": req.ProfileID, "nodes_updated": n})
}

// nodeObfuscationResponse is the obfuscation block of a node (embedded in nodeResponse
// and returned by GET /nodes/{id}/obfuscation). Mode is what the node REALLY runs, as
// last reported by its agent; ProfileID is what the panel wants it to run.
type nodeObfuscationResponse struct {
	Mode            string  `json:"mode"` // "wireguard" or "awg"
	ProfileID       *string `json:"profile_id,omitempty"`
	ProfileName     *string `json:"profile_name,omitempty"`
	ProfileCompat   *string `json:"profile_compat,omitempty"`
	ProfileRevision *int    `json:"profile_revision,omitempty"`
	AppliedRevision *int    `json:"applied_revision,omitempty"`
	// Applied: the node runs the assigned profile at its current revision, so client
	// configs for it carry the AWG parameters. Pending: a profile is assigned but not
	// (yet) applied.
	Applied   bool   `json:"applied"`
	Pending   bool   `json:"pending"`
	Supported bool   `json:"supported"` // the node image can run AWG (binaries + /dev/net/tun)
	Error     string `json:"error,omitempty"`
}

func toNodeObfuscationResponse(n store.NodeObfuscation, staff bool) nodeObfuscationResponse {
	out := nodeObfuscationResponse{
		Mode:      n.Mode,
		Applied:   n.Applied(),
		Pending:   n.ProfileID != nil && !n.Applied(),
		Supported: n.Supported,
	}
	if !staff {
		// Bots and resellers see the mode (so a bot can pick AWG nodes) but not which
		// parameter set stands behind it.
		return out
	}
	out.ProfileID, out.ProfileName, out.ProfileCompat = n.ProfileID, n.ProfileName, n.ProfileCompat
	out.ProfileRevision, out.AppliedRevision = n.ProfileRevision, n.AppliedRevision
	out.Error = n.Error
	return out
}

func isStaff(identity *CallerIdentity) bool {
	return identity != nil && identity.IsAdmin && identity.ResellerID == ""
}

// nodeObfuscationMap loads every node's state for list/get responses. A failure only
// omits the block (best-effort, like the other node enrichments).
func (s *Server) nodeObfuscationMap(ctx context.Context) map[string]store.NodeObfuscation {
	m, err := s.Store.ListNodeObfuscation(ctx)
	if err != nil {
		s.Logger.Warn("list_node_obfuscation_failed", "error", err)
		return nil
	}
	return m
}

func (s *Server) handleGetNodeObfuscation(w http.ResponseWriter, r *http.Request) {
	state, err := s.Store.GetNodeObfuscation(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_node_obfuscation_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node obfuscation")
		return
	}
	writeJSON(w, http.StatusOK, toNodeObfuscationResponse(state, true))
}

type setNodeObfuscationRequest struct {
	ProfileID *string `json:"profile_id"` // null = plain WireGuard
}

func (s *Server) handleSetNodeObfuscation(w http.ResponseWriter, r *http.Request) {
	var req setNodeObfuscationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.Store.SetNodeObfuscationProfile(ctx, id, req.ProfileID)
	switch {
	case errors.Is(err, store.ErrNodeNotFound):
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	case errors.Is(err, store.ErrObfuscationProfileNotFound):
		writeJSONError(w, http.StatusNotFound, "profile_not_found", "no obfuscation profile with that id")
		return
	case err != nil:
		s.Logger.Error("set_node_obfuscation_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not assign profile")
		return
	}
	target := id
	if node, err := s.Store.GetNode(ctx, id); err == nil {
		target = node.Name
	}
	s.auditObfuscation(r, "node.obfuscation_assigned", target, map[string]any{"profile_id": req.ProfileID})
	state, err := s.Store.GetNodeObfuscation(ctx, id)
	if err != nil {
		s.Logger.Error("get_node_obfuscation_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node obfuscation")
		return
	}
	writeJSON(w, http.StatusOK, toNodeObfuscationResponse(state, true))
}

func (s *Server) auditObfuscation(r *http.Request, action, target string, detail any) {
	identity, ok := callerIdentityFromContext(r.Context())
	if !ok {
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), auditActor(identity), action, target, detail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}

// ---- agent heartbeat contract ----

// agentObfuscationReport is what the agent says it actually runs. Absent from agents
// that predate AmneziaWG support: the stored state then stays "wireguard, unsupported",
// so such nodes never get AWG configs handed out.
type agentObfuscationReport struct {
	Mode      string `json:"mode"` // "wireguard" or "awg"
	ProfileID string `json:"profile_id"`
	Revision  int    `json:"revision"`
	Supported bool   `json:"supported"`
	Error     string `json:"error"`
}

// agentObfuscationDirective is the heartbeat response block telling the agent which
// profile to run (Profile nil = plain WireGuard). Always present from this panel
// version on, so an agent can tell "run plain WireGuard" from "panel too old to say".
type agentObfuscationDirective struct {
	Profile *agentObfuscationProfile `json:"profile"`
}

type agentObfuscationProfile struct {
	ID       string             `json:"id"`
	Revision int                `json:"revision"`
	Compat   string             `json:"compat"`
	Params   obfuscation.Params `json:"params"`
}

// recordAgentObfuscation stores the agent's report. Best-effort: a failure must not
// fail the heartbeat (peers and traffic accounting matter more).
func (s *Server) recordAgentObfuscation(ctx context.Context, nodeID string, rep *agentObfuscationReport) {
	if rep == nil {
		return
	}
	err := s.Store.RecordNodeObfuscation(ctx, nodeID, store.NodeObfuscationReport{
		Mode: rep.Mode, ProfileID: rep.ProfileID, Revision: rep.Revision, Supported: rep.Supported, Error: rep.Error,
	})
	if err != nil {
		s.Logger.Error("record_node_obfuscation_failed", "error", err)
	}
}

// agentObfuscationFor builds the directive for a node; nil on a lookup failure so the
// agent keeps what it is running instead of being told "plain WireGuard" by mistake.
func (s *Server) agentObfuscationFor(ctx context.Context, nodeID string) *agentObfuscationDirective {
	p, err := s.Store.DesiredObfuscationForNode(ctx, nodeID)
	if err != nil {
		s.Logger.Error("desired_obfuscation_failed", "error", err)
		return nil
	}
	d := &agentObfuscationDirective{}
	if p != nil {
		d.Profile = &agentObfuscationProfile{ID: p.ID, Revision: p.Revision, Compat: p.Compat, Params: p.Params}
	}
	return d
}

// ---- client config rendering ----

// awgInterfaceLines returns the [Interface] lines for a node's clients: empty unless
// the node really runs its assigned profile (see store.NodeObfuscation.Applied), so a
// config is never AWG while the node is still plain WireGuard, or the reverse.
func (s *Server) awgInterfaceLines(ctx context.Context, nodeID string) string {
	p, err := s.Store.AppliedObfuscationForNode(ctx, nodeID)
	if err != nil {
		s.Logger.Warn("applied_obfuscation_lookup_failed", "error", err)
		return ""
	}
	if p == nil {
		return ""
	}
	return p.Params.InterfaceBlock()
}

// renderPeerConfig is the config every endpoint hands out: the plain WireGuard config
// (renderPeerConfigPlain, in subscription.go) with the node's AmneziaWG parameters
// spliced in at the end of [Interface] when the node really applies a profile. Kept as
// a wrapper so the template itself stays untouched by this feature.
func (s *Server) renderPeerConfig(ctx context.Context, accountID string, ns *string, target store.AccountPeerWithNode, endpointID string) (string, error) {
	cfg, err := s.renderPeerConfigPlain(ctx, accountID, ns, target, endpointID)
	if err != nil {
		return "", err
	}
	return injectAWGLines(cfg, s.awgInterfaceLines(ctx, target.NodeID)), nil
}

// injectAWGLines inserts block (newline-terminated "Key = value" lines) at the end of
// the [Interface] section, i.e. right before the first [Peer] header.
func injectAWGLines(cfg, block string) string {
	if block == "" {
		return cfg
	}
	i := strings.Index(cfg, "\n[Peer]")
	if i < 0 {
		return cfg
	}
	return cfg[:i] + block + cfg[i:]
}

// awgApp is one client app that can import an AWG config.
type awgApp struct {
	OS   string `json:"os"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// awgApps lists the apps per platform for the public subscription page and bots.
// AmneziaVPN covers every desktop/mobile OS; AmneziaWG is the lighter dedicated
// client on Android and iOS. awg2 profiles need a recent build of either.
var awgApps = []awgApp{
	{"android", "AmneziaWG", "https://play.google.com/store/apps/details?id=org.amnezia.awg"},
	{"android", "AmneziaVPN", "https://play.google.com/store/apps/details?id=org.amnezia.vpn"},
	{"ios", "AmneziaWG", "https://apps.apple.com/app/amneziawg/id6478942365"},
	{"ios", "AmneziaVPN", "https://apps.apple.com/app/amneziavpn/id1600529900"},
	{"windows", "AmneziaVPN", "https://github.com/amnezia-vpn/amnezia-client/releases/latest"},
	{"macos", "AmneziaVPN", "https://github.com/amnezia-vpn/amnezia-client/releases/latest"},
	{"linux", "AmneziaVPN", "https://github.com/amnezia-vpn/amnezia-client/releases/latest"},
}

// subscriptionObfuscation is the public subscription info block: whether the account's
// configs are AWG and which apps import them. It never exposes the parameters.
type subscriptionObfuscation struct {
	AWG    bool     `json:"awg"`
	Compat string   `json:"compat,omitempty"`
	Apps   []awgApp `json:"apps"`
}

// subscriptionObfuscationFor builds the page block from the steering-ranked nodes. AWG
// follows the first node, because a plain /sub/{token} fetch serves exactly that one -
// the "install this app" hint must match the config the user actually downloads. The
// app list is filled when any node is AWG, so the page can still show it for a node
// picked by hand.
func (s *Server) subscriptionObfuscationFor(ctx context.Context, nodes []subscriptionNodeResponse) subscriptionObfuscation {
	out := subscriptionObfuscation{Apps: []awgApp{}}
	for _, n := range nodes {
		if n.AWG {
			out.Apps = awgApps
			break
		}
	}
	if len(nodes) == 0 || !nodes[0].AWG {
		return out
	}
	if p, err := s.Store.AppliedObfuscationForNode(ctx, nodes[0].NodeID); err == nil && p != nil {
		out.AWG, out.Compat = true, p.Compat
	}
	return out
}

// awgAppliedNodes is the set of node ids whose configs currently carry AWG parameters.
func (s *Server) awgAppliedNodes(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for id, st := range s.nodeObfuscationMap(ctx) {
		if st.Applied() {
			out[id] = true
		}
	}
	return out
}
