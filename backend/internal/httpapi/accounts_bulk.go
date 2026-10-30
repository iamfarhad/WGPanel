package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/store"
)

// POST /api/v1/accounts/bulk - one request, many accounts. There was no account bulk
// endpoint before (the only "bulk" code is the super-admin's reseller suspend/reactivate),
// so this is new rather than a replacement.
//
// Atomicity is per item: every item runs the same store call the single-account endpoint
// uses (its own transaction, and for resellers its own limits-row lock), so max_accounts,
// the traffic credit and the plan expiry are enforced item by item and a refused item
// never leaves a half-created account. Items are processed in order and the batch keeps
// going past a failed item, except that a create batch stops early once a reseller limit
// is hit (every later item would fail identically).

// maxBulkItems caps a batch: each item is a transaction (and for create, a keypair).
const maxBulkItems = 200

type bulkRequest struct {
	Action string `json:"action"` // create | renew | suspend | enable | delete | reset_usage | add_tags | remove_tags

	// create: count accounts labelled "<label_prefix>-NN". The account fields are those
	// of POST /accounts (template_id and explicit fields) - external_ref is not allowed.
	Count       int    `json:"count"`
	LabelPrefix string `json:"label_prefix"`
	createAccountRequest

	// the other actions: the accounts to act on. add_tags / remove_tags take the tags in
	// the embedded createAccountRequest's "tags" field (normalized like account tags; the
	// accounts' other tags are untouched).
	IDs []string `json:"ids"`

	// renew: at least one of these.
	ExtendDays *int     `json:"extend_days"`
	AddQuotaGB *float64 `json:"add_quota_gb"`
}

type bulkItemResult struct {
	ID      string           `json:"id"`
	Label   string           `json:"label"`
	OK      bool             `json:"ok"`
	Account *accountResponse `json:"account"`
	Error   *apiError        `json:"error"`
}

type bulkResponse struct {
	Action    string           `json:"action"`
	Total     int              `json:"total"`
	Succeeded int              `json:"succeeded"`
	Failed    int              `json:"failed"`
	Results   []bulkItemResult `json:"results"`
}

// bulkPermission is the API-key/role permission each action needs - the same one the
// equivalent single-account route requires.
func bulkPermission(action string) string {
	switch action {
	case "create":
		return "create"
	case "renew", "reset_usage", "add_tags", "remove_tags":
		return "update"
	case "suspend", "enable":
		return "suspend"
	case "delete":
		return "delete"
	}
	return ""
}

func identityHasPermission(identity *CallerIdentity, perm string) bool {
	if identity.IsAdmin {
		return perm == "read" || identity.AdminRole != "support"
	}
	return stringSliceContains(identity.Permissions, perm)
}

// bulkLabel numbers items with a width that fits the batch: prefix-01 ... prefix-10.
func bulkLabel(prefix string, i, count int) string {
	width := len(fmt.Sprint(count))
	if width < 2 {
		width = 2
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, i)
}

func (s *Server) handleBulkAccounts(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	perm := bulkPermission(req.Action)
	if perm == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "action must be one of create, renew, suspend, enable, delete, reset_usage, add_tags, remove_tags")
		return
	}
	if !identityHasPermission(identity, perm) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "you may not run the '"+req.Action+"' bulk action")
		return
	}

	resp := bulkResponse{Action: req.Action, Results: []bulkItemResult{}}
	record := func(res bulkItemResult) {
		resp.Results = append(resp.Results, res)
		if res.OK {
			resp.Succeeded++
		} else {
			resp.Failed++
		}
	}
	subBase := s.subscriptionBaseURLFromStore(ctx)
	finish := func(a store.Account) *accountResponse {
		peers, err := s.Store.ListAccountPeersWithNode(ctx, a.ID, ns)
		if err != nil {
			s.Logger.Error("list_account_peers_failed", "error", err)
		}
		out := s.decorateAccount(ctx, toAccountResponse(a, peers, subBase))
		s.fillRouting(ctx, []*accountResponse{&out})
		return &out
	}
	audit := func(action string, a store.Account, extra map[string]any) {
		detail := map[string]any{"bulk": true}
		for k, v := range extra {
			detail[k] = v
		}
		if err := s.Store.InsertAuditLog(ctx, auditActor(identity), action, a.ID, detail, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	if req.Action == "create" {
		if req.Count < 1 || req.Count > maxBulkItems {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("count must be between 1 and %d", maxBulkItems))
			return
		}
		prefix := strings.TrimSpace(req.LabelPrefix)
		if prefix == "" || len(prefix) > 80 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "label_prefix is required (max 80 characters)")
			return
		}
		if req.ExternalRef != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "external_ref cannot be used in a bulk create")
			return
		}
		tmpl, aerr := s.loadTemplate(ctx, req.TemplateID, ns)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		// Resolved once: every account of the batch shares the same settings and the same
		// expiry/deadline instant.
		spec, aerr := resolveAccountSpec(req.createAccountRequest, tmpl, time.Now())
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		resp.Total = req.Count
		for i := 1; i <= req.Count; i++ {
			label := bulkLabel(prefix, i, req.Count)
			a, aerr := s.createFromSpec(ctx, identity, spec, label)
			if aerr != nil {
				record(bulkItemResult{Label: label, Error: aerr})
				if isResellerLimitCode(aerr.Code) {
					// Every remaining item would fail the same way: report them as skipped
					// failures without hammering the database.
					for j := i + 1; j <= req.Count; j++ {
						record(bulkItemResult{Label: bulkLabel(prefix, j, req.Count), Error: aerr})
					}
					break
				}
				continue
			}
			audit("account.created", a, map[string]any{"template_id": req.TemplateID})
			record(bulkItemResult{ID: a.ID, Label: a.Label, OK: true, Account: finish(a)})
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Actions over existing accounts.
	if len(req.IDs) == 0 || len(req.IDs) > maxBulkItems {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("ids must list between 1 and %d accounts", maxBulkItems))
		return
	}
	if req.Action == "renew" {
		if req.ExtendDays == nil && req.AddQuotaGB == nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "renew needs extend_days and/or add_quota_gb")
			return
		}
		if req.AddQuotaGB != nil && *req.AddQuotaGB < 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "add_quota_gb must not be negative")
			return
		}
		if req.ExtendDays != nil && (*req.ExtendDays < 1 || *req.ExtendDays > 3650) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "extend_days must be between 1 and 3650")
			return
		}
	}
	var bulkTags []string
	if req.Action == "add_tags" || req.Action == "remove_tags" {
		var terr error
		if bulkTags, terr = store.NormalizeTags(req.Tags); terr != nil || len(bulkTags) == 0 {
			msg := "tags must list at least one tag"
			if terr != nil {
				msg = terr.Error()
			}
			writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
	}
	seen := map[string]bool{}
	resp.Total = len(req.IDs)
	for _, id := range req.IDs {
		if !uuidRE.MatchString(id) {
			record(bulkItemResult{ID: id, Error: badRequest("not a valid account id")})
			continue
		}
		if seen[id] {
			record(bulkItemResult{ID: id, Error: &apiError{Status: http.StatusConflict, Code: "duplicate_id", Message: "this id is listed more than once"}})
			continue
		}
		seen[id] = true

		var a store.Account
		var err error
		action := ""
		extra := map[string]any{}
		switch req.Action {
		case "renew":
			action = "account.renewed"
			a, err = s.renewAccount(ctx, id, ns, req.AddQuotaGB, req.ExtendDays)
		case "suspend":
			action = "account.suspended"
			extra["reason"] = "manual"
			a, err = s.Store.SuspendAccount(ctx, id, ns, "manual")
		case "enable":
			action = "account.enabled"
			a, err = s.Store.EnableAccount(ctx, id, ns)
		case "delete":
			action = "account.deleted"
			a, err = s.Store.SoftDeleteAccount(ctx, id, ns)
		case "reset_usage":
			action = "account.usage_reset"
			a, err = s.Store.ResetAccountUsage(ctx, id, ns)
		case "add_tags", "remove_tags":
			action = "account.updated"
			extra[req.Action] = bulkTags
			if req.Action == "add_tags" {
				err = s.Store.ModifyAccountTags(ctx, id, ns, bulkTags, nil)
			} else {
				err = s.Store.ModifyAccountTags(ctx, id, ns, nil, bulkTags)
			}
			if err == nil {
				a, err = s.Store.GetAccount(ctx, id, ns)
			}
		}
		if err != nil {
			e := toAPIError(err, req.Action)
			if e.Status == http.StatusInternalServerError {
				s.Logger.Error("bulk_item_failed", "action", req.Action, "error", err)
			}
			record(bulkItemResult{ID: id, Error: e})
			continue
		}
		audit(action, a, extra)
		record(bulkItemResult{ID: a.ID, Label: a.Label, OK: true, Account: finish(a)})
	}
	writeJSON(w, http.StatusOK, resp)
}

func isResellerLimitCode(code string) bool {
	switch code {
	case "reseller_account_limit_reached", "reseller_data_credit_exceeded", "reseller_expired", "reseller_disabled":
		return true
	}
	return false
}
