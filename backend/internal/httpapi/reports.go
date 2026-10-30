package httpapi

import (
	"net/http"

	"wgpanel-api/internal/store"
)

// handleReports serves the reporting page. Scope is decided here, never by the client
// for non-staff callers: a reseller (session or reseller-owned key) and a plain API key
// are pinned to their own namespace by callerNamespaceArg and their reseller_id/owner
// query params are ignored. Only staff may narrow with ?reseller_id= / ?owner=.
//
// Staff-only extras: node CPU/RAM averages (infrastructure detail) and, for a
// super_admin looking at everything, the per-reseller leaderboard.
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)

	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = "7d"
	}
	if !store.ValidReportRange(rangeKey) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "range must be one of 24h, 7d, 30d, 90d, 365d")
		return
	}

	staff := identity.IsAdmin && identity.ResellerID == ""
	params := store.ReportParams{
		RangeKey:           rangeKey,
		CallerNamespace:    callerNamespaceArg(identity),
		IncludeNodeMetrics: staff,
	}
	if staff {
		q := r.URL.Query()
		if v := q.Get("owner"); v != "" {
			params.OwnerFilter = v
		}
		if v := q.Get("reseller_id"); v != "" {
			params.OwnerFilter = store.ResellerNamespace(v)
		}
		params.IncludeResellers = identity.AdminRole == "super_admin" && params.OwnerFilter == ""
	}

	// A reseller gets the same per-reseller tree, limited to itself and its descendants
	// (the store scopes it by CallerNamespace); empty for a reseller with no children.
	if identity.ResellerID != "" {
		params.IncludeResellers = true
	}

	report, err := s.Store.BuildReport(ctx, params)
	if err != nil {
		s.Logger.Error("build_report_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not build report")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
