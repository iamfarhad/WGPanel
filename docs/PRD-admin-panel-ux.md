# PRD: Admin Panel UX

## 1. Summary
The React admin panel's screens and interactions for operators managing accounts and nodes day-to-day. Built on the stack chosen in project discussion: React + TypeScript + Vite, shadcn/ui + Tailwind, TanStack Query/Table, real-time updates over WebSocket.

## 2. Goals
- An operator can find, inspect, and act on any account or node in under a few clicks.
- Live status (online/offline, node health) without manual refreshing.
- Consistent with role restrictions from [PRD-security-access-control.md](PRD-security-access-control.md) — read-only roles never see enabled action buttons they can't use.

## 3. Non-Goals
- Sales/billing UI (plans, pricing, invoices) — none of that exists in this panel; it lives entirely in the Telegram bot.
- The underlying auth/RBAC rules and data pipelines this UI surfaces — those are the responsibility of [PRD-security-access-control.md](PRD-security-access-control.md) and [PRD-monitoring-stats.md](PRD-monitoring-stats.md) respectively; this PRD only covers how they're presented.

## 4. Screens

### 4.1 Dashboard (landing page)
- Summary cards: total accounts (active/suspended/expired breakdown), total nodes (online/degraded/offline), aggregate throughput across all nodes.
- No revenue/sales metrics — explicitly out of scope per the panel's account-only mandate.
- Recent notifications feed (from [PRD-notifications.md](PRD-notifications.md)) inline.

### 4.2 Accounts List
- Table (TanStack Table) with columns: label, status, node, usage (progress bar), expiry, last handshake.
- Filter by status/node/expiring-within/owner-namespace; search by label or `external_ref`.
- Cursor-paginated to match the API's pagination model (API PRD §6.2), default 20/page.
- Row actions: suspend/enable/renew/delete, gated by role.
- Bulk select + bulk action bar (suspend/renew/delete a selection), calling the same bulk endpoint the bot API uses.

### 4.3 Account Detail
- Header: status, node, quick actions.
- Usage-over-time chart (sourced from the monitoring pipeline, PRD-monitoring-stats.md §6.3).
- Config/QR regeneration panel.
- Per-account audit trail (filtered view of the global audit log).
- Device/endpoint activity (recent distinct source endpoints, surfacing device-limit signals from account-management PRD §6.4).

### 4.4 Nodes List & Detail
- List: name, group, status badge, load %, active peer count.
- Detail: heartbeat history/uptime, capacity editor, "maintenance mode" toggle (drains from auto-selection per node-management PRD §6.4), join-token generator for re-registration.

### 4.5 API Keys (Super Admin only)
- List of bot/reseller keys: namespace, scopes (node groups, permissions), rate limits, last-used timestamp.
- Create/rotate/revoke flows matching API PRD §5.1, with rotation clearly explaining the 24h grace-period behavior before confirming.
- Webhook registrations and delivery logs per key (API PRD §8).

### 4.6 Admin Users & Roles (Super Admin only)
- Manage admin accounts, assign roles (Security PRD §4), enable/reset 2FA, view/revoke active sessions.

### 4.7 Audit Log
- Global searchable/filterable view (Security PRD §8), linkable from any entity's detail page.

## 5. Real-Time Behavior
- Account status (online/offline) and node status update live via a WebSocket connection — no polling required for the currently-viewed list/detail page.
- Given the WebSocket connection drops, the UI falls back to a visible "reconnecting…" indicator and a 10s poll until it re-establishes, rather than silently showing stale data with no signal.

## 6. Acceptance Criteria
- Given a Support-role user, no suspend/enable/renew/delete/create controls render anywhere in the UI (not just disabled — the API would reject them anyway per Security PRD §7, but the UI doesn't invite the attempt).
- Given an account's status changes (e.g., auto-suspended for quota) while an operator has its detail page open, the status badge updates without a manual refresh within the same latency bound as the monitoring pipeline (≤30s, typically near-instant via WebSocket push).
- Given a bulk action is submitted for >100 accounts, the UI shows it as an async job (matching the API's bulk-job model) with a progress indicator rather than blocking the page.
- Given any list view (accounts, nodes, audit log), filters and pagination state are reflected in the URL so a view can be bookmarked/shared between admins.
- Given the panel is used with a Farsi/RTL locale, layout mirrors correctly (Tailwind's RTL utilities) — verified at least for the accounts list, account detail, and dashboard screens.

## 7. Non-Functional Requirements
- Initial dashboard load: p95 < 1.5s on a broadband connection, backed by the Redis live-cache reads (not raw Postgres aggregation) described in the monitoring PRD.
- Accessible: keyboard-navigable tables/forms, sufficient color contrast for status badges (not color-alone status indicators — paired with text/icon).
