package httpapi

import (
	"math"
	"net/http"
	"net/url"
	"time"

	"wgpanel-api/internal/steering"
	"wgpanel-api/internal/store"
)

// subscriptionInfoResponse is everything the public customer page may show about one
// account. It is built only from the account the token resolves to and deliberately
// omits ids, the token itself, peer IPs, device endpoints and the owner namespace.
type subscriptionInfoResponse struct {
	Label string `json:"label"`
	// Status is "active" or "suspended"; StatusReason narrows a suspension to
	// "expired" or "quota_exceeded" when that is why (admin-typed reasons are never
	// exposed - they map to null).
	Status         string  `json:"status"`
	StatusReason   *string `json:"status_reason"`
	DataQuotaBytes *int64  `json:"data_quota_bytes"` // null = unlimited
	DataUsedBytes  int64   `json:"data_used_bytes"`
	ExpiryAt       *string `json:"expiry_at"`
	DaysLeft       *int    `json:"days_left"` // whole days, rounded up; negative once expired
	DeviceLimit    *int    `json:"device_limit"`
	DevicesActive  int     `json:"devices_active"`
	LastHandshake  *string `json:"last_handshake_at"`
	SupportContact *string `json:"support_contact"`
	// Brand is the owning reseller's branding (nearest ancestor with any, else the global
	// default); null when nothing is configured. See brandForAccount.
	Brand *publicBrand `json:"brand"`
	// ConfigPath downloads the config; Nodes (active accounts only) lists per-node
	// choices, best first.
	ConfigPath string                     `json:"config_path"`
	Nodes      []subscriptionNodeResponse `json:"nodes"`
	// RoutingMode is how this account's traffic is routed: full (everything through
	// the tunnel), bypass_iran (Iranian and local ranges connect directly) or custom
	// (an admin-defined exclusion list - the list itself is not exposed).
	RoutingMode string `json:"routing_mode"`
}

func daysLeft(expiry, now time.Time) int {
	return int(math.Ceil(expiry.Sub(now).Hours() / 24))
}

// handleSubscriptionInfo serves GET /api/v1/sub/{token}/info for the public page.
// Unlike the machine endpoints it answers for suspended accounts too (so the page can
// explain why the link stopped working), but never for deleted/unknown tokens.
func (s *Server) handleSubscriptionInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}

	resp := subscriptionInfoResponse{
		Label:          account.Label,
		Status:         "active",
		DataQuotaBytes: account.DataQuotaBytes,
		DataUsedBytes:  account.DataUsedBytes,
		DeviceLimit:    account.DeviceLimit,
		ConfigPath:     "/api/v1/sub/" + url.PathEscape(account.SubscriptionToken) + "?format=conf",
		Nodes:          []subscriptionNodeResponse{},
	}
	if account.Status != "active" {
		resp.Status = "suspended"
		if account.SuspendReason != nil && (*account.SuspendReason == "expired" || *account.SuspendReason == "quota_exceeded") {
			reason := *account.SuspendReason
			resp.StatusReason = &reason
		}
	}
	now := time.Now()
	if account.ExpiryAt != nil {
		v := account.ExpiryAt.Format(time.RFC3339)
		resp.ExpiryAt = &v
		d := daysLeft(*account.ExpiryAt, now)
		resp.DaysLeft = &d
	}
	if account.LastHandshakeAt != nil {
		v := account.LastHandshakeAt.Format(time.RFC3339)
		resp.LastHandshake = &v
	}

	if devices, err := s.Store.ListAccountDevices(ctx, account.ID, nil); err != nil {
		s.Logger.Warn("subscription_info_devices_failed", "error", err)
	} else {
		cutoff := now.Add(-store.DeviceActiveWindow())
		for _, d := range devices {
			if d.LastSeenAt.After(cutoff) {
				resp.DevicesActive++
			}
		}
	}
	if settings, err := s.Store.GetSettings(ctx); err == nil {
		resp.SupportContact = settings.SupportContact
	}
	// A branded page uses its brand's support link; the panel-wide support_contact stays
	// as the fallback for unbranded pages (and for clients older than `brand`).
	var brand store.BrandProfile
	var brandSource string
	resp.Brand, brand, brandSource = s.brandForAccount(r, account)
	switch {
	case brand.SupportLink != "":
		link := brand.SupportLink
		resp.SupportContact = &link
	case resp.Brand != nil && brandSource == store.BrandSourceReseller:
		// A reseller's page must never fall back to the owner's own support contact.
		resp.SupportContact = nil
	}
	resp.RoutingMode = effectiveRoutingMode(nil)
	if r, err := s.Store.GetAccountRouting(ctx, account.ID); err == nil {
		resp.RoutingMode = effectiveRoutingMode(r.Mode)
	}

	if account.Status == "active" {
		if candidates, err := s.Store.SteerCandidatesForAccount(ctx, account.ID, nil); err != nil {
			s.Logger.Warn("subscription_info_nodes_failed", "error", err)
		} else {
			for _, c := range steering.Rank(candidates, "") {
				resp.Nodes = append(resp.Nodes, subscriptionNodeResponse{
					NodeID: c.NodeID, Name: c.Name, Region: c.Region, Online: c.Online, Recommended: c.Recommended,
					ConfigPath: "/api/v1/sub/" + url.PathEscape(account.SubscriptionToken) + "?format=conf&node_id=" + url.QueryEscape(c.NodeID),
				})
			}
		}
	}

	// The response is per-capability-token private data: never cache it in shared caches.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
