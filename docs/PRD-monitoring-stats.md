# PRD: Monitoring & Peer Statistics

## 1. Summary
Defines the pipeline that turns raw WireGuard interface counters on each node into per-account usage figures, online/offline status, historical graphs, and quota enforcement signals — the data backbone for both the admin dashboard and the bot API's `/accounts/{id}/usage` endpoint.

## 2. Goals
- Accurate, near-real-time per-peer traffic and connection status at 10k-100k+ account scale.
- Historical usage graphs without needing to operate a separate time-series database.
- A statistics pipeline that keeps working (locally, on the node) through brief control-plane outages.

## 3. Non-Goals
- Deep packet inspection or per-destination traffic breakdown — only aggregate rx/tx per peer, as exposed by WireGuard itself.
- Long-term (multi-year) raw retention — see §6.4 for the rollup/retention policy.

## 4. Collection Pipeline

```
wg show <iface> dump  →  node agent (delta + reset detection)  →  HTTPS+mTLS heartbeat batch  →  control plane ingestion
                                                                                              │
                                                                              ┌───────────────┼────────────────┐
                                                                       Redis (live cache)              TimescaleDB (history)
```

### 4.1 Node agent side
- Every 10-30s (configurable), the agent reads live counters for every local peer.
- WireGuard counters are **cumulative since interface-up**, not deltas — the agent keeps the last-seen `(rx_bytes, tx_bytes)` per peer in local state (see `WGPANEL_STATE_DIR` in `install-node.sh`) and computes the delta each cycle.
- **Counter reset detection**: if a peer's current counter is *lower* than the last-seen value, the agent treats it as a reset (interface restart) and counts the new value as the full delta (not `current - previous`, which would be negative/wrong).
- Deltas are batched and pushed over the existing HTTPS+mTLS heartbeat call (no separate connection) to the control plane.
- If the control plane is unreachable, the agent buffers up to 1 hour of deltas locally (bounded ring buffer) and flushes on reconnect — no data loss for typical blips, bounded memory for prolonged ones (oldest samples dropped past the buffer limit, logged as a warning).

### 4.2 Control-plane side
- Ingestion writes each peer's delta as a row into a TimescaleDB hypertable (`peer_traffic_samples`: `account_id`, `node_id`, `ts`, `rx_delta`, `tx_delta`).
- The same ingestion updates `accounts.data_used_bytes` (cumulative) and Redis (`peer:{account_id}:last_handshake`, `:rx_rate`, `:tx_rate`) for live dashboard reads without hitting Postgres per page view.
- Ingestion is idempotent per `(account_id, ts)` to tolerate an agent re-sending a batch after a reconnect.

## 5. Online/Offline Determination
- A peer is considered **online** if its `last_handshake` timestamp (reported alongside the counters) is within the last 180 seconds — WireGuard re-handshakes roughly every 120s under active traffic, so 180s tolerates one missed cycle before flipping to offline.
- Given no traffic at all (idle client), the peer correctly shows offline after 180s even though its config is still valid and quota/expiry enforcement is unaffected by online/offline state.

## 6. Acceptance Criteria

### 6.1 Accuracy
- Given a peer's counters reset due to a node reboot, the reported delta for that cycle equals the new cumulative value (not a negative or corrupted delta).
- Given the same batch is delivered twice (agent retry after a dropped ack), the resulting `data_used_bytes` reflects the delta exactly once (no double-counting).

### 6.2 Freshness
- Given normal connectivity, an account's `data_used_bytes` and online status in the admin UI/API are no more than 30 seconds stale relative to the node's actual counters.
- Given a control-plane outage of under 1 hour, no traffic data is lost once connectivity resumes.
- Given an outage longer than the agent's buffer window, the gap is visible in the historical graph (rendered as a gap, not interpolated or silently dropped), and a log entry records how much was lost.

### 6.3 Dashboard & API surface
- `GET /api/v1/accounts/{id}/usage` (per API PRD) returns current cumulative usage plus a time-bucketed series (hourly/daily, caller-selectable) for graphing.
- The admin dashboard's account list shows live usage %, and the account detail view renders a usage-over-time chart sourced from the same TimescaleDB data.
- Node detail view shows aggregate load (active peers, total throughput) derived from summing that node's current peer set.

### 6.4 Retention & Rollups
- Raw per-sample data: retained 7 days.
- Hourly rollups (continuous aggregate): retained 90 days.
- Daily rollups: retained indefinitely (or per a configurable admin setting).
- Rollup/retention jobs run as scheduled TimescaleDB continuous aggregates + retention policies, not application-level cron, to keep this correct under restarts.

## 7. Non-Functional Requirements
- Ingestion must sustain bursts from all nodes reporting simultaneously at 100k total accounts without falling behind — batched writes, not one row per peer per HTTP call.
- Redis live-cache reads for the dashboard must not fall back to a full Postgres scan under normal operation.
- All of the above scales horizontally by adding ingestion workers behind the HTTPS+mTLS heartbeat endpoint (see `docs/STORY-04-node-agent-mtls.md` for why this is plain HTTPS rather than gRPC); the agent's push model means adding nodes doesn't require re-architecting the collection side.
