# PRD: Core WireGuard Account (Peer) Management

## 1. Summary
Defines the data model, state machine, and business rules for a WireGuard "account" (a peer) — the core object that both the admin UI and the bot-facing API (see [PRD-telegram-bot-api.md](PRD-telegram-bot-api.md)) operate on. This PRD is the source of truth for *what an account is and how it behaves*; the API PRD defines *how it's reached over the wire*.

## 2. Goals
- One unambiguous account lifecycle, enforced identically whether triggered from the admin UI or the bot API.
- Deterministic key generation and IP allocation with no collisions across nodes.
- Quota/expiry enforcement that's safe under node/control-plane connectivity gaps.

## 3. Non-Goals
- Pricing, plans, payment — owned by the Telegram bot.
- Node provisioning itself — see [PRD-node-management.md](PRD-node-management.md).

## 4. Data Model

| Field | Notes |
|---|---|
| `id` | Internal UUID |
| `external_ref` | Caller-supplied idempotency key (e.g. bot's internal user/order id), unique per API-key namespace |
| `label` | Human-readable name shown in admin UI |
| `node_id` | Assigned WireGuard node |
| `public_key` / `private_key` | Generated server-side; `private_key` stored encrypted at rest (envelope-encrypted with a KMS/master key) so configs can be re-downloaded without re-keying the client |
| `assigned_ip` | Allocated from the node's peer subnet |
| `data_quota_bytes` | Nullable = unlimited |
| `data_used_bytes` | Cumulative, updated from stats pipeline (see [PRD-monitoring-stats.md](PRD-monitoring-stats.md)) |
| `expiry_at` | Nullable = never expires |
| `device_limit` | Max simultaneous distinct source endpoints tolerated (soft-enforced, see §6.4) |
| `status` | `pending` \| `active` \| `suspended` \| `deleted` |
| `suspend_reason` | `quota_exceeded` \| `expired` \| `manual` \| `abuse_flag` \| `null` |
| `owner_key_namespace` | Which API key/reseller created this account (for isolation, per API PRD §5) |
| `created_at`, `updated_at` |

## 5. State Machine

```
pending --(agent confirms peer applied)--> active
active --(quota reached | expiry reached | manual suspend | abuse flag)--> suspended
suspended --(renew adds quota/time | manual enable)--> active
active/suspended --(delete)--> deleted (soft, purged after 30 days)
```

- `pending` exists only for the (sub-second, normally invisible) window between "control plane accepted the create request" and "node agent confirmed the peer is live on the interface." If an account is still `pending` after 30s, it's flagged `provisioning_failed` and surfaced to the admin.
- `deleted` is soft for 30 days (audit/recovery), then hard-purged along with key material.

## 6. Functional Requirements & Acceptance Criteria

### 6.1 Key generation & IP allocation
- Given a create request, the control plane generates a WireGuard keypair server-side (never trusts a caller-supplied key) and allocates the next free IP from the target node's configured subnet.
- Given a node's subnet is exhausted, account creation on that node fails with `node_capacity_exceeded` and (if the request used `node_id: "auto"`) the panel retries against the next least-loaded allowed node before failing.
- Given an account is deleted (soft), its IP is held for 24 hours before being returned to the free pool, to avoid a just-disconnected client's stale routes colliding with a newly issued peer.

### 6.2 Quota enforcement
- Given `data_used_bytes >= data_quota_bytes`, the account transitions to `suspended` / `quota_exceeded` and the peer is removed from the live WireGuard interface within one stats-ingestion cycle (≤30s, per the monitoring PRD's polling interval).
- Given a renew call adds quota (`add_quota_gb`), if the account was suspended solely for `quota_exceeded` and new quota exceeds current usage, it auto-transitions back to `active` and is re-applied to the node.
- Given quota enforcement must survive a control-plane blip, the **node agent** independently tracks each peer's last-synced quota/expiry and can suspend a peer locally (remove from interface) even if it can't currently reach the control plane, then reconciles state once connectivity resumes.

### 6.3 Expiry enforcement
- Given `expiry_at` passes, the same suspend flow as quota applies, with `suspend_reason = expired`.
- Given an `expiring_soon` lead time (configurable, default 24h before `expiry_at`), a webhook event fires once (not repeated) so the bot can notify the customer (per API PRD §8.2).

### 6.4 Device limit (soft enforcement)
- WireGuard doesn't natively support named "devices" — the same keypair/config can be used from unlimited simultaneous physical devices. The panel approximates enforcement by tracking **distinct source endpoints (IP:port) seen within a rolling 5-minute window** per peer.
- Given distinct source endpoints exceed `device_limit` within the window, the account is flagged `device_limit_exceeded` (visible in the UI and via a webhook event) but is **not** auto-suspended by default — this is a detection/notification signal, not a hard cutoff, since NAT/roaming can produce false positives. A "hard enforce" toggle is available per-account for operators who want an automatic suspend instead.

### 6.5 Config delivery
- Given a request for `/accounts/{id}/config`, the panel returns a ready-to-import `.conf` file (with the node's current endpoint/public key filled in) and a QR-code image, regenerable at any time without changing the underlying keys — unless the account has been migrated to a different node (see node-management PRD), in which case the endpoint fields change and re-import is required.

## 7. Non-Functional Requirements
- Private key material encrypted at rest; decrypted only transiently when generating a config response.
- Account create → peer live on node: p95 ≤ 2s (matches API PRD §6.2).
- All state transitions written to the audit log (who/what/when/why), regardless of whether triggered via UI or bot API.
