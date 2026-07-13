// Package httpapi wires together the public and /internal HTTP surfaces for the
// control-plane API's walking-skeleton story (docs/STORY-01-control-plane-walking-skeleton.md).
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"wgpanel-api/internal/caddyadmin"
	"wgpanel-api/internal/nodeca"
	"wgpanel-api/internal/store"
)

type Server struct {
	Store                   *store.Store
	Redis                   *redis.Client
	CA                      *nodeca.CA
	JWTSecret               string
	InternalAPIToken        string
	NodeJoinTokenTTLMinutes string
	AccountKeyEncryptionKey string
	APIHMACMasterKey        string
	Logger                  *slog.Logger
	// CaddyAdmin/AdminACLEmail (docs/STORY-10-monitoring-and-domain-management.md)
	// push a live domain change to Caddy when set via PATCH /api/v1/settings. Nil
	// CaddyAdmin means this deployment has no Caddy admin socket wired up - domain
	// changes still persist, just without the live push (see handleUpdateSettings).
	CaddyAdmin    *caddyadmin.Client
	AdminACLEmail string
}

// Routes builds the full handler tree: public routes (proxied by Caddy), the
// /internal/* group (gated by internalOnly - see middleware.go), and the admin-only
// node/account-management routes (gated by requireAdmin) - wrapped in a logging
// middleware that never logs request/response bodies (see loggingMiddleware).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/v1/nodes/join", s.handleRedeemJoinToken)

	mux.Handle("GET /internal/healthz", s.internalOnly(http.HandlerFunc(s.handleInternalHealthz)))
	mux.Handle("POST /internal/admins", s.internalOnly(http.HandlerFunc(s.handleCreateAdmin)))
	// install.sh's core-server self-registration (docs/STORY-09-multi-node-accounts.md) -
	// same X-Internal-Token gate as /internal/admins, since it's the same "trusted
	// install-time bootstrap" trust tier.
	mux.Handle("POST /internal/nodes/bootstrap-self", s.internalOnly(http.HandlerFunc(s.handleBootstrapSelfNode)))

	// Node create is operator-or-above; join-token generation is super-admin-only - it
	// mints new trust into the system, same tier as issuing an API key
	// (docs/PRD-security-access-control.md §7).
	mux.Handle("POST /api/v1/nodes", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleCreateNode))))
	mux.Handle("PATCH /api/v1/nodes/{id}", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleUpdateNode))))
	mux.Handle("POST /api/v1/nodes/{id}/join-token", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGenerateJoinToken))))
	// Read-only node visibility is shared with bot/reseller API keys (scoped to their
	// node_groups) - docs/PRD-telegram-bot-api.md §7. Mutating node routes stay admin-only.
	mux.Handle("GET /api/v1/nodes", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListNodes))))
	mux.Handle("GET /api/v1/nodes/{id}", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetNode))))
	// docs/STORY-10-monitoring-and-domain-management.md - node CPU/RAM history chart.
	mux.Handle("GET /api/v1/nodes/{id}/metrics", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetNodeMetrics))))

	// Account endpoints serve both the admin panel (JWT) and bot/reseller clients
	// (HMAC-signed API key) through the same handlers - docs/PRD-account-management.md
	// §1 ("one unambiguous account lifecycle... whether triggered from the admin UI or
	// the bot API"). requirePermission enforces both API-key permission sets and the
	// admin support-role's read-only restriction (docs/PRD-security-access-control.md §4).
	mux.Handle("POST /api/v1/accounts", s.requireAdminOrAPIKey(s.requirePermission("create", http.HandlerFunc(s.handleCreateAccount))))
	mux.Handle("GET /api/v1/accounts", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListAccounts))))
	mux.Handle("GET /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccount))))
	mux.Handle("PATCH /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleUpdateAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/suspend", s.requireAdminOrAPIKey(s.requirePermission("suspend", http.HandlerFunc(s.handleSuspendAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/enable", s.requireAdminOrAPIKey(s.requirePermission("suspend", http.HandlerFunc(s.handleEnableAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/renew", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleRenewAccount))))
	mux.Handle("DELETE /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("delete", http.HandlerFunc(s.handleDeleteAccount))))
	mux.Handle("GET /api/v1/accounts/{id}/config", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccountConfig))))
	// docs/STORY-10-monitoring-and-domain-management.md - account usage-over-time chart.
	mux.Handle("GET /api/v1/accounts/{id}/usage", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccountUsage))))

	// API key issuance is super-admin-only, same reasoning as join-token generation above.
	mux.Handle("POST /api/v1/api-keys", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleCreateAPIKey))))
	mux.Handle("GET /api/v1/api-keys", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAPIKeys))))
	mux.Handle("POST /api/v1/api-keys/{id}/revoke", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRevokeAPIKey))))
	mux.Handle("POST /api/v1/api-keys/{id}/rotate", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRotateAPIKey))))

	// Admin user management (PRD-admin-panel-ux.md §3.6) - super-admin-only, same tier
	// as API keys and join tokens: all three mint new trust into the system.
	mux.Handle("POST /api/v1/admins", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleCreateAdminUser))))
	mux.Handle("GET /api/v1/admins", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAdminUsers))))
	mux.Handle("PATCH /api/v1/admins/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateAdminUser))))
	mux.Handle("DELETE /api/v1/admins/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleDeleteAdminUser))))

	// Audit Log (PRD-admin-panel-ux.md §3.7) - a global view, super-admin-only.
	mux.Handle("GET /api/v1/audit-log", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAuditLog))))

	// Panel-wide Settings (docs/STORY-08-settings-and-bootstrap-admin.md) - readable by
	// any admin role (it's just configuration display), editable only by super_admin
	// since these are panel-wide defaults, same trust tier as API keys/join tokens.
	mux.Handle("GET /api/v1/settings", s.requireAdmin(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PATCH /api/v1/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateSettings))))

	return s.loggingMiddleware(mux)
}
