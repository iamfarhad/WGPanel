package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"wgpanel-api/internal/events"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

type createAccountRequest struct {
	ExternalRef        *string  `json:"external_ref"`
	Label              string   `json:"label"`
	NodeID             *string  `json:"node_id"` // omitted/"auto" = every eligible node; a specific UUID pins to just that one
	DataQuotaGB        *float64 `json:"data_quota_gb"`
	ExpiryAt           *string  `json:"expiry_at"` // RFC3339
	DeviceLimit        *int     `json:"device_limit"`
	BandwidthLimitMbps *int     `json:"bandwidth_limit_mbps"` // omitted/null = unshaped
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
	writeJSON(w, status, toAccountResponse(account, peers, s.subscriptionBaseURLFromStore(r.Context())))
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
	if req.BandwidthLimitMbps != nil && (*req.BandwidthLimitMbps < 1 || *req.BandwidthLimitMbps > maxBandwidthLimitMbps) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "bandwidth_limit_mbps must be between 1 and 100000 (omit for unlimited)")
		return
	}

	subscriptionToken, err := newSubscriptionToken()
	if err != nil {
		s.Logger.Error("generate_subscription_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate subscription token")
		return
	}

	kp, err := wgkeys.GenerateKeyPair()
	if err != nil {
		s.Logger.Error("generate_keypair_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate keypair")
		return
	}
	encryptedPriv, err := wgkeys.Encrypt(s.AccountKeyEncryptionKey, kp.PrivateKey)
	if err != nil {
		s.Logger.Error("encrypt_private_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not secure private key")
		return
	}

	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)

	var ownerNamespace *string
	var allowedGroups []string
	if identity.ResellerID != "" {
		// The store re-reads the reseller's node_groups under its row lock, so a
		// session passes none. A reseller-owned API key passes its own (narrower) set,
		// which the store intersects with the reseller's.
		ns := store.ResellerNamespace(identity.ResellerID)
		ownerNamespace = &ns
		if !identity.IsAdmin {
			allowedGroups = identity.NodeGroups
		}
	} else if !identity.IsAdmin {
		ns := identity.KeyNamespace
		ownerNamespace = &ns
		allowedGroups = identity.NodeGroups
	}

	nodeIDOrAuto := ""
	if req.NodeID != nil {
		nodeIDOrAuto = *req.NodeID
	}

	account, err := s.Store.CreateAccount(ctx, store.CreateAccountParams{
		ExternalRef:         req.ExternalRef,
		Label:               req.Label,
		NodeIDOrAuto:        nodeIDOrAuto,
		PublicKey:           kp.PublicKey,
		PrivateKeyEncrypted: encryptedPriv,
		DataQuotaBytes:      quotaBytes,
		ExpiryAt:            expiryAt,
		DeviceLimit:         req.DeviceLimit,
		BandwidthLimitMbps:  req.BandwidthLimitMbps,
		SubscriptionToken:   subscriptionToken,
		OwnerKeyNamespace:   ownerNamespace,
		AllowedNodeGroups:   allowedGroups,
	})
	switch {
	case writeResellerError(w, err):
		return
	case errors.Is(err, store.ErrNodeNotFound):
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	case errors.Is(err, store.ErrNodeNotRegistered):
		writeJSONError(w, http.StatusConflict, "node_not_registered", "node has not completed registration yet")
		return
	case errors.Is(err, store.ErrNodeGroupNotAllowed):
		writeJSONError(w, http.StatusForbidden, "node_group_not_allowed", "this API key is not scoped to that node's group")
		return
	case errors.Is(err, store.ErrNodeCapacityExceeded):
		writeJSONError(w, http.StatusConflict, "node_capacity_exceeded", "node has no remaining capacity")
		return
	case errors.Is(err, store.ErrNoAvailableNode):
		writeJSONError(w, http.StatusConflict, "no_available_node", "no registered node has remaining capacity")
		return
	case errors.Is(err, store.ErrExternalRefTaken):
		writeJSONError(w, http.StatusConflict, "external_ref_taken", "an account with this external_ref already exists")
		return
	case err != nil:
		s.Logger.Error("create_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create account")
		return
	}

	s.emitAccountEvent(events.AccountCreated, account)
	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.created", account.ID, nil, r.RemoteAddr); err != nil {
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
		accounts, err = s.Store.ListAccounts(ctx, limit, offset, ns, ownerFilter)
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
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
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
	account, err := s.Store.UpdateAccount(ctx, r.PathValue("id"), ns, store.UpdateAccountParams{
		Label:                  req.Label,
		DataQuotaBytes:         quotaBytes,
		ExpiryAt:               expiryAt,
		DeviceLimit:            req.DeviceLimit,
		BandwidthLimitMbps:     req.BandwidthLimitMbps,
		DeviceLimitHardEnforce: req.DeviceLimitHardEnforce,
		ClearDataQuota:         clearQuota,
		ClearDeviceLimit:       clearDevices,
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

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.updated", account.ID, nil, r.RemoteAddr); err != nil {
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

	var addQuotaBytes *int64
	if req.AddQuotaGB != nil {
		b := gbToBytes(*req.AddQuotaGB)
		addQuotaBytes = &b
	}

	var newExpiry *time.Time
	if req.ExtendDays != nil {
		existing, err := s.Store.GetAccount(ctx, id, ns)
		if errors.Is(err, store.ErrAccountNotFound) {
			writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
			return
		}
		if err != nil {
			s.Logger.Error("get_account_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not renew account")
			return
		}
		base := time.Now()
		if existing.ExpiryAt != nil && existing.ExpiryAt.After(base) {
			base = *existing.ExpiryAt
		}
		t := base.Add(time.Duration(*req.ExtendDays) * 24 * time.Hour)
		newExpiry = &t
	}

	account, err := s.Store.RenewAccount(ctx, id, ns, addQuotaBytes, newExpiry)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
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

	// Rendering (including the hard-won DNS and MTU=1280 choices) lives in
	// renderPeerConfig, shared with the subscription endpoint so the two config
	// surfaces can never drift apart.
	config, err := s.renderPeerConfig(ctx, id, ns, target)
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
