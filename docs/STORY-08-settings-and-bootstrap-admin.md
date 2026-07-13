# Story 8: Guaranteed Bootstrap Admin, Standalone Demo Access, and a Settings Page

## Story

Three separate but related gaps surfaced from real usage of the panel:

1. **The first super admin wasn't guaranteed to exist.** `install.sh` created it via a follow-up `wgpanel create-admin` CLI call that raced against the API's health check - if that timed out, the installer logged an error and moved on (`|| true`), leaving a fully running stack with zero admins and no obvious next step.
2. **The Docker demo wasn't actually viewable standalone.** `deploy/docker-compose.yml`'s `frontend` container exposes `127.0.0.1:3000`, but `frontend/nginx.conf` had no route for `/api/*` - only Caddy (production-only, needs a real domain for TLS) proxied API calls. Opening `http://127.0.0.1:3000` in a browser without Caddy in front of it rendered the login page but every API call 404'd.
3. **No in-app way to configure panel-wide defaults.** Things like the panel's public URL, and sensible defaults for new accounts/nodes, only lived in `deploy/.env` (requiring a redeploy) or were hardcoded in the frontend (e.g. every new node dialog defaulted to exactly 250 peers, no matter what).

## What changed

### 1. Server-side bootstrap admin (`cmd/api/main.go`'s `bootstrapFirstAdmin`)

After migrations run and before the HTTP servers start, the API checks `AdminCount()`. If it's zero (a genuinely fresh database, gated so this can never re-trigger later), it generates a random 26-character password, hashes it with the same argon2id path every other admin password goes through, creates a `super_admin` row, and prints the credentials **once** to stdout in a clearly delimited, greppable format (`WGPANEL_INITIAL_ADMIN_USERNAME=...` / `WGPANEL_INITIAL_ADMIN_PASSWORD=...`). The username is configurable via `ADMIN_BOOTSTRAP_USERNAME` (new env var, defaults to `admin`; `install.sh` now writes the operator's chosen username here instead of exporting it for a since-removed CLI call).

`install.sh`'s old `create_first_admin` (which called `wgpanel create-admin`) is replaced by `show_first_admin_credentials`, which waits for health then re-displays whatever the API already printed to its own logs - no second admin gets created, no race with the API's own startup sequencing. `wgpanel` gained a `show-bootstrap-admin` command for recovering these credentials later if they scroll past during install.

This is strictly more reliable than the old flow: it cannot silently no-op the way `create_first_admin || true` could, because it runs synchronously inside the API's own startup path, before the API is even considered healthy.

### 2. nginx now proxies `/api/*` (`frontend/nginx.conf`)

Added a `location /api/ { proxy_pass http://api:8080; ... }` block, mirroring `deploy/Caddyfile`'s `handle_path /api/*`. In production this is a no-op (Caddy still terminates TLS and proxies `/api/*` directly to `api:8080`, never routing those requests through the frontend container at all) - but it means the frontend container is now a genuinely complete, self-contained demo reachable at `http://<host>:3000` with no domain, no TLS, no Caddy required.

### 3. A real Settings page (migration 0009, `internal/store/settings.go`, `internal/httpapi/settings.go`, `SettingsPage.tsx`)

A singleton `panel_settings` table (not a generic key/value store - every field has a known type and default) holding: `public_base_url` (informational - the panel's public domain, shown in the UI; does not itself move DNS/TLS), `default_data_quota_gb`, `default_device_limit`, `default_node_capacity`, and `support_contact`. `GET /api/v1/settings` is readable by any admin role; `PATCH /api/v1/settings` is `super_admin`-only, same trust tier as API keys and join tokens. The frontend's "New account" and "New node" dialogs now pre-fill their quota/device-limit/capacity fields from these settings instead of hardcoded blanks/`250`.

## Definition of Done

- [x] A super admin exists after the API's very first successful boot against a fresh database, with no dependency on `install.sh`'s timing - verified by bringing up a fresh Docker stack, confirming the credential banner in `docker compose logs api`, logging in with the generated password, then restarting the API container and confirming the banner does not reappear (`AdminCount() > 0` gate holds).
- [x] `http://<host>:3000` (the frontend container's exposed port, no Caddy, no domain, no TLS) is a fully working demo: login, all pages, all API calls succeed - verified with real `curl` calls and a full Playwright-driven Chrome run of the existing 17-check `e2e-smoke.mjs` suite against it directly.
- [x] `GET`/`PATCH /api/v1/settings` work against a real database and persist across requests - verified with real `curl` calls, including a value round-trip.
- [x] The New Node and New Account dialogs pick up settings-derived defaults, verified via a Playwright script that PATCHes `default_node_capacity` to 300 through the real API and then confirms the New Node dialog's capacity field actually shows `300` on next open (not the frontend's own hardcoded fallback).
- [x] `docs/openapi.yaml` (and its synced copy at `frontend/public/openapi.yaml`, served at `/api-docs.html`) updated with the new `/api/v1/settings` endpoints and `Settings` schema; re-validated with `openapi-spec-validator`.
- [x] `gofmt`/`go vet`/`go build`/`go test` and `tsc -b`/`vite build` all pass.

## Tasks

1. `cmd/api/main.go`: `bootstrapFirstAdmin` + `ADMIN_BOOTSTRAP_USERNAME` config plumbing (`internal/config`, `deploy/.env.example`, `deploy/docker-compose.yml`).
2. `deploy/install.sh`: seed `ADMIN_BOOTSTRAP_USERNAME` from the existing username prompt; replace the racy `create_first_admin` with `show_first_admin_credentials`.
3. `deploy/wgpanel`: `show-bootstrap-admin` command.
4. `frontend/nginx.conf`: `/api/` proxy block.
5. Migration `0009_create_panel_settings.sql`; `internal/store/settings.go`; `internal/httpapi/settings.go`; wired into `server.go` (`GET` any role, `PATCH` `super_admin`).
6. `frontend/src/pages/SettingsPage.tsx`; sidebar nav entry (`super_admin`-only); `AccountsPage`/`NodesPage` create-dialogs re-synced from settings on every dialog open (not just first mount - the dialogs stay mounted between opens, so a plain `useState` initializer would only ever see the pre-fetch fallback value).
7. `docs/openapi.yaml` + `frontend/public/openapi.yaml`: new `/api/v1/settings` paths and `Settings` schema.
8. Verification: full Docker stack, bootstrap-admin banner + idempotency, standalone-container demo access, settings persistence, defaults-propagation via a real browser.
