package httpapi

import (
	"encoding/json"
	"net/http"

	"wgpanel-api/internal/store"
)

type settingsResponse struct {
	PublicBaseURL       *string  `json:"public_base_url"`
	DefaultDataQuotaGB  *float64 `json:"default_data_quota_gb"`
	DefaultDeviceLimit  *int     `json:"default_device_limit"`
	DefaultNodeCapacity int      `json:"default_node_capacity"`
	SupportContact      *string  `json:"support_contact"`
	PanelDomain         *string  `json:"panel_domain"`
}

func toSettingsResponse(p store.PanelSettings) settingsResponse {
	return settingsResponse{
		PublicBaseURL:       p.PublicBaseURL,
		DefaultDataQuotaGB:  p.DefaultDataQuotaGB,
		DefaultDeviceLimit:  p.DefaultDeviceLimit,
		DefaultNodeCapacity: p.DefaultNodeCapacity,
		SupportContact:      p.SupportContact,
		PanelDomain:         p.PanelDomain,
	}
}

// handleGetSettings is readable by any admin role (including support) - it's
// configuration display, not a mutating action, so it doesn't need requireRole.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.Logger.Error("get_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch settings")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

type updateSettingsRequest struct {
	PublicBaseURL       *string  `json:"public_base_url"`
	DefaultDataQuotaGB  *float64 `json:"default_data_quota_gb"`
	DefaultDeviceLimit  *int     `json:"default_device_limit"`
	DefaultNodeCapacity *int     `json:"default_node_capacity"`
	SupportContact      *string  `json:"support_contact"`
	// PanelDomain, when non-nil/non-empty, both persists and triggers a live push to
	// Caddy's admin API (docs/STORY-10-monitoring-and-domain-management.md) - see
	// domain_live_applied/domain_apply_error on the response.
	PanelDomain *string `json:"panel_domain"`
}

type updateSettingsResponse struct {
	settingsResponse
	// DomainLiveApplied/DomainApplyError are only meaningful when this request
	// included panel_domain - both stay zero-valued otherwise. A false/non-nil here
	// does NOT mean the domain change was rejected: it's still persisted in
	// panel_settings either way, and a later successful update (or a container
	// restart with a matching PANEL_DOMAIN) would still apply it. This just tells
	// the admin whether it took effect *live*, right now, without a restart.
	DomainLiveApplied bool    `json:"domain_live_applied"`
	DomainApplyError  *string `json:"domain_apply_error"`
}

// handleUpdateSettings is super_admin-only (wired via requireRole in server.go) -
// these are panel-wide defaults, the same trust tier as API keys and join tokens.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.DefaultNodeCapacity != nil && *req.DefaultNodeCapacity <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "default_node_capacity must be positive")
		return
	}
	if req.PanelDomain != nil && *req.PanelDomain == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "panel_domain must not be empty")
		return
	}

	ctx := r.Context()
	settings, err := s.Store.UpdateSettings(ctx, store.UpdateSettingsParams{
		PublicBaseURL:       req.PublicBaseURL,
		DefaultDataQuotaGB:  req.DefaultDataQuotaGB,
		DefaultDeviceLimit:  req.DefaultDeviceLimit,
		DefaultNodeCapacity: req.DefaultNodeCapacity,
		SupportContact:      req.SupportContact,
		PanelDomain:         req.PanelDomain,
	})
	if err != nil {
		s.Logger.Error("update_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update settings")
		return
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "settings.updated", "panel_settings", nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	resp := updateSettingsResponse{settingsResponse: toSettingsResponse(settings)}
	if req.PanelDomain != nil {
		if err := s.CaddyAdmin.SetDomain(ctx, *req.PanelDomain, s.AdminACLEmail); err != nil {
			s.Logger.Error("caddy_set_domain_failed", "error", err, "domain", *req.PanelDomain)
			msg := err.Error()
			resp.DomainApplyError = &msg
		} else {
			resp.DomainLiveApplied = true
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
