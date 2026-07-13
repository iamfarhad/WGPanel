# PRD: Multi-Node (Server) Management

## 1. Summary
Covers how WireGuard servers ("nodes") are registered, grouped, health-checked, and selected for new accounts. Builds on the hub-and-spoke architecture (control plane + per-node agent) established in project discussion and implemented via `deploy/install-node.sh`.

## 2. Goals
- Zero-touch node onboarding via a join token, no manual credential copying.
- Reliable, fast (≤30s) detection of node outages.
- Deterministic, load-aware node selection for new accounts (`node_id: "auto"` in the account API).

## 3. Non-Goals
- Automated live migration of existing peers between nodes (flagged as a manual/future action, §6.5).
- Node provisioning of the underlying VPS/server itself (out of scope — assumes the admin already has a server with the install script run on it).

## 4. Data Model

| Field | Notes |
|---|---|
| `id` | Internal UUID |
| `name` | e.g. `de-frankfurt-1` |
| `node_group` | Used for bot API key scoping and auto-selection pools |
| `public_endpoint` | Host:port clients connect to |
| `wg_subnet` | CIDR for peer IP allocation on this node |
| `capacity_max_peers` | Soft cap used in load calculations |
| `status` | `pending` \| `online` \| `degraded` \| `offline` (STORY-02/03 implement only `pending`/`registered` so far - see below) |
| `public_key` | The node's own WireGuard server public key, needed to render client configs (`PRD-account-management.md` §6.5). Added while implementing account config delivery (STORY-03) - admin-supplied for now, since there's no real agent yet to report it automatically. |
| `last_heartbeat_at` | |
| `mtls_cert_fingerprint` | Issued at registration, used to authenticate the agent's gRPC stream |
| `created_at` |

## 5. Node Registration Flow

1. Admin: **Nodes → Add Node**, sets name/group/subnet/capacity, clicks **Generate Join Token** — a single-use token valid for `NODE_AGENT_JOIN_TOKEN_TTL_MIN` (default 30 min, per `deploy/.env.example`).
2. Admin runs `install-node.sh` on the target server, entering the control-plane address and the token.
3. Agent performs a one-time exchange over plain TLS (the join token itself is the trust anchor for this single bootstrap call - the agent generates its own keypair locally and submits a CSR, so its private key never crosses the network): the control plane validates the token (unused + unexpired), signs the CSR with its internal CA, and marks the node `registered`.
4. Agent starts sending periodic HTTPS+mTLS heartbeats (its issued client cert authenticates every call, no bootstrap token needed again); on the first successful heartbeat, the node flips to `online`.

*(Implementation note, STORY-04: built over plain HTTPS + mutual TLS rather than gRPC - see `docs/STORY-04-node-agent-mtls.md` for why. The properties this section actually depends on - agent-initiated, works through NAT, mutually authenticated, periodic - hold either way.)*

### Acceptance Criteria
- Given a join token already used once, a second registration attempt with it is rejected (`token_already_used`).
- Given a token older than its TTL, registration is rejected (`token_expired`) and the admin must generate a new one.
- Given a successful registration, the node's `mtls_cert_fingerprint` is pinned — a future connection presenting a different certificate for the same node id is rejected, surfaced as a security alert (possible node impersonation).
- Given step 3 completes, the node reaches `online` within 10 seconds without any further manual action.

## 6. Health & Load

### 6.1 Heartbeats
- Agent sends an HTTPS+mTLS heartbeat call every 10s, including: current peer count, CPU/load average, and connectivity health.
- Given no heartbeat received for 30s, the control plane marks the node `offline`, fires the `node.down` webhook (per API PRD §8.2), and excludes it from `auto` node selection until it recovers.
- Given a heartbeat resumes after an `offline` period, the node transitions to `online`, fires `node.recovered`, and existing peers on it (which the agent kept running locally throughout, per the account-management PRD's offline-tolerant design) require no action.
- A node reporting `peer_count / capacity_max_peers > 0.9` is marked `degraded` — still usable, but deprioritized in auto-selection and flagged in the UI.

### 6.2 Auto Node Selection
- Given a create-account request with `node_id: "auto"` and the requesting API key's allowed `node_group`, the panel selects the `online` (never `offline`, `degraded` only as fallback) node in that group with the lowest `peer_count / capacity_max_peers` ratio.
- Given no `online` node is available in the allowed group, the request fails with `no_available_node` rather than silently picking an `offline` one.

### 6.3 Node Groups
- Every node belongs to exactly one `node_group` (e.g., `eu`, `us`, `premium`). Bot API keys are scoped to one or more groups (per API PRD §5.1), so a reseller only ever provisions accounts onto nodes they're entitled to use.

### 6.4 Manual Node Actions
- Admin can manually: edit capacity/subnet (subnet edits blocked while active peers exist on the node), remove a node (blocked unless it has zero active accounts), and force-mark a node `offline` (maintenance mode) to drain it from auto-selection without waiting for a real outage.

### 6.5 Peer Migration (manual, v1)
Since a peer's keypair isn't tied to a specific node, migrating an account to a different node is possible but changes the client's endpoint, requiring the customer to re-import their config. In v1 this is an explicit admin/bot-triggered action, not automatic:
- `POST /api/v1/accounts/{id}/migrate {target_node_id}` — deallocates the IP on the old node, allocates a new one on the target, re-applies the peer, and fires an `account.migrated` webhook so the bot can prompt the customer to redownload their config.
- Given a migration request to a node without capacity, it fails with `node_capacity_exceeded` and the account remains untouched on its current node.

## 7. Non-Functional Requirements
- Node outage detection: ≤30 seconds from last real heartbeat.
- Agent-to-control-plane transport: HTTPS with mutual TLS only for anything past initial registration; plaintext connections are rejected at the listener.
- Node list/detail views must reflect heartbeat-derived status with no more than a few seconds of UI staleness (via the same real-time push channel used for peer status, see [PRD-monitoring-stats.md](PRD-monitoring-stats.md)).
