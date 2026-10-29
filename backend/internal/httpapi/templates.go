package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// Plan templates: CRUD under /api/v1/templates. Staff templates are global (optionally
// "available to resellers"); a reseller or bot key owns templates in its own namespace.

type templateRequest struct {
	Name                 string   `json:"name"`
	AvailableToResellers bool     `json:"available_to_resellers"` // staff-owned templates only
	DataQuotaGB          *float64 `json:"data_quota_gb"`          // null = unlimited
	DurationDays         *int     `json:"duration_days"`          // null = no expiry
	DeviceLimit          *int     `json:"device_limit"`
	BandwidthLimitMbps   *int     `json:"bandwidth_limit_mbps"`
	NodeGroup            *string  `json:"node_group"`
	ResetStrategy        string   `json:"reset_strategy"` // "" = none
	OnHold               bool     `json:"on_hold"`
	HoldDeadlineDays     *int     `json:"hold_deadline_days"`
}

type templateResponse struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	Owner                *string `json:"owner"` // null = global (staff) template
	AvailableToResellers bool    `json:"available_to_resellers"`
	DataQuotaBytes       *int64  `json:"data_quota_bytes"`
	DurationDays         *int    `json:"duration_days"`
	DeviceLimit          *int    `json:"device_limit"`
	BandwidthLimitMbps   *int    `json:"bandwidth_limit_mbps"`
	NodeGroup            *string `json:"node_group"`
	ResetStrategy        string  `json:"reset_strategy"`
	OnHold               bool    `json:"on_hold"`
	HoldDeadlineDays     *int    `json:"hold_deadline_days"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toTemplateResponse(t store.PlanTemplate) templateResponse {
	return templateResponse{
		ID: t.ID, Name: t.Name, Owner: t.OwnerNamespace, AvailableToResellers: t.AvailableToResellers,
		DataQuotaBytes: t.DataQuotaBytes, DurationDays: t.DurationDays, DeviceLimit: t.DeviceLimit,
		BandwidthLimitMbps: t.BandwidthLimitMbps, NodeGroup: t.NodeGroup, ResetStrategy: t.ResetStrategy,
		OnHold: t.OnHold, HoldDeadlineDays: t.HoldDeadlineDays,
		CreatedAt: t.CreatedAt.Format(time.RFC3339), UpdatedAt: t.UpdatedAt.Format(time.RFC3339),
	}
}

// templateParams validates a request body into store params.
func (r templateRequest) params() (store.TemplateParams, *apiError) {
	p := store.TemplateParams{
		Name: strings.TrimSpace(r.Name), AvailableToResellers: r.AvailableToResellers,
		DurationDays: r.DurationDays, DeviceLimit: r.DeviceLimit, BandwidthLimitMbps: r.BandwidthLimitMbps,
		ResetStrategy: r.ResetStrategy, OnHold: r.OnHold, HoldDeadlineDays: r.HoldDeadlineDays,
	}
	if p.ResetStrategy == "" {
		p.ResetStrategy = store.ResetNone
	}
	if p.Name == "" || len(p.Name) > 100 {
		return p, badRequest("name is required (max 100 characters)")
	}
	if !store.ValidResetStrategy(p.ResetStrategy) {
		return p, badRequest("reset_strategy must be none, day, week, month or year")
	}
	if r.DataQuotaGB != nil {
		if *r.DataQuotaGB < 0 {
			return p, badRequest("data_quota_gb must not be negative")
		}
		b := gbToBytes(*r.DataQuotaGB)
		p.DataQuotaBytes = &b
	}
	if r.DurationDays != nil && (*r.DurationDays < 1 || *r.DurationDays > maxHoldDays) {
		return p, badRequest("duration_days must be between 1 and 3650")
	}
	if r.DeviceLimit != nil && *r.DeviceLimit < 1 {
		return p, badRequest("device_limit must be at least 1")
	}
	if r.BandwidthLimitMbps != nil && (*r.BandwidthLimitMbps < 1 || *r.BandwidthLimitMbps > maxBandwidthLimitMbps) {
		return p, badRequest("bandwidth_limit_mbps must be between 1 and 100000 (omit for unlimited)")
	}
	if r.HoldDeadlineDays != nil && (*r.HoldDeadlineDays < 1 || *r.HoldDeadlineDays > maxHoldDays) {
		return p, badRequest("hold_deadline_days must be between 1 and 3650")
	}
	if r.NodeGroup != nil {
		g := strings.TrimSpace(*r.NodeGroup)
		if g != "" {
			p.NodeGroup = &g
		}
	}
	if p.OnHold {
		if p.DurationDays == nil {
			return p, badRequest("an on-hold template needs duration_days")
		}
	} else {
		p.HoldDeadlineDays = nil
	}
	return p, nil
}

func (s *Server) templateError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, store.ErrTemplateNotFound):
		writeJSONError(w, http.StatusNotFound, "template_not_found", "no template with that id")
	case errors.Is(err, store.ErrInvalidResetStrategy):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.Logger.Error("template_"+what+"_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not "+what+" template")
	}
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	list, err := s.Store.ListTemplates(r.Context(), callerNamespaceArg(identity))
	if err != nil {
		s.templateError(w, err, "list")
		return
	}
	out := make([]templateResponse, 0, len(list))
	for _, t := range list {
		out = append(out, toTemplateResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusNotFound, "template_not_found", "no template with that id")
		return
	}
	t, err := s.Store.GetTemplate(r.Context(), id, callerNamespaceArg(identity))
	if err != nil {
		s.templateError(w, err, "get")
		return
	}
	writeJSON(w, http.StatusOK, toTemplateResponse(t))
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	p, aerr := req.params()
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	t, err := s.Store.CreateTemplate(ctx, callerNamespaceArg(identity), p)
	if err != nil {
		s.templateError(w, err, "create")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "template.created", t.ID, map[string]string{"name": t.Name}, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusCreated, toTemplateResponse(t))
}

func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	p, aerr := req.params()
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusNotFound, "template_not_found", "no template with that id")
		return
	}
	t, err := s.Store.UpdateTemplate(ctx, id, callerNamespaceArg(identity), p)
	if err != nil {
		s.templateError(w, err, "update")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "template.updated", t.ID, map[string]string{"name": t.Name}, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	writeJSON(w, http.StatusOK, toTemplateResponse(t))
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeJSONError(w, http.StatusNotFound, "template_not_found", "no template with that id")
		return
	}
	if err := s.Store.DeleteTemplate(ctx, id, callerNamespaceArg(identity)); err != nil {
		s.templateError(w, err, "delete")
		return
	}
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "template.deleted", id, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
