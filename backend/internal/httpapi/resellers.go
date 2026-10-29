package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

// writeResellerError maps the store's reseller quota errors to HTTP. State-dependent
// refusals (the reseller is out of room right now) are 409; refusals that depend on
// what the request asked for (unlimited quota, too-long expiry, forbidden node group)
// are 422. Returns true when it wrote a response. Messages are human-readable; the
// `code` is the stable machine contract.
func writeResellerError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrResellerAccountLimit):
		writeJSONError(w, http.StatusConflict, "reseller_account_limit_reached", "you have reached your maximum number of accounts")
	case errors.Is(err, store.ErrResellerDataCredit):
		writeJSONError(w, http.StatusConflict, "reseller_data_credit_exceeded", "this would allocate more data than your remaining traffic credit")
	case errors.Is(err, store.ErrResellerUnlimitedNotAllowed):
		writeJSONError(w, http.StatusUnprocessableEntity, "reseller_unlimited_not_allowed", "a data quota is required: unlimited accounts are not allowed with a traffic credit")
	case errors.Is(err, store.ErrResellerExpiryTooLong):
		writeJSONError(w, http.StatusUnprocessableEntity, "reseller_expiry_too_long", "expiry is further out than your maximum allowed validity")
	case errors.Is(err, store.ErrResellerExpiryRequired):
		writeJSONError(w, http.StatusUnprocessableEntity, "reseller_expiry_required", "an expiry date is required (within your maximum allowed validity)")
	case errors.Is(err, store.ErrResellerNodeGroupNotAllowed):
		writeJSONError(w, http.StatusUnprocessableEntity, "reseller_node_group_not_allowed", "you may not place accounts on that node's group")
	case errors.Is(err, store.ErrResellerExpired):
		writeJSONError(w, http.StatusUnauthorized, "reseller_expired", "this reseller plan has expired")
	case errors.Is(err, store.ErrResellerDisabled):
		writeJSONError(w, http.StatusForbidden, "reseller_disabled", "this reseller account is disabled")
	case errors.Is(err, store.ErrAccountLockedByAdmin):
		writeJSONError(w, http.StatusForbidden, "account_suspended_by_admin", "this account was suspended by an administrator and can only be reactivated by one")
	case errors.Is(err, store.ErrInvalidQuota):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "data quota must not be negative")
	default:
		return false
	}
	return true
}

// resellerBlockCode is the login/refresh gate: "" for an existing, enabled,
// unexpired reseller, otherwise the 401 code to return. A lookup failure fails closed.
func (s *Server) resellerBlockCode(ctx context.Context, adminID string) string {
	l, err := s.resellerLimits(ctx, adminID)
	switch {
	case err != nil:
		return "account_disabled"
	case l.Disabled:
		return "account_disabled"
	case l.ExpiresAt != nil && !l.ExpiresAt.After(time.Now()):
		return "reseller_expired"
	}
	return ""
}

func expiresAtString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.RFC3339)
	return &v
}

type resellerUsageResponse struct {
	AccountsUsed       int   `json:"accounts_used"`
	DataAllocatedBytes int64 `json:"data_allocated_bytes"`
	DataUsedBytes      int64 `json:"data_used_bytes"`
}

// resellerResponse carries limits flat (so a form can bind to it directly) plus a
// usage block. null limits mean unlimited; empty node_groups means all groups.
type resellerResponse struct {
	ID             string                `json:"id"`
	Username       string                `json:"username"`
	Namespace      string                `json:"namespace"`
	Disabled       bool                  `json:"disabled"`
	Notes          string                `json:"notes"`
	MaxAccounts    *int                  `json:"max_accounts"`
	DataLimitBytes *int64                `json:"data_limit_bytes"`
	NodeGroups     []string              `json:"node_groups"`
	ExpiresAt      *string               `json:"expires_at"` // RFC3339; null = never expires
	Usage          resellerUsageResponse `json:"usage"`
	CreatedAt      string                `json:"created_at"`
	UpdatedAt      string                `json:"updated_at"`
}

func toResellerResponse(r store.Reseller) resellerResponse {
	groups := r.NodeGroups
	if groups == nil {
		groups = []string{}
	}
	return resellerResponse{
		ID: r.AdminID, Username: r.Username, Namespace: store.ResellerNamespace(r.AdminID),
		Disabled: r.Disabled, Notes: r.Notes,
		MaxAccounts: r.MaxAccounts, DataLimitBytes: r.DataLimitBytes, NodeGroups: groups, ExpiresAt: expiresAtString(r.ExpiresAt),
		Usage: resellerUsageResponse{
			AccountsUsed: r.Usage.AccountsUsed, DataAllocatedBytes: r.Usage.DataAllocatedBytes, DataUsedBytes: r.Usage.DataUsedBytes,
		},
		CreatedAt: r.CreatedAt.Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
	}
}

// handleMe tells the SPA who is logged in (id/username/role) so it can choose the
// reseller or staff UI without decoding the JWT; for a reseller it also embeds the
// same limits+usage payload as GET /api/v1/reseller/me.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	out := map[string]any{
		"id": identity.AdminID, "username": identity.AdminUsername, "role": identity.AdminRole, "reseller": nil,
	}
	if identity.ResellerID != "" {
		res, err := s.Store.GetReseller(r.Context(), identity.ResellerID)
		if err != nil {
			s.Logger.Error("get_reseller_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load reseller")
			return
		}
		out["reseller"] = toResellerResponse(res)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleResellerMe is the reseller's own limits + live usage. 403 for any non-reseller
// caller (it has no reseller record to show).
func (s *Server) handleResellerMe(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	if identity.ResellerID == "" {
		writeJSONError(w, http.StatusForbidden, "forbidden", "this endpoint is for reseller accounts")
		return
	}
	res, err := s.Store.GetReseller(r.Context(), identity.ResellerID)
	if err != nil {
		s.Logger.Error("get_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load reseller")
		return
	}
	writeJSON(w, http.StatusOK, toResellerResponse(res))
}

func (s *Server) handleListResellers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListResellers(r.Context())
	if err != nil {
		s.Logger.Error("list_resellers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list resellers")
		return
	}
	out := make([]resellerResponse, 0, len(list))
	for _, x := range list {
		out = append(out, toResellerResponse(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"resellers": out})
}

type createResellerRequest struct {
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Notes       string   `json:"notes"`
	Disabled    bool     `json:"disabled"`
	MaxAccounts *int     `json:"max_accounts"`
	DataLimitGB *float64 `json:"data_limit_gb"`
	NodeGroups  []string `json:"node_groups"`
	ExpiresAt   *string  `json:"expires_at"`
}

// validateResellerLimits is shared by create and patch; the DB CHECKs are the backstop.
func validateResellerLimits(maxAccounts *int, dataLimitGB *float64, groups []string) string {
	switch {
	case maxAccounts != nil && *maxAccounts < 0:
		return "max_accounts must be >= 0 (null = unlimited)"
	case dataLimitGB != nil && *dataLimitGB < 0:
		return "data_limit_gb must be >= 0 (null = unlimited)"
	}
	for _, g := range groups {
		if strings.TrimSpace(g) == "" {
			return "node_groups must not contain empty names"
		}
	}
	return ""
}

func (s *Server) handleCreateReseller(w http.ResponseWriter, r *http.Request) {
	var req createResellerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Username == "" || len(req.Password) < 8 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username is required and password must be at least 8 characters")
		return
	}
	if msg := validateResellerLimits(req.MaxAccounts, req.DataLimitGB, req.NodeGroups); msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil || !t.After(time.Now()) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "expires_at must be a future RFC3339 timestamp (or null for never)")
			return
		}
		expiresAt = &t
	}
	hash, err := authcrypto.HashPassword(req.Password)
	if err != nil {
		s.Logger.Error("hash_password_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not hash password")
		return
	}
	var dataLimit *int64
	if req.DataLimitGB != nil {
		b := gbToBytes(*req.DataLimitGB)
		dataLimit = &b
	}

	ctx := r.Context()
	res, err := s.Store.CreateReseller(ctx, store.NewResellerParams{
		Username: req.Username, PasswordHash: hash, MaxAccounts: req.MaxAccounts, DataLimitBytes: dataLimit,
		NodeGroups: req.NodeGroups, ExpiresAt: expiresAt, Disabled: req.Disabled, Notes: req.Notes,
	})
	if errors.Is(err, store.ErrUsernameTaken) {
		writeJSONError(w, http.StatusConflict, "username_taken", "an admin with this username already exists")
		return
	}
	if err != nil {
		s.Logger.Error("create_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create reseller")
		return
	}
	s.auditReseller(r, "reseller.created", res.Username, nil)
	writeJSON(w, http.StatusCreated, toResellerResponse(res))
}

// auditReseller records a management action against the acting super admin.
func (s *Server) auditReseller(r *http.Request, action, target string, detail any) {
	identity, ok := callerIdentityFromContext(r.Context())
	if !ok {
		return
	}
	if err := s.Store.InsertAuditLog(r.Context(), identity.AdminUsername, action, target, detail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}

func (s *Server) handleGetReseller(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.GetReseller(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch reseller")
		return
	}
	writeJSON(w, http.StatusOK, toResellerResponse(res))
}

// updateResellerRequest: an omitted field is left alone; an explicit null on a limit
// means "unlimited".
type updateResellerRequest struct {
	Password    *string              `json:"password"`
	Notes       *string              `json:"notes"`
	Disabled    *bool                `json:"disabled"`
	MaxAccounts store.Opt[int]       `json:"max_accounts"`
	DataLimitGB store.Opt[float64]   `json:"data_limit_gb"`
	NodeGroups  *[]string            `json:"node_groups"`
	ExpiresAt   store.Opt[time.Time] `json:"expires_at"`
}

func (s *Server) handleUpdateReseller(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateResellerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	var groups []string
	if req.NodeGroups != nil {
		groups = *req.NodeGroups
	}
	if msg := validateResellerLimits(req.MaxAccounts.Value, req.DataLimitGB.Value, groups); msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if req.Password != nil && len(*req.Password) < 8 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	patch := store.ResellerLimitsPatch{
		MaxAccounts: req.MaxAccounts, ExpiresAt: req.ExpiresAt,
		NodeGroups: req.NodeGroups, Disabled: req.Disabled, Notes: req.Notes,
	}
	if req.DataLimitGB.Set {
		patch.DataLimitBytes.Set = true
		if req.DataLimitGB.Value != nil {
			b := gbToBytes(*req.DataLimitGB.Value)
			patch.DataLimitBytes.Value = &b
		}
	}
	if err := s.Store.UpdateResellerLimits(ctx, id, patch); errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	} else if err != nil {
		s.Logger.Error("update_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update reseller")
		return
	}
	if req.Password != nil {
		hash, err := authcrypto.HashPassword(*req.Password)
		if err != nil {
			s.Logger.Error("hash_password_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not hash password")
			return
		}
		if err := s.Store.ResetAdminPassword(ctx, id, hash); err != nil {
			s.Logger.Error("reset_admin_password_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reset password")
			return
		}
	}

	res, err := s.Store.GetReseller(ctx, id)
	if err != nil {
		s.Logger.Error("get_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch reseller")
		return
	}
	s.auditReseller(r, "reseller.updated", res.Username, map[string]any{"password_reset": req.Password != nil})
	writeJSON(w, http.StatusOK, toResellerResponse(res))
}

// handleDeleteReseller refuses (409 reseller_has_accounts) while the reseller still
// owns non-deleted accounts, rather than orphaning them or silently mass-deleting
// customers' VPN access. Disable the reseller (and bulk-suspend or delete its
// accounts) first.
func (s *Server) handleDeleteReseller(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	res, err := s.Store.GetReseller(ctx, id)
	if errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch reseller")
		return
	}
	switch err := s.Store.DeleteReseller(ctx, id); {
	case errors.Is(err, store.ErrResellerHasAccounts):
		writeJSONError(w, http.StatusConflict, "reseller_has_accounts", "this reseller still owns accounts; delete them first (disable the reseller meanwhile)")
		return
	case errors.Is(err, store.ErrResellerNotFound):
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	case err != nil:
		s.Logger.Error("delete_reseller_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete reseller")
		return
	}
	s.auditReseller(r, "reseller.deleted", res.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSuspendResellerAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	n, err := s.Store.SuspendResellerAccounts(ctx, id)
	if errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	if err != nil {
		s.Logger.Error("suspend_reseller_accounts_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not suspend accounts")
		return
	}
	s.auditReseller(r, "reseller.accounts_suspended", id, map[string]int{"count": n})
	writeJSON(w, http.StatusOK, map[string]any{"suspended": n, "reason": store.SuspendReasonReseller})
}

func (s *Server) handleReactivateResellerAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	n, still, err := s.Store.ReactivateResellerAccounts(ctx, id)
	if errors.Is(err, store.ErrResellerNotFound) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	if err != nil {
		s.Logger.Error("reactivate_reseller_accounts_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reactivate accounts")
		return
	}
	s.auditReseller(r, "reseller.accounts_reactivated", id, map[string]int{"count": n, "still_suspended": still})
	writeJSON(w, http.StatusOK, map[string]any{"reactivated": n, "still_suspended": still})
}
