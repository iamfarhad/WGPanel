package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// Shared account-creation plumbing for POST /accounts, bulk create and plan templates.

// apiError is an error already mapped to its HTTP form; bulk items carry it per item.
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message }

func badRequest(msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	maxHoldDays     = 3650
	maxScheduleDays = 3650
)

// accountSpec is a fully resolved set of account settings: the request's explicit
// fields laid over the template's.
type accountSpec struct {
	ExternalRef      *string
	NodeID           string
	Quota            *int64
	Expiry           *time.Time
	DeviceLimit      *int
	Bandwidth        *int
	HoldDays         *int
	HoldDeadline     *time.Time
	ResetStrategy    string
	TemplateNodeGrp  *string
	TemplateResolved bool
	// Note/Tags are the normalized note and tags to store after creation (nil = none).
	Note *string
	Tags *[]string
	// Routing is the validated routing mode/CIDRs/precision (routing_api.go).
	Routing resolvedRouting
}

// resolveAccountSpec applies the precedence "explicit request field > template value >
// default". Pure (now is injected) so it is unit-testable.
func resolveAccountSpec(req createAccountRequest, tmpl *store.PlanTemplate, now time.Time) (accountSpec, *apiError) {
	var sp accountSpec
	sp.ExternalRef = req.ExternalRef
	var tagsIn *[]string
	if req.Tags != nil {
		tagsIn = &req.Tags
	}
	var merr *apiError
	if sp.Note, sp.Tags, merr = validateAccountMeta(req.Note, tagsIn); merr != nil {
		return sp, merr
	}
	if req.NodeID != nil {
		sp.NodeID = *req.NodeID
	}

	if req.DataQuotaGB != nil {
		if *req.DataQuotaGB < 0 {
			return sp, badRequest("data_quota_gb must not be negative")
		}
		b := gbToBytes(*req.DataQuotaGB)
		sp.Quota = &b
	} else if tmpl != nil {
		sp.Quota = tmpl.DataQuotaBytes
	}

	if req.BandwidthLimitMbps != nil {
		if *req.BandwidthLimitMbps < 1 || *req.BandwidthLimitMbps > maxBandwidthLimitMbps {
			return sp, badRequest("bandwidth_limit_mbps must be between 1 and 100000 (omit for unlimited)")
		}
		sp.Bandwidth = req.BandwidthLimitMbps
	} else if tmpl != nil {
		sp.Bandwidth = tmpl.BandwidthLimitMbps
	}

	if req.DeviceLimit != nil {
		sp.DeviceLimit = req.DeviceLimit
	} else if tmpl != nil {
		sp.DeviceLimit = tmpl.DeviceLimit
	}

	rt, aerr := resolveRoutingForCreate(req.routingRequest, tmpl)
	if aerr != nil {
		return sp, aerr
	}
	sp.Routing = rt

	sp.ResetStrategy = store.ResetNone
	if tmpl != nil {
		sp.ResetStrategy = tmpl.ResetStrategy
		sp.TemplateNodeGrp = tmpl.NodeGroup
		sp.TemplateResolved = true
	}
	if req.ResetStrategy != nil {
		sp.ResetStrategy = *req.ResetStrategy
	}
	if sp.ResetStrategy == "" {
		sp.ResetStrategy = store.ResetNone
	}
	if !store.ValidResetStrategy(sp.ResetStrategy) {
		return sp, badRequest("reset_strategy must be none, day, week, month or year")
	}

	var explicitExpiry *time.Time
	if req.ExpiryAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiryAt)
		if err != nil {
			return sp, badRequest("expiry_at must be RFC3339")
		}
		explicitExpiry = &t
	}

	onHold := tmpl != nil && tmpl.OnHold
	if req.OnHold != nil {
		onHold = *req.OnHold
	}
	if onHold {
		if explicitExpiry != nil {
			return sp, badRequest("expiry_at cannot be combined with on_hold: the expiry starts at the first connection")
		}
		days := req.HoldDurationDays
		if days == nil && tmpl != nil {
			days = tmpl.DurationDays
		}
		if days == nil {
			return sp, badRequest("an on-hold account needs hold_duration_days (or a template with a duration)")
		}
		if *days < 1 || *days > maxHoldDays {
			return sp, badRequest("hold_duration_days must be between 1 and 3650")
		}
		sp.HoldDays = days
		dl := req.HoldDeadlineDays
		if dl == nil && tmpl != nil {
			dl = tmpl.HoldDeadlineDays
		}
		if dl != nil {
			if *dl < 1 || *dl > maxHoldDays {
				return sp, badRequest("hold_deadline_days must be between 1 and 3650")
			}
			t := now.Add(time.Duration(*dl) * 24 * time.Hour)
			sp.HoldDeadline = &t
		}
		return sp, nil
	}

	if explicitExpiry != nil {
		sp.Expiry = explicitExpiry
	} else if tmpl != nil && tmpl.DurationDays != nil {
		t := now.Add(time.Duration(*tmpl.DurationDays) * 24 * time.Hour)
		sp.Expiry = &t
	}
	return sp, nil
}

// loadTemplate resolves req.TemplateID within the caller's visibility.
func (s *Server) loadTemplate(ctx context.Context, templateID *string, ns *string) (*store.PlanTemplate, *apiError) {
	if templateID == nil || *templateID == "" {
		return nil, nil
	}
	if !uuidRE.MatchString(*templateID) {
		return nil, badRequest("template_id must be a UUID")
	}
	t, err := s.Store.GetTemplate(ctx, *templateID, ns)
	if errors.Is(err, store.ErrTemplateNotFound) {
		return nil, &apiError{Status: http.StatusNotFound, Code: "template_not_found", Message: "no template with that id"}
	}
	if err != nil {
		s.Logger.Error("get_template_failed", "error", err)
		return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not load template"}
	}
	return &t, nil
}

// ownerAndGroups derives who owns a new account and which node groups it may be placed
// on, exactly as account creation always has.
func ownerAndGroups(identity *CallerIdentity) (owner *string, groups []string) {
	if identity.ResellerID != "" {
		// The store re-reads the reseller's node_groups under its row lock, so a
		// session passes none. A reseller-owned API key passes its own (narrower) set,
		// which the store intersects with the reseller's.
		ns := store.ResellerNamespace(identity.ResellerID)
		owner = &ns
		if !identity.IsAdmin {
			groups = identity.NodeGroups
		}
	} else if !identity.IsAdmin {
		ns := identity.KeyNamespace
		owner = &ns
		groups = identity.NodeGroups
	}
	return owner, groups
}

// createFromSpec provisions one account (keypair, subscription token, store call).
// Errors are returned already mapped to apiError.
func (s *Server) createFromSpec(ctx context.Context, identity *CallerIdentity, sp accountSpec, label string) (store.Account, *apiError) {
	internal := func(msg string, err error) (store.Account, *apiError) {
		s.Logger.Error("create_account_failed", "step", msg, "error", err)
		return store.Account{}, &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not create account"}
	}
	token, err := newSubscriptionToken()
	if err != nil {
		return internal("token", err)
	}
	kp, err := wgkeys.GenerateKeyPair()
	if err != nil {
		return internal("keypair", err)
	}
	encPriv, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, kp.PrivateKey)
	if err != nil {
		return internal("encrypt", err)
	}
	encPSK, err := newEncryptedPSK(s.AccountKeyEncryptionKey)
	if err != nil {
		return internal("psk", err)
	}

	owner, groups := ownerAndGroups(identity)
	// A template's node group narrows placement when no specific node is pinned.
	if sp.TemplateNodeGrp != nil && *sp.TemplateNodeGrp != "" && (sp.NodeID == "" || sp.NodeID == "auto") {
		g := *sp.TemplateNodeGrp
		if len(groups) > 0 && !stringSliceContains(groups, g) {
			return store.Account{}, &apiError{Status: http.StatusForbidden, Code: "node_group_not_allowed", Message: "this API key is not scoped to the template's node group"}
		}
		groups = []string{g}
	}

	account, err := s.Store.CreateAccount(ctx, store.CreateAccountParams{
		ExternalRef:         sp.ExternalRef,
		Label:               label,
		NodeIDOrAuto:        sp.NodeID,
		PublicKey:           kp.PublicKey,
		PrivateKeyEncrypted: encPriv,
		DataQuotaBytes:      sp.Quota,
		ExpiryAt:            sp.Expiry,
		DeviceLimit:         sp.DeviceLimit,
		BandwidthLimitMbps:  sp.Bandwidth,
		SubscriptionToken:   token,
		OwnerKeyNamespace:   owner,
		AllowedNodeGroups:   groups,
		HoldDurationDays:    sp.HoldDays,
		HoldDeadlineAt:      sp.HoldDeadline,
		ResetStrategy:       sp.ResetStrategy,

		RoutingMode:           sp.Routing.Mode,
		RoutingExcludedCIDRs:  sp.Routing.CIDRs,
		RoutingPrecision:      sp.Routing.Precision,
		PresharedKeyEncrypted: &encPSK,
	})
	if err != nil {
		return store.Account{}, toAPIError(err, "create")
	}
	// The account exists even if annotating it fails, so a failure here only logs.
	if err := s.applyAccountMeta(ctx, account.ID, owner, sp.Note, sp.Tags); err != nil {
		s.Logger.Error("apply_account_meta_failed", "error", err)
	}
	return account, nil
}

// captureWriter lets writeResellerError (the single mapping of reseller errors) be
// reused to produce a per-item error for bulk responses.
type captureWriter struct {
	h      http.Header
	status int
	body   []byte
}

func (c *captureWriter) Header() http.Header { return c.h }
func (c *captureWriter) WriteHeader(s int)   { c.status = s }
func (c *captureWriter) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return len(b), nil
}

// toAPIError maps any store error from an account operation to its HTTP form, using the
// same codes the single-account endpoints return.
func toAPIError(err error, op string) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	cw := &captureWriter{h: http.Header{}}
	if writeResellerError(cw, err) {
		var env errorEnvelope
		if json.Unmarshal(cw.body, &env) == nil {
			return &apiError{Status: cw.status, Code: env.Error.Code, Message: env.Error.Message}
		}
	}
	switch {
	case errors.Is(err, store.ErrAccountNotFound):
		return &apiError{Status: http.StatusNotFound, Code: "account_not_found", Message: "no account with that id"}
	case errors.Is(err, store.ErrNodeNotFound):
		return &apiError{Status: http.StatusNotFound, Code: "node_not_found", Message: "no node with that id"}
	case errors.Is(err, store.ErrNodeNotRegistered):
		return &apiError{Status: http.StatusConflict, Code: "node_not_registered", Message: "node has not completed registration yet"}
	case errors.Is(err, store.ErrNodeInMaintenance):
		return &apiError{Status: http.StatusConflict, Code: "node_in_maintenance", Message: "node is in maintenance and takes no new accounts"}
	case errors.Is(err, store.ErrNodeGroupNotAllowed):
		return &apiError{Status: http.StatusForbidden, Code: "node_group_not_allowed", Message: "this API key is not scoped to that node's group"}
	case errors.Is(err, store.ErrNodeCapacityExceeded):
		return &apiError{Status: http.StatusConflict, Code: "node_capacity_exceeded", Message: "node has no remaining capacity"}
	case errors.Is(err, store.ErrNoAvailableNode):
		return &apiError{Status: http.StatusConflict, Code: "no_available_node", Message: "no registered node has remaining capacity"}
	case errors.Is(err, store.ErrExternalRefTaken):
		return &apiError{Status: http.StatusConflict, Code: "external_ref_taken", Message: "an account with this external_ref already exists"}
	case errors.Is(err, store.ErrAccountQuotaExhausted):
		return &apiError{Status: http.StatusConflict, Code: "quota_exhausted", Message: "this account has used its whole data quota and would be suspended again at once: raise the quota, renew with add_quota_gb or reset its usage first"}
	case errors.Is(err, store.ErrInvalidResetStrategy), errors.Is(err, store.ErrHoldWithExpiry), errors.Is(err, store.ErrInvalidTag):
		return badRequest(err.Error())
	}
	return &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "could not " + op + " account"}
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	writeJSONError(w, e.Status, e.Code, e.Message)
}

// renewAccount renews one account. For an account still on hold the validity clock has
// not started, so extend_days lengthens its hold duration instead of moving expiry_at.
func (s *Server) renewAccount(ctx context.Context, id string, ns *string, addQuotaGB *float64, extendDays *int) (store.Account, error) {
	var addQuotaBytes *int64
	if addQuotaGB != nil {
		b := gbToBytes(*addQuotaGB)
		addQuotaBytes = &b
	}
	var newExpiry *time.Time
	var addHold *int
	if extendDays != nil {
		existing, err := s.Store.GetAccount(ctx, id, ns)
		if err != nil {
			return store.Account{}, err
		}
		if existing.OnHold() {
			if *existing.HoldDurationDays+*extendDays < 1 {
				return store.Account{}, badRequest("extend_days would leave the on-hold duration below 1 day")
			}
			addHold = extendDays
		} else {
			base := time.Now()
			if existing.ExpiryAt != nil && existing.ExpiryAt.After(base) {
				base = *existing.ExpiryAt
			}
			t := base.Add(time.Duration(*extendDays) * 24 * time.Hour)
			newExpiry = &t
		}
	}
	return s.Store.RenewAccountHold(ctx, id, ns, addQuotaBytes, newExpiry, addHold)
}
