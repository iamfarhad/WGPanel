# Story 11: VPN Product Features — Bandwidth Limits, Device Tracking, Subscription URLs, Node Steering

## Story

**As** an operator running WGPanel as a real VPN service, **I want** the four product-differentiating features from the enterprise roadmap — per-account bandwidth limits, device-limit enforcement, subscription URLs for client provisioning, and smart node steering — **so that** accounts can be sold and operated with real service tiers (rate + volume + device caps), clients can self-provision without panel credentials, and connections land on the best node automatically.

## Scope

Four features, all landed in this story (migrations 0014–0017):

### 1. Per-account bandwidth limits (migration 0014)

`accounts.bandwidth_limit_mbps` (NULL = unshaped) — the "rate" sibling of `data_quota_bytes`' "volume" cap.

- Settable at creation (`bandwidth_limit_mbps`, 1–100000) and via PATCH (where `0` explicitly clears the limit — "remove the rate limit" is a real operation in a way removing a quota hasn't needed to be, and 0 Mbps is meaningless as an actual limit, so the sentinel is unambiguous).
- The limit rides along with each desired peer in the heartbeat response (`peers[].bandwidth_limit_mbps`), and `cmd/agent/tc.go` enforces it with tc in the same signature-gated reconcile pass as `ConfigureDevice`:
  - **Egress** on the wg interface (= client download): HTB root qdisc with `default 0` (unlimited peers bypass shaping entirely), one class + `u32 match ip dst <peer-ip>/32` filter per limited peer.
  - **Ingress** (= client upload): `u32 match ip src` + `police rate ... drop`, bucket sized at ~100ms of the allowed rate (32KB floor). Policing rather than IFB-based shaping deliberately — fewer moving parts, adequate at VPN-account granularity.
  - Applied wholesale (teardown + rebuild) on peer-set change, mirroring `ReplacePeers` semantics. A shaping failure (e.g. no `tc` binary on an old bare-metal node) is logged but does **not** hold back the peer signature — holding it back would force `ConfigureDevice` to reapply every 10s, which is exactly the handshake-resetting behavior `peerSignature` exists to prevent.
- The peer signature now includes the limit, so a rate change alone (same peers) still triggers a reconcile.
- Older agents ignore the new field: peers still sync, just without rate enforcement.

### 2. Device-limit enforcement (migration 0015, PRD-account-management.md §6.4 — previously "Not built")

Built to the PRD's spec: distinct source endpoints (IP:port) within a **rolling 5-minute window**, soft enforcement by default.

- The agent now reports each peer's kernel-known endpoint alongside its traffic counters (same single `wgctrl` snapshot). The control plane only counts an endpoint as a live sighting when its **handshake is inside the window** — the kernel remembers a peer's last endpoint indefinitely, long after that client disconnected, so endpoint presence alone would overcount forever.
- `account_devices` table: one row per (account, endpoint) ever seen, upserted batched (`unnest ... ON CONFLICT`), `last_seen_at` guarded with `GREATEST` so re-ordered heartbeats can't move it backwards. Rows unseen for 30 days are pruned by the existing sweep loop (every ~5 min, not every 5s tick).
- On every ingest, accounts whose devices were touched get reconciled inside the same transaction (`FOR UPDATE` ordered by id — same deadlock-avoidance idiom as `CreateAccount`): crossing above the limit sets `device_limit_exceeded_at` + an `account.device_limit_exceeded` audit event; dropping back under clears it (+ `account.device_limit_cleared`). Audit rows are written only **after** the transaction commits — an audit row describing a suspend that then rolled back would be worse than a best-effort insert.
- `accounts.device_limit_hard_enforce` (default false) is the PRD's per-account toggle: when on, crossing the limit suspends with the new `suspend_reason = 'device_limit'`. Clearing never auto-unsuspends — lifting a hard-enforced suspension is a deliberate operator action (Enable), matching how manual/abuse suspensions behave.
- New `GET /api/v1/accounts/{id}/devices`; `device_limit_exceeded`/`device_limit_hard_enforce` on every account response; devices section + auto-suspend toggle + "over device limit" badge in the panel.

### 3. Subscription URLs (migration 0016)

`GET /api/v1/sub/{token}` serves the account's **current** wg-quick config with no panel credentials — the 192-bit random token in the path is the credential (capability URL), the same trust level as the `.conf` contents it returns.

- Kept under `/api/v1/` deliberately: both Caddy (production) and the nginx demo route already proxy `/api/*`, so no deploy-config changes and no interaction with the live domain-management push.
- Node choice defaults to the steering engine's recommendation (`?region=` biases it); `?node_id=` pins one. `GET /api/v1/sub/{token}/nodes` is the machine-readable node picker, each entry with a ready-to-fetch `config_path`.
- Suspended/expired accounts get `403 account_suspended` (with the same lazy expiry reconciliation as every account read) instead of a config that would silently stop handshaking.
- `POST /api/v1/accounts/{id}/subscription/rotate` (update-tier) invalidates a leaked URL immediately without touching the WireGuard keypair — rotating the link shouldn't force every legitimate device to re-import its tunnel.
- Tokens are stored plaintext on purpose (the panel must re-display the URL; anyone who can read the column can already reach the private-key decryption path) and are backfilled for pre-existing accounts by the migration (pgcrypto's `gen_random_bytes`). New accounts get theirs from `crypto/rand` in the handler.
- **The token is a path segment, so the logging middleware now redacts `/api/v1/sub/*` paths** — the existing "bodies are never logged" guarantee wasn't enough on its own for this one route family. Config rendering itself was extracted into `renderPeerConfig`, shared with `GET /accounts/{id}/config` so the two surfaces (and their hard-won DNS/MTU=1280 choices) can never drift apart.

### 4. Smart node steering (migration 0017)

`nodes.region` (free-form label, e.g. `eu`, `us-east`; settable at creation and via node PATCH, `""` clears) plus a scoring engine in `internal/steering` (pure, unit-tested):

- Score = 0.7 × load ratio (active peers / capacity) + 0.3 × recent avg CPU (15-minute window; a node with no samples scores on load alone rather than being penalized for missing telemetry). Lower is better.
- Ordering: online before offline (an offline region match never beats an online node), requested-region matches before the rest, then score, then name (deterministic tie-break). Exactly one entry is `recommended` — even when every node is down there is still a "least bad" answer, and callers that must distinguish can read `online` themselves.
- Surfaced as `GET /api/v1/accounts/{id}/steer?region=` (read-tier, namespace-scoped like every other account read so an API key can't probe node load through an account it doesn't own) and as the subscription endpoint's default node choice.
- Creation-time placement (`CreateAccount`'s fan-out/least-loaded) is untouched — steering is about *which node to connect to right now*, not where peers get provisioned.

## Compatibility

- Older node agents keep working against the new control plane: they don't send `endpoint` (no device sightings from that node) and ignore `bandwidth_limit_mbps` (no shaping). Both features degrade to "not enforced on that node," never to broken peer sync.
- The containerized node (`deploy/node.Dockerfile`) already ships `iproute2` and runs with `NET_ADMIN`, so tc shaping works there unchanged; bare-metal nodes need `iproute2` (standard on Ubuntu/Debian).

## Definition of Done

- [x] Migrations 0014–0017 apply cleanly on a fresh database (all additive; 0016 backfills tokens for existing accounts).
- [x] `go build ./... && go vet ./... && go test ./...` green; new unit tests for the steering ranking (online/region/load/CPU/tie-break), the tc command plan (filtering, class/filter/police layout, burst floor), signature-includes-limit, token shape, path redaction, and conf-filename sanitization.
- [x] Frontend `tsc -b` + production build green; accounts UI exposes bandwidth (create + inline edit), devices (list, active count, exceeded badge, hard-enforce toggle), and the subscription URL (copy/rotate); nodes UI exposes region (create/edit/table).
- [x] `docs/openapi.yaml` covers the five new routes, the new account/node fields, and the previously-undocumented `PATCH /api/v1/nodes/{id}`.

## Known limitations / follow-ups

- tc shaping is IPv4-only (matches the panel's IPv4-only allocation today) and per-node — an account connected through two nodes at once can use its full rate on each. Cross-node aggregate rate limiting would need coordination the heartbeat model doesn't attempt.
- No rate limiting on `/api/v1/sub/*` lookups; the 192-bit token makes online guessing infeasible, but a per-IP limiter is cheap defense-in-depth once the bot-API rate-limiting story (PRD-telegram-bot-api §9) lands.
- Device identity is endpoint-based by construction (WireGuard has no device identity): CGNAT rebinding or roaming can look like extra devices. That's exactly why the PRD makes enforcement soft by default — the UI says so next to the hard-enforce toggle.
- Steering doesn't do GeoIP client localization; `?region=` is an explicit client/bot hint. An optional MaxMind-DB lookup (env-gated) is the natural next increment if automatic locality matters.
