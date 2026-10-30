package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"wgpanel-api/internal/store"
)

// Sub-resellers: a reseller managing the resellers below it. Staff manage the same
// tree through /api/v1/resellers (parent_id on create, depth/parent_id in every row);
// these /api/v1/reseller/children routes are the reseller-session view of it, always
// pinned to the caller's own subtree.
//
// What a parent may do to a descendant (decided once, here):
//   - see it and its accounts (reads), suspend/enable its accounts, bulk-suspend and
//     bulk-reactivate its subtree's accounts, edit its limits within the carving rules,
//     disable/enable it, and delete it when it is clean;
//   - NOT: edit, renew, delete or download the config of its accounts (those move the
//     child's own allocation / hand out its keys), reset its password, read its API
//     keys, wallets or webhooks, or re-enable a reseller that staff disabled.
// A child can never see or touch its parent's or siblings' data: every account read is
// the caller's own namespace plus its descendants', and an id outside the subtree is a
// 404, indistinguishable from an id that does not exist.

// resellerExtraRoutes extends the reseller-session allowlist (reseller_gate.go) from here
// so this feature does not have to touch the shared literal.
func init() {
	for _, p := range []string{
		"GET /api/v1/reseller/children",
		"POST /api/v1/reseller/children",
		"PATCH /api/v1/reseller/children/{id}",
		"DELETE /api/v1/reseller/children/{id}",
		"POST /api/v1/reseller/children/{id}/suspend-accounts",
		"POST /api/v1/reseller/children/{id}/reactivate-accounts",
		"GET /api/v1/reseller/branding",
		"PUT /api/v1/reseller/branding",
	} {
		resellerRoutes[p] = true
	}
}

func (s *Server) registerResellerTreeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/reseller/children", s.requireAdmin(http.HandlerFunc(s.handleListChildren)))
	mux.Handle("POST /api/v1/reseller/children", s.requireAdmin(http.HandlerFunc(s.handleCreateChild)))
	mux.Handle("PATCH /api/v1/reseller/children/{id}", s.requireAdmin(http.HandlerFunc(s.handleUpdateChild)))
	mux.Handle("DELETE /api/v1/reseller/children/{id}", s.requireAdmin(http.HandlerFunc(s.handleDeleteChild)))
	mux.Handle("POST /api/v1/reseller/children/{id}/suspend-accounts", s.requireAdmin(http.HandlerFunc(s.handleSuspendChildAccounts)))
	mux.Handle("POST /api/v1/reseller/children/{id}/reactivate-accounts", s.requireAdmin(http.HandlerFunc(s.handleReactivateChildAccounts)))
	s.registerBrandingRoutes(mux)
}

// resellerCaller returns the signed-in reseller's id, or writes 403: these routes have
// no meaning for staff (who use /api/v1/resellers) and are JWT-only, so a reseller-owned
// API key never gets here.
func resellerCaller(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity, ok := callerIdentityFromContext(r.Context())
	if !ok || identity.ResellerID == "" {
		writeJSONError(w, http.StatusForbidden, "forbidden", "this endpoint is for reseller accounts")
		return "", false
	}
	return identity.ResellerID, true
}

// descendantOr404 resolves {id} inside the caller's subtree; anything else is 404.
func (s *Server) descendantOr404(w http.ResponseWriter, r *http.Request, callerID string) (store.Reseller, bool) {
	res, err := s.Store.GetDescendant(r.Context(), callerID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotDescendant) {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return store.Reseller{}, false
	}
	if err != nil {
		s.Logger.Error("get_descendant_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch reseller")
		return store.Reseller{}, false
	}
	return res, true
}

// handleListChildren returns every descendant (children, grandchildren, ...) flat,
// parents first; each row carries parent_id and depth so the client builds the tree.
func (s *Server) handleListChildren(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	list, err := s.Store.ListDescendants(r.Context(), callerID)
	if err != nil {
		s.Logger.Error("list_descendants_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list resellers")
		return
	}
	out := make([]resellerResponse, 0, len(list))
	for _, x := range list {
		out = append(out, toResellerResponse(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"resellers": out, "max_depth": store.MaxResellerDepth})
}

func (s *Server) handleCreateChild(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	var req createResellerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	// The parent is always the caller; a client-supplied parent_id is ignored.
	s.createReseller(w, r, req, &callerID, true)
}

func (s *Server) handleUpdateChild(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	res, ok := s.descendantOr404(w, r, callerID)
	if !ok {
		return
	}
	s.updateReseller(w, r, res.AdminID, true)
}

func (s *Server) handleDeleteChild(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	res, ok := s.descendantOr404(w, r, callerID)
	if !ok {
		return
	}
	s.deleteReseller(w, r, res.AdminID)
}

func (s *Server) handleSuspendChildAccounts(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	res, ok := s.descendantOr404(w, r, callerID)
	if !ok {
		return
	}
	n, err := s.Store.SuspendChildAccounts(r.Context(), res.AdminID)
	if err != nil {
		s.Logger.Error("suspend_child_accounts_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not suspend accounts")
		return
	}
	s.auditReseller(r, "reseller.child_accounts_suspended", res.Username, map[string]int{"count": n})
	writeJSON(w, http.StatusOK, map[string]any{"suspended": n, "reason": store.SuspendReasonParent})
}

func (s *Server) handleReactivateChildAccounts(w http.ResponseWriter, r *http.Request) {
	callerID, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	res, ok := s.descendantOr404(w, r, callerID)
	if !ok {
		return
	}
	n, still, err := s.Store.ReactivateChildAccounts(r.Context(), res.AdminID)
	if err != nil {
		s.Logger.Error("reactivate_child_accounts_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reactivate accounts")
		return
	}
	s.auditReseller(r, "reseller.child_accounts_reactivated", res.Username, map[string]int{"count": n, "still_suspended": still})
	writeJSON(w, http.StatusOK, map[string]any{"reactivated": n, "still_suspended": still})
}
