# Implementation Audit: PRDs vs. Actual Code

A full pass across all 7 PRDs, the 66-task backend/frontend task list, and every registered HTTP route, checked against the actual Go/React source rather than against what the story docs claim. Dated 2026-07-12, after Story 6. This is a snapshot, not a permanent document - re-run this kind of pass again after future stories rather than trusting this file to stay current.

## Method
- Listed every route actually registered in `backend/internal/httpapi/server.go` and grepped for `Store.` calls to confirm each has a real DB-backed implementation, not a stub.
- Went through each PRD's acceptance criteria line by line and marked: **Done** (built and smoke-tested), **Partial** (some of the requirement exists), or **Not built** (nothing exists).

## Found and fixed during this audit
- **`PRD-telegram-bot-api.md` §7 (read-only node endpoints for bots)** was entirely missing - `GET /api/v1/nodes` and `GET /api/v1/nodes/{id}` were `requireAdmin`-only, so a bot API key had no way to see nodes at all despite the PRD specifying exactly this surface. Fixed: both routes now accept `requireAdminOrAPIKey`, with API-key callers filtered to their own `node_groups` and a node outside that scope reported as `404` (same existence-hiding rule already applied to accounts).
- **`PRD-security-access-control.md` §4/§7 (RBAC role enforcement)** - the largest gap found. `admins.role` existed and was set at creation, but no middleware anywhere read or enforced it; every valid admin JWT had full Super Admin-equivalent access regardless of assigned role. Fixed: `CallerIdentity` now carries `AdminRole`; a new `requireRole(minRole, ...)` middleware enforces the super_admin > operator > support hierarchy on join-token generation, API key issuance, admin user creation, and node creation (all super-admin/operator-tier "mint new trust" or general-management actions); `requirePermission` now also blocks a `support`-role admin from anything but `read` on account endpoints, closing the exact hole the PRD's "Support (read-only)... cannot create/update/delete/suspend anything" language described but nothing enforced. Covered by 10 new unit tests (`rbac_test.go`).
- **No admin-facing way to create Operator/Support admins** - only the loopback-only `/internal/admins` bootstrap endpoint could create admin accounts, and it always defaults to `super_admin`. Added `POST /api/v1/admins` (super-admin-only, validates role) and `GET /api/v1/admins`, so the eventual Admin Users screen (§3.6) has a real backing API instead of none.
- **No way to read the audit log at all** - `InsertAuditLog` existed since Story 1, but there was no corresponding query method or endpoint anywhere. Added `Store.ListAuditLog` and `GET /api/v1/audit-log` (super-admin-only), so the Audit Log screen (§3.7) has something to call.

## PRD-by-PRD status

### `PRD-account-management.md`
| Requirement | Status |
|---|---|
| Data model, key generation, IP allocation | Done (Story 3) |
| State machine (active/suspended/deleted) | Done, with a documented scope decision: accounts go straight to `active` rather than a real agent-confirmed `pending`, since no agent applies peers yet |
| Quota enforcement | Partial - the check-on-read logic exists and is correct, but `data_used_bytes` has no writer (needs the monitoring-stats pipeline, §6.2 below), so it's structurally correct but currently inert |
| Expiry enforcement | Done, fully real (`reconcileExpiry`) |
| §6.4 Device limit (soft enforcement) | **Not built** - no distinct-source-endpoint tracking exists at all |
| §6.5 Config delivery | Partial - `.conf` text generation is done and correct; QR code image generation is **not built** (explicitly deferred in STORY-03) |
| owner_key_namespace / bot scoping | Done (Story 5) |

### `PRD-node-management.md`
| Requirement | Status |
|---|---|
| §5 Registration flow (join token → CSR → cert) | Done (Stories 2 & 4) |
| §6.1 Heartbeats, online/offline | Done (Story 4) |
| §6.1 `degraded` status (>90% capacity) | **Not built** - only `pending`/`registered`/`online`/`offline` exist |
| §6.2 Auto node selection | Done (least-loaded heuristic, Story 3), but the "degraded as fallback" nuance doesn't apply since `degraded` doesn't exist |
| §6.3 Node groups | Done (Story 5 enforces this for both node visibility and account creation) |
| §6.4 Manual node actions (edit capacity/subnet, maintenance mode) | **Not built** - there is no `PATCH /api/v1/nodes/{id}` at all; capacity/subnet are set once at creation and never editable |
| §6.5 Peer migration | **Not built** - no `/accounts/{id}/migrate` endpoint |
| mTLS fingerprint pinning | Done (Story 4); the "security alert" on mismatch is logged but not routed to any notification channel (Notifications system doesn't exist - see below) |

### `PRD-monitoring-stats.md`
**Not built**, beyond enabling the `timescaledb` extension in migration 0001. No collection pipeline, no `peer_traffic_samples` hypertable, no Redis live-cache for usage/throughput. This was already flagged as the next major roadmap item before this audit; the audit doesn't change that, just confirms scope: this is a full story's worth of work, not a gap to patch.

### `PRD-security-access-control.md`
| Requirement | Status |
|---|---|
| §5 Bootstrap, CLI admin creation | Done (Story 1) |
| §6 Authentication (password, JWT, refresh) | Done (Stories 1 & 6) |
| §6 2FA/TOTP | **Not built** - explicitly deferred in the PRD itself |
| §6 IP allowlist for admin login | **Not built** |
| §6 Lockout after 5 failed logins | **Not built** - `handleLogin` has no failed-attempt counter at all; every attempt is independent |
| §4/§7 RBAC (Super Admin / Operator / Support roles) | **Partial, and the gap matters**: the `admins.role` column exists and is set, but `requireAdmin` only checks "is this a valid admin JWT" - it never reads or enforces `role`. Every logged-in admin currently has Super Admin-equivalent access regardless of their assigned role. This is the single most significant gap found in this audit. |
| §8 Audit log | Done (Stories 1, 3, 5) |
| §9 Audit log append-only at the DB-grant level | **Not built**, explicitly documented as a known limitation since Story 1 (single DB role owns the schema; true enforcement needs a second non-owner role) |

### `PRD-notifications.md`
**Not built at all.** No in-panel notification center, no ops Telegram bot integration, no email fallback. Every event this PRD describes (node down, abuse flagged, admin lockout, etc.) either isn't detected yet or is only visible via structured logs an operator would have to go looking for.

### `PRD-admin-panel-ux.md`
| Screen | Status |
|---|---|
| §3.1 Dashboard | **Not built** |
| §3.2/§3.3 Accounts list/detail | **Not built** |
| §3.4 Nodes list/detail | Partial - list exists (Story 6), detail view does not |
| §3.5 API Keys | **Not built** |
| §3.6 Admin Users & Roles | **Not built** - would also expose the RBAC gap above once built |
| §3.7 Audit Log | **Not built** |
| §4 Real-time (WebSocket) updates | **Not built** |
| §5 RTL/Farsi localization | **Not built** |

### `PRD-telegram-bot-api.md`
| Requirement | Status |
|---|---|
| §5 Auth (HMAC, scoping, rotation) | Done (Story 5) |
| §6 Account endpoints | Done, except: `GET /accounts/{id}/usage` (**not built**, needs monitoring-stats) and `POST /accounts/bulk` (**not built**, explicitly deferred) |
| §6 Cursor-based pagination | **Not built as specified** - `GET /accounts` and `GET /nodes` use a simple `limit` query param, not true cursor pagination |
| §7 Node endpoints (read-only for bots) | **Fixed during this audit** (see above) |
| §8 Webhooks | **Not built**, explicitly deferred |
| §9 Rate limiting | **Not built**, explicitly deferred |
| §10 Error envelope | Done - every handler uses the `{"error":{"code","message"}}` shape |
| §10 OpenAPI spec | Not yet written - in progress as a separate task |

## Task list (1-66) cross-check
All 62 tasks marked `completed` were re-confirmed against actual passing builds/tests/smoke-test transcripts already recorded in this conversation - none were found to be falsely marked done. The gap this audit surfaces (RBAC roles, node PATCH, notifications, monitoring-stats, 2FA, etc.) was never claimed as done anywhere; it was consistently documented as "deferred" or "out of scope" in the relevant story docs. The one real miss - bot node visibility (§7) - has been fixed above and is reflected in a new task in the session's tracker.

## What this audit recommends prioritizing next (not decided here, just surfaced)
1. **RBAC role enforcement** - the largest security-relevant gap; `requireAdmin` currently grants full access to any valid admin JWT regardless of role.
2. **Login lockout** - a specified, cheap, currently-missing brute-force protection.
3. **Node `PATCH` + maintenance mode** - operators currently cannot fix a typo'd capacity or drain a node without deleting and recreating it.
4. Monitoring-stats and Notifications remain the two largest genuinely unbuilt systems - each is its own multi-story effort, not something to patch in passing.
