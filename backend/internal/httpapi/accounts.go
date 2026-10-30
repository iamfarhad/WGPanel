package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"wgpanel-api/internal/events"

	"wgpanel-api/internal/store"
)

type createAccountRequest struct {
	ExternalRef        *string  `json:"external_ref"`
	Label              string   `json:"label"`
	NodeID             *string  `json:"node_id"` // omitted/"auto" = every eligible node; a specific UUID pins to just that one
	DataQuotaGB        *float64 `json:"data_quota_gb"`
	ExpiryAt           *string  `json:"expiry_at"` // RFC3339
	DeviceLimit        *int     `json:"device_limit"`
	BandwidthLimitMbps *int     `json:"bandwidth_limit_mbps"` // omitted/null = unshaped
	// TemplateID applies a plan template; any explicit field above overrides it.
	TemplateID *string `json:"template_id"`
	// OnHold starts the expiry clock at the first handshake instead of now; the
	// duration is HoldDurationDays (or the template's duration_days). HoldDeadlineDays
	// activates the account anyway after that many days without a handshake.
	OnHold           *bool   `json:"on_hold"`
	HoldDurationDays *int    `json:"hold_duration_days"`
	HoldDeadlineDays *int    `json:"hold_deadline_days"`
	ResetStrategy    *string `json:"reset_strategy"` // none|day|week|month|year
	// Split-tunnel routing (routing_api.go): routing_mode, routing_excluded_cidrs, routing_precision.
	routingRequest
}

// maxBandwidthLimitMbps rejects obviously-nonsense rates (100 Gbps) before they
// reach tc, which would accept and silently mangle them.
const maxBandwidthLimitMbps = 100_000

// peerOnlineWindow matches docs/PRD-monitoring-stats.md §5: a peer counts as
// online iff its last reported WireGuard handshake was within this window.
const peerOnlineWindow = 180 * time.Second

type accountPeerResponse struct {
	NodeID          string  `json:"node_id"`
	NodeName        string  `json:"node_name"`
	AssignedIP      string  `json:"assigned_ip"`
	Online          bool    `json:"online"`
	LastHandshakeAt *string `json:"last_handshake_at"`
}

type accountResponse struct {
	ID             string                `json:"id"`
	ExternalRef    *string               `json:"external_ref"`
	Label          string                `json:"label"`
	PublicKey      string                `json:"public_key"`
	Peers          []accountPeerResponse `json:"peers"`
	DataQuotaBytes *int64                `json:"data_quota_bytes"`
	DataUsedBytes  int64                 `json:"data_used_bytes"`
	ExpiryAt       *string               `json:"expiry_at"`
	DeviceLimit    *int                  `json:"device_limit"`
	// DeviceLimitExceeded is PRD §6.4's standing soft-enforcement flag; HardEnforce is
	// the per-account toggle that upgrades it to an automatic suspend.
	DeviceLimitExceeded    bool `json:"device_limit_exceeded"`
	DeviceLimitHardEnforce bool `json:"device_limit_hard_enforce"`
	BandwidthLimitMbps     *int `json:"bandwidth_limit_mbps"`
	// SubscriptionPath is the relative capability URL serving this account's current
	// config (prepend the panel's public origin). The token is deliberately not
	// exposed as a separate field - the path is the only shape clients need.
	SubscriptionPath string `json:"subscription_path"`
	// SubscriptionURL is the absolute form of SubscriptionPath on the separate
	// subscription origin (Settings' sub_domain/sub_port, migration 0019), or null
	// when no separate origin is configured - callers should then fall back to
	// prepending the panel's own origin to SubscriptionPath, as before.
	SubscriptionURL *string `json:"subscription_url"`
	Status          string  `json:"status"`
	SuspendReason   *string `json:"suspend_reason"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	// LastHandshakeAt is the newest last_handshake_at across the account's peers
	// (RFC3339), null while none has ever connected. Computed in SQL (store.Account.
	// LastHandshakeAt), so the list endpoint pays no extra query for it.
	LastHandshakeAt *string `json:"last_handshake_at"`
	// NodeID/AssignedIP are deprecated - kept for one release as a bot-integration
	// compatibility bridge from the pre-multi-node single-peer model (see
	// docs/STORY-09-multi-node-accounts.md). They mirror the account's first peer
	// (by creation order), or null if it has none yet. New integrations should read
	// Peers instead.
	NodeID     *string `json:"node_id"`
	AssignedIP *string `json:"assigned_ip"`
	// Owner is the raw owner namespace ("reseller:<admin id>", an API key id, or null
	// for an account created by a staff admin); ResellerID is the admin id when the
	// owner is a reseller - what the UI joins against GET /api/v1/resellers.
	Owner      *string `json:"owner"`
	ResellerID *string `json:"reseller_id"`
	// OnHold: the expiry clock has not started (expiry_at is null); it starts at the
	// first handshake. The account is still status "active". HoldDurationDays is the
	// validity it will get; HoldDeadlineAt (optional) activates it anyway.
	OnHold           bool    `json:"on_hold"`
	HoldDurationDays *int    `json:"hold_duration_days"`
	HoldDeadlineAt   *string `json:"hold_deadline_at"`
	ActivatedAt      *string `json:"activated_at"`
	// ResetStrategy (none|day|week|month|year) zeroes the used traffic on schedule;
	// NextResetAt is the next run, LastResetAt the last reset of any kind.
	ResetStrategy string  `json:"reset_strategy"`
	NextResetAt   *string `json:"next_reset_at"`
	LastResetAt   *string `json:"last_reset_at"`
	// routing_mode / routing_excluded_cidrs / routing_precision - filled by fillRouting.
	routingResponse
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.RFC3339)
	return &v
}

// subscriptionBaseURL is the origin to prepend to subscription paths when a
// separate subscription domain is configured, e.g. "https://sub.example.com:8443" -
// nil when the feature is off. Pure so it's testable without a store.
func subscriptionBaseURL(settings store.PanelSettings) *string {
	if settings.SubDomain == nil || *settings.SubDomain == "" {
		return nil
	}
	base := "https://" + *settings.SubDomain
	if settings.SubPort != nil && *settings.SubPort != 443 {
		base += ":" + strconv.Itoa(*settings.SubPort)
	}
	return &base
}

// subscriptionBaseURLFromStore fetches settings and derives the subscription
// origin. Best-effort: a settings read failure degrades to "no separate origin"
// (clients fall back to SubscriptionPath) rather than failing account responses.
func (s *Server) subscriptionBaseURLFromStore(ctx context.Context) *string {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		s.Logger.Warn("get_settings_for_subscription_url_failed", "error", err)
		return nil
	}
	return subscriptionBaseURL(settings)
}

func toAccountResponse(a store.Account, peers []store.AccountPeerWithNode, subBase *string) accountResponse {
	var expiryAt *string
	if a.ExpiryAt != nil {
		s := a.ExpiryAt.Format(time.RFC3339)
		expiryAt = &s
	}
	var accountLastHandshake *string
	if a.LastHandshakeAt != nil {
		s := a.LastHandshakeAt.Format(time.RFC3339)
		accountLastHandshake = &s
	}

	peerResponses := make([]accountPeerResponse, 0, len(peers))
	for _, p := range peers {
		var lastHandshake *string
		online := false
		if p.LastHandshakeAt != nil {
			s := p.LastHandshakeAt.Format(time.RFC3339)
			lastHandshake = &s
			online = time.Since(*p.LastHandshakeAt) <= peerOnlineWindow
		}
		peerResponses = append(peerResponses, accountPeerResponse{
			NodeID:          p.NodeID,
			NodeName:        p.NodeName,
			AssignedIP:      p.AssignedIP,
			Online:          online,
			LastHandshakeAt: lastHandshake,
		})
	}

	var deprecatedNodeID, deprecatedAssignedIP *string
	if len(peers) > 0 {
		deprecatedNodeID = &peers[0].NodeID
		deprecatedAssignedIP = &peers[0].AssignedIP
	}

	subscriptionPath := "/api/v1/sub/" + a.SubscriptionToken
	var subscriptionURL *string
	if subBase != nil {
		u := *subBase + subscriptionPath
		subscriptionURL = &u
	}

	var resellerID *string
	if a.OwnerKeyNamespace != nil {
		if id, ok := strings.CutPrefix(*a.OwnerKeyNamespace, store.ResellerNamespacePrefix); ok {
			resellerID = &id
		}
	}

	return accountResponse{
		Owner:                  a.OwnerKeyNamespace,
		ResellerID:             resellerID,
		ID:                     a.ID,
		ExternalRef:            a.ExternalRef,
		Label:                  a.Label,
		PublicKey:              a.PublicKey,
		Peers:                  peerResponses,
		DataQuotaBytes:         a.DataQuotaBytes,
		DataUsedBytes:          a.DataUsedBytes,
		ExpiryAt:               expiryAt,
		DeviceLimit:            a.DeviceLimit,
		DeviceLimitExceeded:    a.DeviceLimitExceededAt != nil,
		DeviceLimitHardEnforce: a.DeviceLimitHardEnforce,
		BandwidthLimitMbps:     a.BandwidthLimitMbps,
		SubscriptionPath:       subscriptionPath,
		SubscriptionURL:        subscriptionURL,
		Status:                 a.Status,
		SuspendReason:          a.SuspendReason,
		CreatedAt:              a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:              a.UpdatedAt.Format(time.RFC3339),
		LastHandshakeAt:        accountLastHandshake,
		NodeID:                 deprecatedNodeID,
		AssignedIP:             deprecatedAssignedIP,
		OnHold:                 a.OnHold(),
		HoldDurationDays:       a.HoldDurationDays,
		HoldDeadlineAt:         rfc3339Ptr(a.HoldDeadlineAt),
		ActivatedAt:            rfc3339Ptr(a.ActivatedAt),
		ResetStrategy:          a.ResetStrategy,
		NextResetAt:            rfc3339Ptr(a.NextResetAt),
		LastResetAt:            rfc3339Ptr(a.LastResetAt),
	}
}

const bytesPerGB = 1_000_000_000

func gbToBytes(gb float64) int64 { return int64(gb * bytesPerGB) }

// auditActor names who to blame for an audit_log row, whether the caller is an
// admin or a bot/reseller API key.
func auditActor(identity *CallerIdentity) string {
	if identity.IsAdmin {
		return identity.AdminUsername // for a reseller this is its own username
	}
	if identity.ResellerID != "" {
		// Names both the key and its owning reseller, e.g. api_key:<key_id>@reseller:<id>.
		return "api_key:" + identity.KeyID + "@" + identity.KeyNamespace
	}
	return "api_key:" + identity.KeyNamespace
}

// respondWithAccount fetches the account's current peers and writes the full
// response - every handler below that returns an account goes through this so the
// Peers field is never accidentally left empty by a forgotten fetch.
func (s *Server) respondWithAccount(w http.ResponseWriter, r *http.Request, status int, account store.Account, callerNamespace *string) {
	peers, err := s.Store.ListAccountPeersWithNode(r.Context(), account.ID, callerNamespace)
	if err != nil {
		s.Logger.Error("list_account_peers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account peers")
		return
	}
	resp := toAccountResponse(account, peers, s.subscriptionBaseURLFromStore(r.Context()))
	s.fillRouting(r.Context(), []*accountResponse{&resp})
	writeJSON(w, status, resp)
}

// handleCreateAccount serves both admin and API-key callers (requireAdminOrAPIKey).
// Generates a real keypair, encrypts the private key, and gives the account a peer
// on every eligible node (or one specific node, if NodeID pins it) - see
// store.CreateAccount for the fan-out/transactional details.
func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Label == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "label is required")
		return
	}

	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)

	tmpl, aerr := s.loadTemplate(ctx, req.TemplateID, callerNamespaceArg(identity))
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	spec, aerr := resolveAccountSpec(req, tmpl, time.Now())
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	account, aerr := s.createFromSpec(ctx, identity, spec, req.Label)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}

	s.emitAccountEvent(events.AccountCreated, account)
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.created", account.ID, createAuditDetail(req.TemplateID, account), r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	s.respondWithAccount(w, r, http.StatusCreated, account, callerNamespaceArg(identity))
}

// handleListAccounts serves the account list and, with ?external_ref=, the lookup-by-ref
// a client needs to find an account it created earlier (including one it has since
// deleted - see store.FindAccountsByExternalRef).
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	q := r.URL.Query()

	var accounts []store.Account
	var err error
	var nextCursor *string
	cursorMode := false
	if ref := q.Get("external_ref"); ref != "" {
		// Exact-match lookup: limit, offset and the admin owner filters do not apply.
		accounts, err = s.Store.FindAccountsByExternalRef(ctx, ref, ns)
	} else {
		limit := 50
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		// Unlike limit (a bad value falls back to the default), a bad offset is refused:
		// silently serving page 0 would make a paging loop spin forever on page 0.
		if q.Has("cursor") && q.Has("offset") {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "use either cursor or offset, not both")
			return
		}
		offset := 0
		if q.Has("offset") {
			n, perr := strconv.Atoi(q.Get("offset"))
			if perr != nil || n < 0 {
				writeJSONError(w, http.StatusBadRequest, "invalid_request", "offset must be an integer >= 0")
				return
			}
			offset = n
		}
		// ?owner= / ?reseller_id= narrow the list for staff admins only. A reseller or API
		// key is already pinned to its own namespace, so the filter is ignored for them
		// rather than letting a crafted value probe other namespaces.
		ownerFilter := ""
		if identity.IsAdmin && identity.ResellerID == "" {
			if v := q.Get("owner"); v != "" {
				ownerFilter = v
			}
			if v := q.Get("reseller_id"); v != "" {
				ownerFilter = store.ResellerNamespace(v)
			}
		}
		if q.Has("cursor") {
			// Keyset paging (?cursor= starts; then pass the previous next_cursor).
			// One extra row is fetched to know whether another page exists.
			afterAt, afterID, cerr := decodeAccountCursor(q.Get("cursor"))
			if cerr != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid_cursor", "cursor is not valid - restart paging with an empty cursor")
				return
			}
			accounts, err = s.Store.ListAccountsAfter(ctx, limit+1, afterAt, afterID, ns, ownerFilter)
			if err == nil && len(accounts) > limit {
				accounts = accounts[:limit]
				last := accounts[len(accounts)-1]
				c := encodeAccountCursor(last.CreatedAt, last.ID)
				nextCursor = &c
			}
			cursorMode = true
		} else {
			accounts, err = s.Store.ListAccounts(ctx, limit, offset, ns, ownerFilter)
		}
	}
	if err != nil {
		s.Logger.Error("list_accounts_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list accounts")
		return
	}

	subBase := s.subscriptionBaseURLFromStore(ctx)
	out := make([]accountResponse, 0, len(accounts))
	for _, a := range accounts {
		peers, err := s.Store.ListAccountPeersWithNode(ctx, a.ID, ns)
		if err != nil {
			s.Logger.Error("list_account_peers_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account peers")
			return
		}
		out = append(out, toAccountResponse(a, peers, subBase))
	}
	routingPtrs := make([]*accountResponse, len(out))
	for i := range out {
		routingPtrs[i] = &out[i]
	}
	s.fillRouting(ctx, routingPtrs)
	if cursorMode {
		// next_cursor is null on the last page. Only present in cursor mode, so the
		// limit/offset response shape is unchanged.
		writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "next_cursor": nextCursor})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// encodeAccountCursor/decodeAccountCursor: an opaque keyset position
// (created_at microseconds, id), the exact sort key of ListAccounts.
func encodeAccountCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt.UnixMicro(), 10) + "|" + id))
}

func decodeAccountCursor(c string) (*time.Time, string, error) {
	if c == "" {
		return nil, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, "", err
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, "", errors.New("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, "", err
	}
	if !uuidPattern.MatchString(id) {
		return nil, "", errors.New("malformed cursor id")
	}
	t := time.UnixMicro(n).UTC()
	return &t, id, nil
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	ns := callerNamespaceArg(identity)
	account, err := s.Store.GetAccount(r.Context(), r.PathValue("id"), ns)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account")
		return
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

type updateAccountRequest struct {
	Label       *string  `json:"label"`
	DataQuotaGB *float64 `json:"data_quota_gb"`
	ExpiryAt    *string  `json:"expiry_at"`
	DeviceLimit *int     `json:"device_limit"`
	// BandwidthLimitMbps: omitted = unchanged, 0 = remove the limit (unshaped).
	BandwidthLimitMbps     *int  `json:"bandwidth_limit_mbps"`
	DeviceLimitHardEnforce *bool `json:"device_limit_hard_enforce"`
	// ClearDataQuota / ClearDeviceLimit: true sets the field to null (unlimited), which
	// omitting data_quota_gb / device_limit cannot do (omitted = unchanged). They are
	// refused alongside a value for the same field; false/absent = no effect.
	ClearDataQuota   *bool `json:"clear_data_quota"`
	ClearDeviceLimit *bool `json:"clear_device_limit"`
	// ResetStrategy: none|day|week|month|year (restarts the schedule from now).
	// HoldDurationDays edits the duration of an account that is still on hold; setting
	// expiry_at on an on-hold account activates it with that fixed date instead.
	ResetStrategy    *string `json:"reset_strategy"`
	HoldDurationDays *int    `json:"hold_duration_days"`
	// Split-tunnel routing (routing_api.go). Omitted = unchanged; routing_mode "" = back to default.
	routingRequest
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	var req updateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}

	var expiryAt *time.Time
	if req.ExpiryAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiryAt)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "expiry_at must be RFC3339")
			return
		}
		expiryAt = &t
	}
	var quotaBytes *int64
	if req.DataQuotaGB != nil {
		if *req.DataQuotaGB < 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "data_quota_gb must not be negative")
			return
		}
		b := gbToBytes(*req.DataQuotaGB)
		quotaBytes = &b
	}
	if req.BandwidthLimitMbps != nil && (*req.BandwidthLimitMbps < 0 || *req.BandwidthLimitMbps > maxBandwidthLimitMbps) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "bandwidth_limit_mbps must be between 1 and 100000, or 0 to remove the limit")
		return
	}

	if req.ResetStrategy != nil && !store.ValidResetStrategy(*req.ResetStrategy) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "reset_strategy must be none, day, week, month or year")
		return
	}
	if req.HoldDurationDays != nil && (*req.HoldDurationDays < 1 || *req.HoldDurationDays > maxHoldDays) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "hold_duration_days must be between 1 and 3650")
		return
	}
	clearQuota := req.ClearDataQuota != nil && *req.ClearDataQuota
	clearDevices := req.ClearDeviceLimit != nil && *req.ClearDeviceLimit
	if clearQuota && req.DataQuotaGB != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "clear_data_quota cannot be combined with data_quota_gb")
		return
	}
	if clearDevices && req.DeviceLimit != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "clear_device_limit cannot be combined with device_limit")
		return
	}

	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	routingUpdate, aerr := s.prepareRoutingUpdate(ctx, r.PathValue("id"), ns, req.routingRequest)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	account, err := s.Store.UpdateAccount(ctx, r.PathValue("id"), ns, store.UpdateAccountParams{
		Label:                  req.Label,
		DataQuotaBytes:         quotaBytes,
		ExpiryAt:               expiryAt,
		DeviceLimit:            req.DeviceLimit,
		BandwidthLimitMbps:     req.BandwidthLimitMbps,
		DeviceLimitHardEnforce: req.DeviceLimitHardEnforce,
		ClearDataQuota:         clearQuota,
		ClearDeviceLimit:       clearDevices,
		ResetStrategy:          req.ResetStrategy,
		HoldDurationDays:       req.HoldDurationDays,
	})
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if writeResellerError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("update_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update account")
		return
	}

	var auditDetail any
	if routingUpdate != nil {
		if err := s.Store.UpdateAccountRouting(ctx, account.ID, ns, *routingUpdate); err != nil {
			s.Logger.Error("update_account_routing_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update account routing")
			return
		}
		auditDetail = map[string]any{"routing_mode": req.RoutingMode, "routing_excluded_cidrs": req.RoutingExcludedCIDRs, "routing_precision": req.RoutingPrecision}
	}

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.updated", account.ID, auditDetail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

type suspendAccountRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleSuspendAccount(w http.ResponseWriter, r *http.Request) {
	var req suspendAccountRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body is fine, defaults below
	reason := req.Reason
	if reason != "manual" && reason != "abuse_flag" {
		reason = "manual"
	}

	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	account, err := s.Store.SuspendAccount(ctx, r.PathValue("id"), ns, reason)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("suspend_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not suspend account")
		return
	}

	detail := map[string]string{"reason": reason}
	s.emitAccountEvent(events.AccountSuspended, account)
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.suspended", account.ID, detail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

func (s *Server) handleEnableAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	account, err := s.Store.EnableAccount(ctx, r.PathValue("id"), ns)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if errors.Is(err, store.ErrAccountQuotaExhausted) {
		// Enabling would be undone by the next traffic sample; say so instead of
		// answering 200 for an account that is about to be suspended again.
		writeJSONError(w, http.StatusConflict, "quota_exhausted",
			"this account has used its whole data quota and would be suspended again at once: raise the quota, renew with add_quota_gb or reset its usage first")
		return
	}
	if writeResellerError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("enable_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not enable account")
		return
	}

	s.emitAccountEvent(events.AccountEnabled, account)
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.enabled", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

type renewAccountRequest struct {
	AddQuotaGB *float64 `json:"add_quota_gb"`
	ExtendDays *int     `json:"extend_days"` // added to current expiry_at (or now, if it had none)
}

func (s *Server) handleRenewAccount(w http.ResponseWriter, r *http.Request) {
	var req renewAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	account, err := s.renewAccount(ctx, id, ns, req.AddQuotaGB, req.ExtendDays)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	var ae *apiError
	if errors.As(err, &ae) {
		writeAPIError(w, ae)
		return
	}
	if writeResellerError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("renew_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not renew account")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.renewed", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

// handleResetAccountUsage zeroes the account's data_used_bytes (see
// store.ResetAccountUsage for the exact suspension rules). It takes no input: an empty
// body, {} or anything else is ignored.
func (s *Server) handleResetAccountUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	account, err := s.Store.ResetAccountUsage(ctx, r.PathValue("id"), ns)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if writeResellerError(w, err) {
		return
	}
	if err != nil {
		s.Logger.Error("reset_account_usage_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reset account usage")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.usage_reset", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	account, err := s.Store.SoftDeleteAccount(ctx, r.PathValue("id"), ns)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("delete_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete account")
		return
	}

	s.emitAccountEvent(events.AccountDeleted, account)
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.deleted", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}

// handleGetAccountConfig renders a real wg-quick client config for one of the
// account's peers (an account can now have a peer on several nodes - see
// docs/STORY-09-multi-node-accounts.md). ?node_id= picks which one; if omitted, it
// defaults to the only peer when there's exactly one, otherwise 400s with the list
// of valid node ids rather than silently guessing.
func (s *Server) handleGetAccountConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	if _, err := s.Store.GetAccount(ctx, id, ns); errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account")
		return
	}

	peers, err := s.Store.ListAccountPeersWithNode(ctx, id, ns)
	if err != nil {
		s.Logger.Error("list_account_peers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account peers")
		return
	}
	if len(peers) == 0 {
		writeJSONError(w, http.StatusConflict, "no_peers", "this account has no node peers yet")
		return
	}

	var target store.AccountPeerWithNode
	nodeIDParam := r.URL.Query().Get("node_id")
	switch {
	case nodeIDParam == "" && len(peers) == 1:
		target = peers[0]
	case nodeIDParam == "":
		ids := make([]string, 0, len(peers))
		for _, p := range peers {
			ids = append(ids, p.NodeID)
		}
		writeJSONError(w, http.StatusBadRequest, "node_id_required",
			"this account has peers on multiple nodes - specify ?node_id= (one of: "+strings.Join(ids, ", ")+")")
		return
	default:
		found := false
		for _, p := range peers {
			if p.NodeID == nodeIDParam {
				target = p
				found = true
				break
			}
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "node_not_found", "this account has no peer on that node")
			return
		}
	}

	// ?endpoint_id= (staff only): a specific node endpoint's uuid, or "direct" for the
	// node's public_endpoint. Without it the best healthy endpoint is chosen automatically.
	endpointID := r.URL.Query().Get("endpoint_id")
	if endpointID != "" && (identity.ResellerID != "" || !identity.IsAdmin) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "endpoint_id is available to panel staff only")
		return
	}

	// Rendering (including the hard-won DNS and MTU choices) lives in
	// renderPeerConfig, shared with the subscription endpoint so the two config
	// surfaces can never drift apart.
	config, err := s.renderPeerConfig(ctx, id, ns, target, endpointID)
	if errors.Is(err, errEndpointNotFound) {
		writeJSONError(w, http.StatusNotFound, "endpoint_not_found", "that endpoint does not exist on this node")
		return
	}
	if errors.Is(err, errNodeMissingPublicKey) {
		writeJSONError(w, http.StatusConflict, "node_missing_public_key", "the node this account is on has no public key set yet")
		return
	}
	if err != nil {
		s.Logger.Error("render_account_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not render config")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(config))
}
