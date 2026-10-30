package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// Abuse controls (migration 0033): account notes/tags, auto-cleanup, IP-based device
// limit + sharing detection, torrent blocking. The handlers and background work live in
// this file so the shared account/agent handlers only gain small call sites.

func (s *Server) registerAbuseRoutes(mux *http.ServeMux) {
	// Readable by any staff role (configuration display), editable by super_admin only:
	// these decide which customer accounts get deleted or suspended. Not on the reseller
	// allowlist - a reseller sees the effects (badges, events) on its own accounts, never
	// the panel-wide knobs.
	mux.Handle("GET /api/v1/abuse-settings", s.requireAdmin(http.HandlerFunc(s.handleGetAbuseSettings)))
	mux.Handle("PATCH /api/v1/abuse-settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateAbuseSettings))))
}

// ---- settings -------------------------------------------------------------------

type abuseNodeResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// TorrentBlock is the node's own override; null = inherits the global setting.
	TorrentBlock *bool `json:"torrent_block"`
	// TorrentBlockEffective is what the node is currently told to do.
	TorrentBlockEffective bool `json:"torrent_block_effective"`
}

type abuseSettingsResponse struct {
	AutoCleanupEnabled          bool                `json:"auto_cleanup_enabled"`
	AutoCleanupDays             int                 `json:"auto_cleanup_days"`
	DeviceLimitMode             string              `json:"device_limit_mode"`
	DeviceWindowMinutes         int                 `json:"device_window_minutes"`
	SharingDetectionEnabled     bool                `json:"sharing_detection_enabled"`
	SharingMinNetworks          int                 `json:"sharing_min_networks"`
	SharingWindowMinutes        int                 `json:"sharing_window_minutes"`
	TorrentBlockEnabled         bool                `json:"torrent_block_enabled"`
	TorrentSuspendEnabled       bool                `json:"torrent_suspend_enabled"`
	TorrentSuspendHits          int                 `json:"torrent_suspend_hits"`
	TorrentSuspendWindowMinutes int                 `json:"torrent_suspend_window_minutes"`
	Nodes                       []abuseNodeResponse `json:"nodes"`
}

func (s *Server) abuseSettingsResponse(ctx context.Context) (abuseSettingsResponse, error) {
	cfg, err := s.Store.GetAbuseSettings(ctx)
	if err != nil {
		return abuseSettingsResponse{}, err
	}
	nodes, err := s.Store.ListNodeTorrentOverrides(ctx)
	if err != nil {
		return abuseSettingsResponse{}, err
	}
	resp := abuseSettingsResponse{
		AutoCleanupEnabled:          cfg.AutoCleanupEnabled,
		AutoCleanupDays:             cfg.AutoCleanupDays,
		DeviceLimitMode:             cfg.DeviceLimitMode,
		DeviceWindowMinutes:         cfg.DeviceWindowMinutes,
		SharingDetectionEnabled:     cfg.SharingDetectionEnabled,
		SharingMinNetworks:          cfg.SharingMinNetworks,
		SharingWindowMinutes:        cfg.SharingWindowMinutes,
		TorrentBlockEnabled:         cfg.TorrentBlockEnabled,
		TorrentSuspendEnabled:       cfg.TorrentSuspendEnabled,
		TorrentSuspendHits:          cfg.TorrentSuspendHits,
		TorrentSuspendWindowMinutes: cfg.TorrentSuspendWindowMinutes,
		Nodes:                       make([]abuseNodeResponse, 0, len(nodes)),
	}
	for _, n := range nodes {
		resp.Nodes = append(resp.Nodes, abuseNodeResponse{ID: n.NodeID, Name: n.NodeName, TorrentBlock: n.Override, TorrentBlockEffective: n.Effective})
	}
	return resp, nil
}

func (s *Server) handleGetAbuseSettings(w http.ResponseWriter, r *http.Request) {
	resp, err := s.abuseSettingsResponse(r.Context())
	if err != nil {
		s.Logger.Error("get_abuse_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch abuse settings")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type updateAbuseSettingsRequest struct {
	AutoCleanupEnabled          *bool   `json:"auto_cleanup_enabled"`
	AutoCleanupDays             *int    `json:"auto_cleanup_days"`
	DeviceLimitMode             *string `json:"device_limit_mode"`
	DeviceWindowMinutes         *int    `json:"device_window_minutes"`
	SharingDetectionEnabled     *bool   `json:"sharing_detection_enabled"`
	SharingMinNetworks          *int    `json:"sharing_min_networks"`
	SharingWindowMinutes        *int    `json:"sharing_window_minutes"`
	TorrentBlockEnabled         *bool   `json:"torrent_block_enabled"`
	TorrentSuspendEnabled       *bool   `json:"torrent_suspend_enabled"`
	TorrentSuspendHits          *int    `json:"torrent_suspend_hits"`
	TorrentSuspendWindowMinutes *int    `json:"torrent_suspend_window_minutes"`
	// NodeOverrides maps node id -> true/false (force on/off) or null (inherit).
	NodeOverrides map[string]*bool `json:"node_overrides"`
}

func inRange(v *int, lo, hi int) bool { return v == nil || (*v >= lo && *v <= hi) }

func (s *Server) handleUpdateAbuseSettings(w http.ResponseWriter, r *http.Request) {
	var req updateAbuseSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	switch {
	case !inRange(req.AutoCleanupDays, 1, 3650):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "auto_cleanup_days must be between 1 and 3650")
		return
	case req.DeviceLimitMode != nil && *req.DeviceLimitMode != store.DeviceModeEndpoint && *req.DeviceLimitMode != store.DeviceModeIP:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "device_limit_mode must be endpoint or ip")
		return
	case !inRange(req.DeviceWindowMinutes, 1, 1440):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "device_window_minutes must be between 1 and 1440")
		return
	case !inRange(req.SharingMinNetworks, 2, 50):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "sharing_min_networks must be between 2 and 50")
		return
	case !inRange(req.SharingWindowMinutes, 1, 1440):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "sharing_window_minutes must be between 1 and 1440")
		return
	case !inRange(req.TorrentSuspendHits, 1, 1_000_000):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "torrent_suspend_hits must be at least 1")
		return
	case !inRange(req.TorrentSuspendWindowMinutes, 1, 10080):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "torrent_suspend_window_minutes must be between 1 and 10080")
		return
	}
	for id := range req.NodeOverrides {
		if !uuidRE.MatchString(id) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "node_overrides keys must be node ids")
			return
		}
	}

	ctx := r.Context()
	if _, err := s.Store.UpdateAbuseSettings(ctx, store.UpdateAbuseSettingsParams{
		AutoCleanupEnabled: req.AutoCleanupEnabled, AutoCleanupDays: req.AutoCleanupDays,
		DeviceLimitMode: req.DeviceLimitMode, DeviceWindowMinutes: req.DeviceWindowMinutes,
		SharingDetectionEnabled: req.SharingDetectionEnabled, SharingMinNetworks: req.SharingMinNetworks,
		SharingWindowMinutes: req.SharingWindowMinutes, TorrentBlockEnabled: req.TorrentBlockEnabled,
		TorrentSuspendEnabled: req.TorrentSuspendEnabled, TorrentSuspendHits: req.TorrentSuspendHits,
		TorrentSuspendWindowMinutes: req.TorrentSuspendWindowMinutes,
	}); err != nil {
		s.Logger.Error("update_abuse_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update abuse settings")
		return
	}
	for id, v := range req.NodeOverrides {
		if err := s.Store.SetNodeTorrentOverride(ctx, id, v); errors.Is(err, store.ErrNodeNotFound) {
			writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with id "+id)
			return
		} else if err != nil {
			s.Logger.Error("set_node_torrent_override_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update node override")
			return
		}
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		// The body itself is the detail: it is operator-chosen thresholds, nothing secret,
		// and "who turned auto-delete on, and when" is exactly what an audit trail is for.
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "settings.abuse_updated", "abuse_settings", req, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
	resp, err := s.abuseSettingsResponse(ctx)
	if err != nil {
		s.Logger.Error("get_abuse_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch abuse settings")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- notes and tags ---------------------------------------------------------------

// validateAccountMeta normalizes the note/tags of a create or update request. Tags are
// normalized here (not trusted from the client) so "VIP" and " vip " are one tag.
func validateAccountMeta(note *string, tags *[]string) (*string, *[]string, *apiError) {
	if note != nil && len([]rune(*note)) > store.MaxNoteLength {
		return nil, nil, badRequest("note must be at most 2000 characters")
	}
	if tags == nil {
		return note, nil, nil
	}
	norm, err := store.NormalizeTags(*tags)
	if err != nil {
		return nil, nil, badRequest(err.Error())
	}
	return note, &norm, nil
}

// withAccountListFilter reads ?tag= (repeatable, all must match) and ?q= (substring of
// label, external_ref, note or a tag) into the request context for ListAccounts.
func withAccountListFilter(ctx context.Context, q url.Values) (context.Context, *apiError) {
	tags, err := store.NormalizeTags(q["tag"])
	if err != nil {
		return ctx, badRequest(err.Error())
	}
	query := q.Get("q")
	if len(tags) == 0 && query == "" {
		return ctx, nil
	}
	return store.WithAccountFilter(ctx, store.AccountFilter{Tags: tags, Query: query}), nil
}

// decorateAccounts fills the abuse-related response fields (note, tags, sharing flag,
// torrent hits) for a page of accounts with one batched lookup. A failure only logs:
// the account data itself is more important than its annotations.
func (s *Server) decorateAccounts(ctx context.Context, rs []accountResponse) {
	ids := make([]string, len(rs))
	for i := range rs {
		ids[i] = rs[i].ID
	}
	meta, err := s.Store.AccountMetaByIDs(ctx, ids)
	if err != nil {
		s.Logger.Error("account_meta_failed", "error", err)
	}
	for i := range rs {
		m, ok := meta[rs[i].ID]
		if !ok {
			rs[i].Tags = []string{}
			continue
		}
		rs[i].Note, rs[i].Tags = m.Note, m.Tags
		rs[i].PossiblyShared = m.SharingSuspectedAt != nil
		rs[i].TorrentHits24h = m.TorrentHits24h
	}
}

func (s *Server) decorateAccount(ctx context.Context, r accountResponse) accountResponse {
	rs := []accountResponse{r}
	s.decorateAccounts(ctx, rs)
	return rs[0]
}

// applyAccountMeta stores a create/update request's note and tags after the account
// exists, scoped to the caller's namespace.
func (s *Server) applyAccountMeta(ctx context.Context, id string, ns *string, note *string, tags *[]string) error {
	if note == nil && tags == nil {
		return nil
	}
	return s.Store.SetAccountMeta(ctx, id, ns, note, tags)
}

// ---- account detail extras --------------------------------------------------------

type sharingNetworkResponse struct {
	Network  string   `json:"network"`
	IPs      []string `json:"ips"`
	Nodes    []string `json:"nodes"`
	LastSeen string   `json:"last_seen_at"`
}

// abuseAccountExtras is merged into GET /accounts/{id}/devices: the device-limit mode
// the counts follow, the sharing verdict with its evidence, and the torrent history.
func (s *Server) abuseAccountExtras(ctx context.Context, accountID string, ns *string) map[string]any {
	extras := map[string]any{}
	cfg, err := s.Store.GetAbuseSettings(ctx)
	if err != nil {
		s.Logger.Error("get_abuse_settings_failed", "error", err)
		return extras
	}
	extras["device_limit_mode"] = cfg.DeviceLimitMode
	extras["device_window_minutes"] = cfg.DeviceWindowMinutes

	sharing := map[string]any{
		"enabled":        cfg.SharingDetectionEnabled,
		"suspected":      false,
		"min_networks":   cfg.SharingMinNetworks,
		"window_minutes": cfg.SharingWindowMinutes,
		"networks":       []sharingNetworkResponse{},
	}
	if meta, err := s.Store.AccountMetaByIDs(ctx, []string{accountID}); err == nil {
		if m, ok := meta[accountID]; ok && m.SharingSuspectedAt != nil {
			sharing["suspected"] = true
			sharing["since"] = m.SharingSuspectedAt.Format(time.RFC3339)
		}
	}
	if cfg.SharingDetectionEnabled {
		nets, err := s.Store.AccountSharingNetworks(ctx, accountID, ns, time.Duration(cfg.SharingWindowMinutes)*time.Minute)
		if err != nil {
			s.Logger.Error("account_sharing_networks_failed", "error", err)
		}
		out := make([]sharingNetworkResponse, 0, len(nets))
		for _, n := range nets {
			out = append(out, sharingNetworkResponse{Network: n.Network, IPs: n.IPs, Nodes: n.Nodes, LastSeen: n.LastSeen.Format(time.RFC3339)})
		}
		sharing["networks"] = out
	}
	extras["sharing"] = sharing

	torrent := map[string]any{"blocking_enabled": cfg.TorrentBlockEnabled, "recent": []map[string]any{}}
	if sum, err := s.Store.GetAccountTorrentSummary(ctx, accountID, ns); err == nil {
		torrent["hits_24h"], torrent["hits_30d"] = sum.Hits24h, sum.Hits30d
		torrent["last_hit_at"] = rfc3339Ptr(sum.LastHit)
		recent := make([]map[string]any, 0, len(sum.Recent))
		for _, h := range sum.Recent {
			recent = append(recent, map[string]any{"at": h.TS.Format(time.RFC3339), "node_name": h.NodeName, "hits": h.Hits})
		}
		torrent["recent"] = recent
	} else {
		s.Logger.Error("account_torrent_summary_failed", "error", err)
	}
	extras["torrent"] = torrent
	return extras
}

// ---- agent heartbeat: torrent hits -----------------------------------------------

// agentTorrentHit is one tunnel IP's new torrent-signature packets since the agent's
// last acknowledged heartbeat (deltas, not totals - see cmd/agent/torrent.go).
type agentTorrentHit struct {
	IP   string `json:"ip"`
	Hits int64  `json:"hits"`
}

// maxTorrentHitsPerReport bounds what one heartbeat may claim for one IP, so a buggy or
// hostile agent cannot overflow the counters.
const maxTorrentHitsPerReport = 100_000_000

// ingestTorrentHits records an agent's reported hits and announces what happened.
// Best-effort like the rest of heartbeat telemetry: failures are logged, never failed.
func (s *Server) ingestTorrentHits(ctx context.Context, nodeID string, hits []agentTorrentHit) {
	if len(hits) == 0 {
		return
	}
	reports := make([]store.TorrentHitReport, 0, len(hits))
	for _, h := range hits {
		if net.ParseIP(h.IP) == nil || h.Hits <= 0 {
			continue
		}
		reports = append(reports, store.TorrentHitReport{IP: h.IP, Hits: min(h.Hits, maxTorrentHitsPerReport)})
	}
	evs, err := s.Store.IngestTorrentHits(ctx, nodeID, reports)
	if err != nil {
		s.Logger.Error("ingest_torrent_hits_failed", "error", err)
		return
	}
	for _, e := range evs {
		data := map[string]any{"account_id": e.AccountID, "label": e.Label, "node_name": e.NodeName, "hits": e.Hits, "suspended": e.Suspended}
		if e.ExternalRef != nil {
			data["external_ref"] = *e.ExternalRef
		}
		if e.Announce {
			s.emit(events.AccountTorrentDetected, e.Namespace, data)
		}
		if e.Suspended {
			s.emit(events.AccountSuspended, e.Namespace, map[string]any{"account_id": e.AccountID, "label": e.Label, "status": "suspended", "reason": "torrent_abuse"})
			_ = s.Store.InsertAuditLog(ctx, "system", "account.suspended", e.AccountID,
				map[string]any{"reason": "torrent_abuse", "window_hits": e.WindowHits}, "")
		}
	}
}

// ---- background sweep ---------------------------------------------------------------

// cleanupBatch bounds one sweep's auto-deletions, like resetBatch does for resets: a
// huge backlog (feature switched on for an old panel) is worked off over several ticks.
const cleanupBatch = 100

// torrentHitRetention matches the 30-day window the account detail shows.
const torrentHitRetention = 30 * 24 * time.Hour

// runAbuseSweep runs the settings-gated background jobs once: auto-cleanup of dead
// accounts, sharing detection, and pruning of old torrent hits. Called from the offline
// sweep loop on the lifecycle cadence (about once a minute).
func (s *Server) runAbuseSweep(ctx context.Context) {
	cfg, err := s.Store.GetAbuseSettings(ctx)
	if err != nil {
		s.Logger.Error("abuse_settings_failed", "error", err)
		return
	}

	if cfg.AutoCleanupEnabled {
		s.runAutoCleanup(ctx, cfg.AutoCleanupDays)
	}

	if cfg.SharingDetectionEnabled {
		evs, err := s.Store.EvaluateSharing(ctx, cfg.SharingMinNetworks, time.Duration(cfg.SharingWindowMinutes)*time.Minute)
		if err != nil {
			s.Logger.Error("sharing_sweep_failed", "error", err)
		}
		for _, e := range evs {
			data := map[string]any{"account_id": e.AccountID, "label": e.Label, "networks": len(e.Networks), "network_list": e.Networks}
			if e.ExternalRef != nil {
				data["external_ref"] = *e.ExternalRef
			}
			s.emit(events.AccountSharingSuspected, e.Namespace, data)
		}
	} else if err := s.Store.ClearSharingFlags(ctx); err != nil {
		s.Logger.Error("sharing_clear_failed", "error", err)
	}

	if err := s.Store.PruneTorrentHits(ctx, torrentHitRetention); err != nil {
		s.Logger.Error("torrent_prune_failed", "error", err)
	}
}

// runAutoCleanup deletes accounts that have been expired/quota-suspended for `days`
// days, writing the audit row (actor "system", reason auto_cleanup) and the same
// account.deleted event a manual delete emits, with reason auto_cleanup in its data.
func (s *Server) runAutoCleanup(ctx context.Context, days int) {
	for {
		deleted, err := s.Store.AutoDeleteDeadAccounts(ctx, days, cleanupBatch)
		if err != nil {
			s.Logger.Error("auto_cleanup_failed", "error", err)
			return
		}
		for _, d := range deleted {
			data := map[string]any{"account_id": d.ID, "label": d.Label, "status": "deleted", "reason": "auto_cleanup", "suspend_reason": d.Reason}
			if d.ExternalRef != nil {
				data["external_ref"] = *d.ExternalRef
			}
			s.emit(events.AccountDeleted, d.Namespace, data)
			_ = s.Store.InsertAuditLog(ctx, "system", "account.deleted", d.ID, map[string]any{
				"reason":         "auto_cleanup",
				"suspend_reason": d.Reason,
				"suspended_at":   d.SuspendedAt.Format(time.RFC3339),
				"after_days":     days,
			}, "")
		}
		if len(deleted) < cleanupBatch {
			return
		}
	}
}
