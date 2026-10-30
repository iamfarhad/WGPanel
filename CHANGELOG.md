# Changelog

All notable changes to Jirjirak WireGuard (WGPanel) are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Install and update
- `install.sh` installs the newest release by default, or the one given with `--version vX.Y.Z`
  (or `WGPANEL_VERSION`). It pins `API_IMAGE`/`FRONTEND_IMAGE`/`NODE_IMAGE` to that tag instead of
  `:latest` and takes `docker-compose.yml`, `Caddyfile` and `.env.example` from the same tag.
- `wgpanel update [--version vX.Y.Z] [--force]` moves to the newest (or given) release: backup, that
  release's images and deploy files, the self-node too, automatic rollback on a failed health check.
  It is a no-op when already on that release, and asks before a downgrade.
- `wgpanel rollback` restores the exact previous release (image tags and deploy files) instead of
  restarting whatever images happen to be cached.
- `wgpanel version` shows the installed release and the newest one.
- `install-node.sh --version vX.Y.Z` installs a node on a given release, and
  `install-node.sh --update [--version vX.Y.Z]` moves an installed node without a new join token.
- `deploy/set-version.sh` writes the release version into `frontend/package.json` and both READMEs'
  install instructions; the READMEs show the current release and a release badge.

## [0.1.0] - 2026-10-30

First tagged release.

### Panel and UI
- Jirjirak branding: Vazirmatn font, mascot illustrations, light and dark themes.
- Full Persian translation with RTL layout and Jalali dates, plus an English/Persian toggle.
- Dashboard with fleet status, "expiring soon" counts and a plan card for resellers.
- Three-dot row menus.
- Reports page (24 hours to 1 year):
  - traffic, active and new accounts, quota usage, expiry buckets and top accounts;
  - per-node and per-reseller breakdowns;
  - a table view for every chart, and CSV export.
- API reference served inside the panel at `/api-docs.html`.

### Accounts
- Multi-node accounts: one account gets a peer on every eligible node.
- Per-account limits:
  - data quota;
  - expiry;
  - device limit, with optional auto-suspend;
  - bandwidth limit, enforced with tc on every node.
- Data caps are enforced: an account is suspended when its usage reaches its quota, and `enable` refuses an account that is still over its cap.
- Start on first use ("on hold"): the expiry clock starts at the first handshake. An optional deadline activates the account anyway.
- Scheduled usage reset: daily, weekly, monthly or yearly.
- Plan templates, global or reseller-owned. Accounts can be created from a template.
- Bulk operations on accounts: create, renew, suspend, enable, delete and reset usage. A bulk create can be exported to CSV.
- Subscription URL with load, health and region-aware node steering.
- Public mobile subscription page at `/s/{token}`. It shows usage, expiry, a QR code, config download and app install steps, in Persian and English.

### Resellers
- Reseller role with a plan: maximum accounts, traffic credit, allowed node groups and a plan expiry date.
- Resellers sign in to the same panel and see only their own accounts and reports.
- Reseller-owned API keys, scoped to the reseller's namespace.

### Nodes and networking
- Node-local DNS forwarder on the WireGuard gateway, used by client configs by default.
- The node agent checks its network and DNS at startup.
- Multiple entry endpoints per node:
  - kind: direct or relay;
  - priority;
  - TCP health checks;
  - a per-endpoint MTU.

  Configs use the best healthy endpoint.
- Configurable client MTU (default 1280).
- MSS clamping in the node container.
- Host network tuning in the installers and in `wgpanel tune`: BBR + fq, larger socket buffers, conntrack sizing, TCP MTU probing and UDP GRO forwarding.

### Security
- Progressive login lockout per username and per IP.
- TOTP two-factor sign-in with recovery codes.
- Passkeys (WebAuthn), including passwordless sign-in.
- Admin IP allowlist, with `wgpanel clear-ip-allowlist` as a break-glass command.
- Roles: super admin, operator, support and reseller.
- Audit log.

### Notifications, webhooks and backups
- Domain events:
  - node down and recovered;
  - account created, deleted, suspended and enabled;
  - quota at 80% and quota exhausted;
  - expiring soon and expired;
  - reseller credit low and plan expiring;
  - backup succeeded and failed.
- Notifications through Telegram, email and an in-panel notification center.
- Signed webhooks (`X-Jirjirak-Signature`):
  - retries with backoff;
  - a delivery log and redeliver;
  - an SSRF guard.
- API keys with the `webhooks` permission manage their own webhooks, and account events carry `external_ref`.
- Scheduled, encrypted off-server backups to Telegram and S3-compatible storage, with retention.
- Notification channels and webhooks are included in panel backups.

### Bot API
- HMAC-signed API keys with per-key permissions and node groups.
- Find an account by `external_ref`.
- Offset and cursor pagination.
- `reset-usage`, clearing limits, and `last_handshake_at` on accounts.
- Rate limit per API key (default 20 requests/s, burst 40), returning `Retry-After` and `X-RateLimit-*` headers.

### Deployment
- Production Docker Compose stack: db, redis, api, frontend and caddy, with memory limits and healthchecks.
- Installers for the panel (with an optional self-node) and for remote nodes.
- `wgpanel` CLI: status, logs, backup, update with rollback, doctor and tune.
- Images on GHCR: `wgpanel-api`, `wgpanel-frontend` and `wgpanel-node`.
- Builds use Go 1.26.

### Fixed
- Clients that connected but had no internet. The causes were an interface/subnet mismatch and public DNS being unreachable on some networks.
- Byte counts now use decimal units everywhere, matching API quotas.
- Used/limit pairs rendered reversed in RTL.
- Invalid YAML in the OpenAPI spec. CI now checks that it parses.

### Upgrade notes
- Migrations run automatically when the API starts. Take a backup first (`wgpanel backup`).
- Passkeys need the panel opened over HTTPS on its configured domain.
- API clients that page through accounts should handle HTTP 429.

[Unreleased]: https://github.com/iamfarhad/WGPanel/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/iamfarhad/WGPanel/releases/tag/v0.1.0
