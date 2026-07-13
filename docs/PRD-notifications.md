# PRD: Internal/Admin Notifications

## 1. Summary
Operational alerting for the **panel's own admins/operators** — distinct from the customer-facing events the bot receives via webhooks (per [PRD-telegram-bot-api.md](PRD-telegram-bot-api.md) §8). This is "tell the ops team something needs attention," not "tell the customer their plan expired."

## 2. Goals
- Admins learn about infrastructure problems (node down, abuse patterns, security events) before a customer complains.
- Channel-agnostic delivery so this doesn't hard-depend on any one notification service.

## 3. Non-Goals
- Customer notifications — entirely the bot's responsibility, triggered by the webhook events already defined in the API PRD.
- SMS/push notifications — out of scope for v1 (Telegram + email cover the target ops workflow).

## 4. Events & Default Severity

| Event | Severity | Source |
|---|---|---|
| `node.down` | Critical | Node management PRD §6.1 |
| `node.recovered` | Info | Node management PRD §6.1 |
| `node.capacity_high` (>90% peers) | Warning | Node management PRD §6.1 |
| `account.abuse_flagged` (device-limit exceeded, anomalous usage spike) | Warning | Account management PRD §6.4 |
| `api.error_rate_high` (per-key 5xx/429 spike) | Warning | API PRD §11 |
| `admin.login_failed_lockout` | Warning | Security PRD §6 |
| `node.mtls_fingerprint_mismatch` (possible impersonation) | Critical | Node management PRD §5 |
| `agent.stats_buffer_overflow` (data loss during prolonged outage) | Warning | Monitoring PRD §6.2 |

## 5. Delivery Channels
- **In-panel notification center** — always on, no configuration needed; every event above appears here regardless of other channel settings.
- **Telegram (ops channel)** — a *separate* bot/chat from the customer-facing sales bot, configured with its own bot token + chat id in admin settings.
- **Email** — SMTP configuration in admin settings, for admins who prefer it or as a fallback if the Telegram ops bot itself is misconfigured.

Each admin can subscribe/unsubscribe per event type and per channel from their profile settings; Critical-severity events cannot be fully muted (they can be routed to a different channel, but not silenced entirely) to avoid a misconfigured node outage going unnoticed.

## 6. Acceptance Criteria
- Given a `node.down` event fires, it appears in the in-panel notification center within 5 seconds and is delivered to all subscribed channels within 30 seconds.
- Given the ops Telegram bot token is invalid/misconfigured, delivery falls back silently to email for Critical events and surfaces a persistent "notification channel broken" banner in the admin UI (so the misconfiguration itself doesn't go unnoticed).
- Given an admin has muted Warning-severity events for `api.error_rate_high`, they still see it in the in-panel notification center (never fully suppressed, per §5).
- Given the same underlying condition persists (e.g., a node stays down), the event is not re-delivered every heartbeat cycle — a de-duplication window (default 15 min) prevents alert spam, with a single follow-up reminder if the condition is still unresolved after 1 hour.
- Every delivered notification is linked back to its audit-log entry where applicable (e.g., `admin.login_failed_lockout` links to the relevant audit rows), so investigating from the notification is one click.

## 7. Non-Functional Requirements
- Notification delivery must not block the event's originating process (e.g., stats ingestion) — dispatched asynchronously via the same Redis-backed job queue used for webhook retries.
- Telegram/SMTP credentials for the ops channel are stored the same way as other secrets (encrypted at rest), never exposed via any read API.
