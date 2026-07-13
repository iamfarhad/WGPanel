# PRD: Panel API for Telegram Bot Integration

## 1. Summary
The WireGuard Enterprise Panel manages WireGuard accounts (peers) and server nodes. It does **not** handle sales, billing, or payments — that lives entirely in a separate Telegram bot. This PRD covers the API layer that the Telegram bot (and potentially other reseller bots) use to create, manage, and monitor accounts on the panel.

## 2. Goals
- Give bot clients full programmatic control over the account lifecycle (create, renew, suspend, delete, inspect) without needing panel UI access.
- Support multiple independent bots/resellers, each scoped to only their own accounts/nodes.
- Push real-time events to bots (expiry, quota thresholds, node health) instead of requiring constant polling.
- Handle enterprise scale: 10k–100k+ accounts, bursty traffic patterns typical of a Telegram bot (spikes on payment confirmation, mass renewals, etc.).

## 3. Non-Goals
- No payment processing, invoicing, or pricing logic in the panel — the bot owns all sales/billing logic and simply tells the panel "provision this account with these parameters" or "extend this account."
- No end-user-facing UI beyond the existing admin panel.

## 4. Actors
| Actor | Description |
|---|---|
| **Bot Client** | A Telegram bot backend holding an API key/secret scoped to its own reseller namespace. |
| **Super Admin** | Issues/revokes API keys, sets scopes and rate limits per bot client. |
| **Panel API** | The system described in this PRD. |

## 5. Authentication & Authorization

### 5.1 API Key Model
- Each bot client is issued an `api_key` (public identifier) and `api_secret` (never transmitted after issuance, only used to sign requests).
- Every request must include:
  - `X-API-Key: <api_key>`
  - `X-Timestamp: <unix_ts>`
  - `X-Signature: HMAC-SHA256(api_secret, method + path + timestamp + body)`
- Requests with a timestamp outside a ±5 minute window are rejected (replay protection).
- Keys are scoped to:
  - A specific **reseller namespace** (the set of accounts it may see/manage — created accounts are automatically tagged with the owning key's namespace).
  - A specific **node group** (which servers it's allowed to provision accounts on), or "all nodes."
  - A **permission set**: `read`, `create`, `update`, `suspend`, `delete`, `bulk` (independently toggleable).
- Keys support: creation, rotation (issue new secret without downtime — both old and new valid for a grace period), revocation, and optional expiry date.

### 5.2 Acceptance Criteria
- Given a request with an invalid or missing signature, the API returns `401 Unauthorized`.
- Given a request timestamp older than 5 minutes, the API returns `401 Unauthorized` with reason `stale_timestamp`.
- Given a bot client key scoped to reseller namespace `A`, a request for an account belonging to namespace `B` returns `404 Not Found` (not `403`, to avoid leaking existence).
- Given a key without `delete` permission, a `DELETE /accounts/{id}` call returns `403 Forbidden`.
- Given an admin rotates a key's secret, the old secret remains valid for 24 hours before automatic invalidation.
- All key creation/rotation/revocation actions are recorded in the audit log with the acting admin's identity.

## 6. Account Management Endpoints

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/v1/accounts` | Create account (peer) |
| GET | `/api/v1/accounts/{id}` | Get account detail |
| GET | `/api/v1/accounts` | List/search accounts (cursor paginated) |
| PATCH | `/api/v1/accounts/{id}` | Update quota/expiry/device_limit/label |
| POST | `/api/v1/accounts/{id}/renew` | Extend expiry and/or add quota |
| POST | `/api/v1/accounts/{id}/suspend` | Suspend (soft disable) |
| POST | `/api/v1/accounts/{id}/enable` | Re-enable a suspended account |
| DELETE | `/api/v1/accounts/{id}` | Permanently delete account + peer config |
| GET | `/api/v1/accounts/{id}/config` | Get WireGuard config file + QR code |
| GET | `/api/v1/accounts/{id}/usage` | Current + historical traffic usage |
| POST | `/api/v1/accounts/bulk` | Bulk create/update/suspend (returns job) |
| GET | `/api/v1/jobs/{job_id}` | Poll async bulk job status |

### 6.1 Create Account — request shape
```json
POST /api/v1/accounts
{
  "external_ref": "tg_user_582910",
  "label": "Ali - Plan Gold",
  "node_id": "auto",
  "data_quota_gb": 50,
  "expiry_date": "2026-08-11T00:00:00Z",
  "device_limit": 2
}
```
`node_id: "auto"` lets the panel pick the least-loaded node within the key's allowed node group.

### 6.2 Acceptance Criteria
- Given a valid create request, the API provisions a WireGuard keypair, allocates a free IP within the target node's subnet, writes the peer to the node config, and returns `201 Created` with `account_id`, `public_key`, `assigned_ip`, `node_id` within 2 seconds (p95).
- Given `node_id: "auto"` and the key is scoped to multiple nodes, the panel selects the node with lowest current load among allowed nodes.
- Given a duplicate `external_ref` for the same API key namespace, the API returns `409 Conflict` (idempotency — prevents double-provisioning on bot retries).
- Given a renew request with `add_quota_gb` and/or `extend_days`, the account's quota/expiry increase additively (not overwritten), and the account is auto re-enabled if it was suspended solely due to quota/expiry.
- Given a suspend call, the peer is removed from the live WireGuard interface (connection drops within 5s) but its record, keys, and usage history are retained for reactivation.
- Given a delete call, the peer is removed from the node and the account record is soft-deleted (retained 30 days for audit) then purged.
- Given a `GET /accounts` call, results are cursor-paginated (default page size 20, max 100), filterable by `status`, `node_id`, `expiring_before`, `external_ref`.
- Given a bulk request of up to 5,000 items, the API returns `202 Accepted` with a `job_id` immediately; processing continues asynchronously and is queryable via `/jobs/{job_id}` with per-item success/failure detail.

## 7. Node Endpoints (read-only for bots)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/nodes` | List nodes visible to this key, with status/load |
| GET | `/api/v1/nodes/{id}` | Node detail |

### Acceptance Criteria
- Given a bot key scoped to a node group, `/nodes` returns only nodes in that group, each with `status` (online/offline), `load_pct`, `active_peers`, `capacity`.
- Given a node goes offline, its `status` field updates within 30 seconds of the panel's health-check detecting the outage.

## 8. Webhooks

### 8.1 Registration
| Method | Path | Purpose |
|---|---|---|
| POST | `/api/v1/webhooks` | Register a webhook URL + subscribed events |
| GET | `/api/v1/webhooks` | List registered webhooks for this key |
| DELETE | `/api/v1/webhooks/{id}` | Remove a webhook |
| GET | `/api/v1/webhooks/{id}/deliveries` | Delivery log (status, retries, response codes) |

### 8.2 Events
| Event | Trigger |
|---|---|
| `account.quota_threshold` | Usage crosses configurable % (default 80%, 100%) |
| `account.expiring_soon` | Configurable lead time before expiry (default 24h) |
| `account.expired` | Expiry reached, account auto-suspended |
| `account.suspended` / `account.enabled` | Status change (regardless of cause) |
| `node.down` / `node.recovered` | Node health transitions |

### 8.3 Delivery
- Payloads are signed: `X-Webhook-Signature: HMAC-SHA256(webhook_secret, body)`.
- On non-2xx response or timeout (5s), retries follow exponential backoff: 1m, 5m, 30m, 2h, 12h (5 attempts total), then marked `failed` and visible in the delivery log.
- Bots can replay a failed delivery manually via `POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver`.

### 8.4 Acceptance Criteria
- Given an account crosses 80% quota usage, a `account.quota_threshold` event is delivered to all subscribed webhooks within 60 seconds.
- Given a webhook endpoint is unreachable, the panel retries per the backoff schedule and does not drop the event silently — it's visible via the delivery log with `status: failed` after exhausting retries.
- Given a bot registers a webhook, a test `ping` event can be triggered on-demand via `POST /api/v1/webhooks/{id}/test` to verify connectivity before going live.

## 9. Rate Limiting & Quotas
- Per-API-key limits, configurable by admin, with sane defaults:
  - Read endpoints: 50 req/s sustained, burst 200.
  - Write endpoints (create/update/suspend/delete): 20 req/s sustained, burst 50.
  - Bulk endpoint: 5 req/minute (separate bucket; bulk jobs themselves aren't rate-limited internally).
- Exceeding a limit returns `429 Too Many Requests` with a `Retry-After` header.
- Limits are enforced via a token-bucket algorithm per key, visible to the admin in real time (current usage %).

### Acceptance Criteria
- Given a bot exceeds its sustained rate, subsequent requests within the same window return `429` with an accurate `Retry-After` value.
- Given traffic returns below the threshold, requests succeed again without manual intervention.
- Given an admin raises a key's rate limit, the new limit takes effect within 60 seconds without requiring a key rotation.

## 10. Versioning, Errors, Docs
- All endpoints are namespaced `/api/v1/...`; breaking changes ship as `/api/v2/...` with `v1` supported for a minimum 6-month deprecation window.
- Errors follow a consistent envelope:
```json
{ "error": { "code": "quota_exceeded", "message": "...", "request_id": "..." } }
```
- Full OpenAPI 3.0 spec is published and kept in sync with the implementation; a sandbox/staging environment with synthetic nodes is available for bot developers to test against without touching production WireGuard servers.

### Acceptance Criteria
- Given any API error, the response includes a stable `error.code` (machine-parseable) and a `request_id` that can be cross-referenced in the audit log.
- Given a client calls a deprecated `v1` endpoint, the response includes a `Deprecation` and `Sunset` header once `v2` is available.
- Given the OpenAPI spec, it can be imported into Postman/Insomnia and successfully round-trip all documented endpoints against the sandbox.

## 11. Observability & Audit
- Every API call is logged: `api_key_id`, `endpoint`, `method`, `status_code`, `latency_ms`, `ip_address`, `request_id`, timestamp.
- Admin panel exposes a searchable API activity log per key.
- Metrics exported (Prometheus-compatible): request rate, error rate, p50/p95/p99 latency, per-key rate-limit rejections, webhook delivery success rate.

### Acceptance Criteria
- Given any API request, a corresponding audit log entry exists searchable by `api_key_id` within 5 seconds.
- Given a spike in `429` or `5xx` responses for a key, an internal alert fires to the admin (not the bot) so infra issues are caught proactively.

## 12. Non-Functional Requirements
| Requirement | Target |
|---|---|
| Single-account op latency | p95 < 200ms, p99 < 500ms |
| Sustained throughput | 50 req/s per key baseline, horizontally scalable |
| Availability | 99.9% monthly for the API layer |
| Bulk job (5,000 items) completion | < 10 minutes |
| Data isolation | Zero cross-namespace data leakage (verified by automated test suite) |
| Transport security | TLS 1.3 preferred, TLS 1.2 minimum; HMAC secrets never logged |
