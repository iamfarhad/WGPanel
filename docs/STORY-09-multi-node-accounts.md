# Story 9: Real Peer Sync, Multi-Node Accounts, and Core-Server-as-a-Node

## Story

A user reported a "critical issue": `CreateAccount` required at least one WireGuard node to already be registered before an account could be created at all - a fresh single-server install had nowhere to put a first account. They wanted two things: (1) the panel's own server should also act as a WireGuard node, self-registered during install, so a single-server install works immediately; (2) an account should not be pinned to one node - the same account should get a peer on every eligible node, staying synced as nodes are added later.

Investigating surfaced a deeper gap underneath the reported symptom: **no code anywhere in this project had ever pushed a WireGuard peer onto a real interface.** `cmd/agent/heartbeat.go`'s only use of `wgctrl` was a read-only peer count. Every account ever created was a database row plus a downloadable client config - nothing a client generated from that config could actually connect to, because the server side never configured the matching peer. This had to be built regardless of the single-node/multi-node question, since neither model does anything real without it.

This story landed in three parts, in dependency order: (A) real peer application via `wgctrl`, (B) the multi-node account data model, (C) core-server self-registration.

## Part A: real peer application

`internal/httpapi/agentserver.go`'s `handleAgentHeartbeat` now computes the full desired peer list for a node on every heartbeat (`store.ListDesiredPeersForNode` - every `account_peers` row for that node whose account is `active`) and returns it in the response (`peers: [{public_key, allowed_ips}]`, alongside the existing `status: "ok"`). `cmd/agent/heartbeat.go` applies it via `wgctrl.Client.ConfigureDevice(iface, wgtypes.Config{Peers: desired, ReplacePeers: true})` - matching `wg syncconf` semantics (the list IS the complete membership; unchanged peers aren't re-handshaked). No manual diff against the interface's current peers first; `ReplacePeers` makes that unnecessary.

This is also how suspend/delete enforcement happens now: a suspended or deleted account simply stops appearing in the next heartbeat's desired-peer computation, and the agent's next `ConfigureDevice` call drops it - within one ~10s heartbeat interval, no separate "tear down this peer" call needed anywhere.

A missing WireGuard interface (host just rebooted before `wg-quick@wg0` ran, or - the normal case in this dev sandbox - no real interface exists at all) is tolerated the same way the pre-existing peer-count read already was: log a warning and retry next heartbeat, don't crash the loop. `deploy/install-node.sh`'s systemd unit gained an explicit `After=`/`Requires=wg-quick@<iface>.service` ordering dependency so this stays the rare case rather than the routine one on every reboot.

## Part B: multi-node accounts

New table `account_peers` (migration `0010`) replaces `accounts.node_id`/`assigned_ip` - one row per (account, node) with its own allocated IP. The account's WireGuard keypair itself stays on the `accounts` row, shared across every node it has a peer on - WireGuard peers are identified by public key, so the same client key is valid as a peer on any number of independent servers simultaneously.

`CreateAccount` no longer resolves one node. By default (`node_id` omitted or `"auto"`), it gives the account a peer on every currently eligible node (`status IN ('registered', 'online')`, node_group-restricted for a scoped API key). Passing an explicit node UUID still pins the account to just that one, for the cases that need it. A single full node is **skipped, not fatal** - only zero successfully provisioned nodes fails the whole call. Node rows are locked in `ORDER BY id` before `FOR UPDATE` so two concurrent fan-out creates can never deadlock against each other by acquiring locks in different orders.

`RedeemJoinToken` now calls `BackfillAccountPeersForNode` on success - every currently-`active` account without a peer on that node yet gets one. This is what keeps the promise for nodes added *after* an account already exists, not just at creation time.

`GET /api/v1/accounts/{id}/config` gained a `?node_id=` query param (an account can have more than one peer now): defaults to the only peer if there's exactly one, otherwise 400s with the list of valid node ids rather than guessing. The account response gained a `peers` array; the old singular `node_id`/`assigned_ip` fields are kept as deprecated, computed aliases (mirroring the first peer) for one release's worth of bot-integration compatibility.

### A design review caught real bugs in the first draft before any of this was written

A Plan-mode review (reading the actual pre-existing code, not just the proposal) found several concrete correctness issues, all fixed before implementation:
- The original draft hand-diffed the interface's current peers against the desired set. `wgtypes.Config{ReplacePeers: true}` does this correctly and more cheaply - no need to read the device first, and it's what `wg syncconf` already does.
- A `UNIQUE(node_id, assigned_ip)` constraint on `account_peers` would have silently broken the existing 24h IP-reuse hold (`SoftDeleteAccount` never clears `assigned_ip`) the first time an IP was actually reused - a bug that wouldn't have surfaced until well after rollout. Dropped in favor of the same query-based availability check the single-node model already used.
- The original draft made the fan-out `CreateAccount` transaction all-or-nothing across every eligible node, meaning one full/undersized node would block onboarding on the entire fleet. Changed to skip full nodes individually.
- Missing `ORDER BY id` on the node-locking query would have been a textbook lock-ordering deadlock between concurrent creates.

## Part C: core server as a self-registered node

New internal-only endpoint `POST /internal/nodes/bootstrap-self` (gated by `X-Internal-Token`, same tier as `/internal/admins`) combines `CreateNode` + join-token generation into one call. `backend/Dockerfile`'s build stage now also compiles `./cmd/agent` and copies the resulting binary into the same final distroless image (never executed there - only ever extracted via `docker create`+`docker cp`, which also sidesteps `install-node.sh`'s existing architecture-matching problem, since Docker itself already resolved the correct image for the host).

`deploy/install.sh` gained `setup_self_node`, guarded by the same idempotency check `cmd/agent` itself already uses (skip entirely if `${AGENT_DIR}/state/node-id.txt` exists) - re-running the installer never creates a duplicate node for the same box. It prompts for the WireGuard subnet/interface address/port, runs the same `wg genkey`/`wg-quick up` steps `install-node.sh` uses, extracts the agent binary, calls `bootstrap-self`, and installs the same systemd unit shape. Run inside a `set +e` subshell in `main()` with each internal step explicitly `|| return 1`-guarded - this is a nice-to-have layered on an already-fully-working panel, and a failure partway through (a `docker cp` hiccup, `wg genkey` failing) should stop just that flow, not abort the whole installer this late after the panel and its first admin already exist.

Residual trade-off worth stating plainly: this puts a privileged (`CAP_NET_ADMIN`), customer-traffic-terminating WireGuard interface on the same host as Postgres/Redis/JWT secrets/the admin panel. That's what was asked for and is reasonable for a single-server deployment, but it's a real change in blast radius from a compromised WireGuard/kernel path, not something to leave implicit.

## Definition of Done

- [x] Real peer application verified against a real Docker stack with real `cmd/agent` processes: heartbeat responses carry the exactly-correct desired-peer list, computed from real `account_peers` rows.
- [x] Creating an account with no `node_id` fanned out to exactly the currently-registered nodes (2 of 3 test nodes - the third was still `pending`), each with a distinct IP from its own subnet.
- [x] Registering a third node *after* the account existed triggered a real backfill - its very first heartbeat already showed `desired_peers: 1` for the pre-existing account, confirmed via the API afterward.
- [x] Suspending the account dropped its desired-peer count to 0 on the very next heartbeat across all three nodes, confirmed from real agent process logs - no explicit teardown call anywhere.
- [x] `GET /accounts/{id}/config` with 3 peers and no `node_id` returned `400 node_id_required` listing all three valid ids; with an explicit `node_id` it returned that specific node's correct config (own IP, own endpoint, own public key).
- [x] `POST /internal/nodes/bootstrap-self` verified directly: creates a node + returns a working join token in one call; rejected with `401` when the internal token is missing or wrong.
- [x] `cmd/agent` cross-compiles cleanly with `CGO_ENABLED=0` for linux/amd64 despite now importing `wgctrl`'s `ConfigureDevice` path (previously only used for read-only peer counting); the real `backend/Dockerfile` build was run (not just a local cross-compile) and both `wgpanel-api` and `wgpanel-agent` binaries were extracted from the resulting image via the exact `docker create`+`docker cp` sequence `install.sh` uses, confirming a real, correctly-arch-matched binary comes out.
- [x] Full existing 19-check `e2e-smoke.mjs` suite (updated for the new response shapes and UI) passes against a real Docker stack + real browser, including two new checks added for this story: the pin-to-one-node override and the default all-eligible-nodes fan-out, both verified through the actual UI, not just the API.
- [x] `docs/openapi.yaml` (+ synced `frontend/public/openapi.yaml`) updated for the `Account`/`CreateAccountRequest` schema changes, the new `peers`/`AccountPeer` schema, the `?node_id=` config parameter, and the new internal bootstrap endpoint; re-validated with `openapi-spec-validator` and re-confirmed rendering correctly in Swagger UI (31 operations).
- [x] `gofmt`/`go vet`/`go build`/`go test` and `tsc -b`/`vite build` all pass.

### An honest limitation, not swept under the rug

Real kernel-level (or userspace) WireGuard peer application could not be exercised end-to-end in this development sandbox: creating even a userspace `wireguard-go` interface on macOS requires root privileges this environment doesn't have (confirmed by installing `wireguard-go` and observing `operation not permitted` on interface creation, with and without explicit invocation flags). What *was* verified for real: the control-plane side computes the exactly-correct desired-peer list from real `account_peers` rows every heartbeat cycle, and the agent's `wgctrl.ConfigureDevice` call path is reached and fails only on the expected "interface does not exist" error - the same limitation `readPeerCount` (this project's pre-existing read-only peer count) already lived with on this host. The actual kernel/netlink peer-configuration call itself will need verification on a real Linux host with a real WireGuard interface, which is exactly the scope of the still-open, already-tracked roadmap item "End-to-end install.sh pass on a throwaway VM."

## Tasks

1. `internal/httpapi/agentserver.go` + `cmd/agent/heartbeat.go`: heartbeat response carries desired peers; agent applies via `wgctrl.ConfigureDevice`.
2. Migration `0010_account_peers.sql`; `internal/store/accounts.go` rewrite (`CreateAccount` fan-out, `BackfillAccountPeersForNode`, `ListAccountPeersWithNode`, `ListDesiredPeersForNode`); `internal/store/nodes.go`'s `RedeemJoinToken` calls the backfill.
3. `internal/httpapi/accounts.go` rewrite: `peers` response field, deprecated `node_id`/`assigned_ip` aliases, optional `node_id` on create, `?node_id=` on config.
4. `frontend/src/pages/AccountsPage.tsx` rewrite: "all eligible nodes" as the create-dialog default, per-peer config viewing in the detail dialog.
5. `internal/httpapi/nodes_bootstrap.go` (new `POST /internal/nodes/bootstrap-self`); `backend/Dockerfile` builds and ships `cmd/agent` too; `deploy/install.sh`'s `setup_self_node` and its helper functions; `deploy/install-node.sh`'s systemd unit gains the `wg-quick` ordering dependency.
6. `docs/openapi.yaml` + `frontend/public/openapi.yaml`; `e2e-smoke.mjs` updated for the new shapes plus two new checks.

## Out of scope (deferred)

- Real traffic-byte accounting (`data_used_bytes` still has no writer) - needs the heartbeat to actually report per-peer transfer stats, a distinct piece from peer *application* built here.
- Node deletion - `account_peers.node_id REFERENCES nodes(id) ON DELETE CASCADE` exists but nothing calls it yet; a real "remove a node" flow needs a drain step first, not a naive cascade delete.
- Verifying actual kernel-level peer configuration on a real Linux host - see the honest limitation above; folds into the existing "install.sh on a throwaway VM" roadmap item.
