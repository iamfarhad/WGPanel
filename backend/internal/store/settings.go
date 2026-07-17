package store

import "context"

// PanelSettings is the single configuration row a super_admin can edit from the admin
// panel's Settings screen (docs/PRD-admin-panel-ux.md's "some needed config" ask) -
// see migrations/0009_create_panel_settings.sql for what each field actually affects.
type PanelSettings struct {
	PublicBaseURL       *string
	DefaultDataQuotaGB  *float64
	DefaultDeviceLimit  *int
	DefaultNodeCapacity int
	SupportContact      *string
	// ClientDNS is the DNS = line written into every generated wg-quick config
	// (migration 0018). Comma-separated; defaults to Cloudflare, overridable when the
	// node's egress can't reach that (see the migration's comment).
	ClientDNS string
	// PanelDomain (migration 0012) is the live-managed TLS domain, distinct from
	// PublicBaseURL which stays purely informational (see migration 0009's comment).
	// Changing this triggers a live push to Caddy's admin API (docs/STORY-10-
	// monitoring-and-domain-management.md) in addition to being persisted here - nil
	// means "not yet set via the panel", i.e. Caddy is still running whatever
	// PANEL_DOMAIN its container was started with.
	PanelDomain *string
}

// GetSettings always returns the singleton row - it's seeded by migration 0009, so this
// never has a not-found case in practice.
func (s *Store) GetSettings(ctx context.Context) (PanelSettings, error) {
	var p PanelSettings
	err := s.pool.QueryRow(ctx, `
		SELECT public_base_url, default_data_quota_gb, default_device_limit, default_node_capacity, support_contact, panel_domain, client_dns
		FROM panel_settings WHERE id = 1
	`).Scan(&p.PublicBaseURL, &p.DefaultDataQuotaGB, &p.DefaultDeviceLimit, &p.DefaultNodeCapacity, &p.SupportContact, &p.PanelDomain, &p.ClientDNS)
	return p, err
}

// UpdateSettingsParams: a nil field leaves that column unchanged, matching the same
// "omitted = unchanged" PATCH convention PATCH /api/v1/accounts/{id} already uses -
// like that endpoint, this means a nullable field can be set but not explicitly
// cleared back to null through this API (an accepted existing simplification, not
// new to this endpoint).
type UpdateSettingsParams struct {
	PublicBaseURL       *string
	DefaultDataQuotaGB  *float64
	DefaultDeviceLimit  *int
	DefaultNodeCapacity *int
	SupportContact      *string
	PanelDomain         *string
	ClientDNS           *string
}

func (s *Store) UpdateSettings(ctx context.Context, p UpdateSettingsParams) (PanelSettings, error) {
	_, err := s.pool.Exec(ctx, `
		UPDATE panel_settings SET
			public_base_url = COALESCE($1, public_base_url),
			default_data_quota_gb = COALESCE($2, default_data_quota_gb),
			default_device_limit = COALESCE($3, default_device_limit),
			default_node_capacity = COALESCE($4, default_node_capacity),
			support_contact = COALESCE($5, support_contact),
			panel_domain = COALESCE($6, panel_domain),
			client_dns = COALESCE($7, client_dns),
			updated_at = now()
		WHERE id = 1
	`, p.PublicBaseURL, p.DefaultDataQuotaGB, p.DefaultDeviceLimit, p.DefaultNodeCapacity, p.SupportContact, p.PanelDomain, p.ClientDNS)
	if err != nil {
		return PanelSettings{}, err
	}
	return s.GetSettings(ctx)
}
