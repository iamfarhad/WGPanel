# Story 7: Admin Panel — Full Buildout & Polish

## Story

**As** an admin, **I want** every screen `PRD-admin-panel-ux.md` describes — not just Nodes — with a professional look and feel, **so that** the panel is actually usable day-to-day instead of a proof-of-concept shell. Story 6 deliberately stopped at login + Nodes; this story closes the gap it explicitly deferred.

## Scope

- Dashboard: fleet-wide stat cards (node/account counts, data transferred) aggregated client-side from the existing list endpoints — there is no dedicated `/stats` endpoint yet (tracked separately in the PRD audit).
- Accounts: list, create, and a detail view wired to every lifecycle action the backend already exposes (suspend, enable, renew, delete, view wg-quick config).
- API Keys: create (with a one-time-reveal secret, matching how the backend never returns a secret again after creation/rotation), list, rotate, revoke.
- Admin Users: create, list, role selection — gated in the UI to `super_admin` (the backend already enforces this via `requireRole`; the UI just doesn't show links a user can't use).
- Audit Log: list with an expandable detail row (actor, action, target, IP, JSON detail).
- Sidebar navigation replacing Story 6's single top-bar link, with role-aware visibility (support/operator never see API Keys, Admin Users, or Audit Log links, since those are `super_admin`-only server-side).
- A small shared UI kit (`Badge`, `Dialog`, `ConfirmDialog`, `Select`, `Skeleton`/`TableSkeleton`, `StatCard`, `PageHeader`, `EmptyState`) plus a toast notification system, so every new page looks and behaves consistently rather than each reinventing its own patterns.

## A real backend bug found during verification (not a test-setup issue)

While driving the full account-creation flow through a **real** registered node (not a stub), account creation kept failing with `node_not_registered` even though `GET /api/v1/nodes` showed the node as `"status": "online"`. Traced it to `internal/store/accounts.go`: `status` is a single overloaded column written by three different events —

- join-token redemption sets it to `'registered'`
- the agent's first heartbeat (`RecordHeartbeat`) unconditionally overwrites it to `'online'`
- the offline-sweep loop flips it to `'offline'` on staleness

`CreateAccount`'s explicit-node check (and the `auto`-select query) both required `status = 'registered'` exactly — but by the time a node is reporting heartbeats and therefore shows `"online"` in the API, that literal value has already been overwritten and can never be `'registered'` again. Every node that ever completed real end-to-end registration was permanently unable to receive new accounts. This had gone unnoticed in Stories 3-5 because their smoke tests exercised node registration and account creation as separate scenarios against separate nodes, never chaining both against the same one.

Fixed by treating `'online'` as an implicitly-registered superstate in both places `internal/store/accounts.go` checks node eligibility (the `auto`-select query's `WHERE` clause and the explicit-node status check), rather than adding a new column — `'online'` can only be reached after `'registered'` was true, so no information is actually lost by accepting it too. Verified by rebuilding the API image, bringing up a real node agent (`cmd/agent` run as a real OS process against the real mTLS listener) through registration and heartbeats, and confirming an account could then be created against that specific node from the real UI.

## Definition of Done

- [x] Dashboard, Accounts, API Keys, Admin Users, and Audit Log pages all exist and fetch real data from their respective endpoints — no mock/fixture data anywhere.
- [x] Sidebar hides `super_admin`-only nav items for a `support`-role admin, confirmed by logging in as a real `support` user created via the Admin Users page and inspecting the rendered sidebar (only Dashboard/Nodes/Accounts shown).
- [x] Every account lifecycle action in the PRD (create, suspend, enable, renew, delete, view config) works from the UI against the real backend, including the wg-quick config text rendering with a real decrypted private key.
- [x] API key secrets are shown exactly once (on create and on rotate) with an explicit "shown once" warning, matching the backend's actual one-time-reveal behavior.
- [x] `tsc -b && vite build` pass cleanly.
- [x] Full flow verified end-to-end via a real Docker Compose stack (Postgres/Redis/API), a real `cmd/agent` process completing real mTLS registration and heartbeats, and a real Chrome browser driven by Playwright — not unit tests asserting in isolation. All 17 checks in the expanded `e2e-smoke.mjs` passed, including the node-status bug fix above (which the *first* run of this same script caught).
- [x] Test artifacts and secrets from the verification run were torn down (`docker compose down -v`, generated `.env` removed) rather than left behind.

## Tasks

1. Shared UI kit: `Badge`/`statusTone`, `Dialog`, `ConfirmDialog`, `Select`, `Skeleton`/`TableSkeleton`, `StatCard`, `PageHeader`, `EmptyState`, plus a hand-rolled `ToastProvider`.
2. `lib/jwt.ts` — client-side JWT payload decode (display only, not verification) so the UI can show the signed-in username/role and gate nav without a new backend endpoint.
3. `DashboardLayout` rewritten as a sidebar with `lucide-react` icons and role-aware nav filtering.
4. `DashboardPage`, `AccountsPage` (+ create/detail dialogs), `ApiKeysPage` (+ create/rotate/revoke + one-time secret reveal), `AdminUsersPage` (+ create), `AuditLogPage` (+ expandable rows).
5. `NodesPage` rebuilt on the shared `Badge`/`PageHeader`/`EmptyState` kit, plus create-node and generate-join-token dialogs (the latter `super_admin`-only, matching the backend route's role requirement).
6. Routing: `/dashboard`, `/accounts`, `/api-keys`, `/admins`, `/audit-log` added alongside the existing `/nodes`; default redirect moved from `/nodes` to `/dashboard`.
7. Backend fix: `internal/store/accounts.go` node-eligibility checks accept `status IN ('registered', 'online')`.
8. Verification: `tsc -b`, `vite build`, then a real Docker stack + real node agent process + Playwright-driven Chrome exercising every page and every lifecycle action, including a standalone check that the `support` role's sidebar correctly omits `super_admin`-only links.

## Out of scope (deferred, per the PRD audit)

- A dedicated `/stats` or `/dashboard` aggregate backend endpoint — the Dashboard page currently computes totals client-side from the existing list endpoints, which is fine at today's scale but won't be once account/node counts grow past what a single unpaginated list call should return.
- Real-time (WebSocket) status updates, RTL/Farsi localization — unchanged from Story 6's deferral, still waiting on their own design work.
- OpenAPI/Swagger documentation of the full API surface — tracked as its own follow-up.
