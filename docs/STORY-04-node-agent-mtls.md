# Story 4: Node Agent — mTLS Registration & Heartbeat

## Story

**As** an operator who has run `install-node.sh` on a WireGuard server, **I want** the agent it installs to actually register with the control plane and report itself alive, **so that** a node's `registered`/`online`/`offline` status in the panel reflects real connectivity instead of being purely admin-asserted, per `docs/PRD-node-management.md` §5-6.

## A deliberate architecture change, made before writing any code

The README/PRDs said "gRPC (mTLS, agent-initiated, outbound)." Building that for real would need `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` — none of which are installed in this environment, and none of which any other part of this backend needed so far (every prior story deliberately favored stdlib over a new dependency: `net/http`'s router over chi/gin, a hand-rolled migration runner over `golang-migrate`). Everything this design actually needs from gRPC — agent-initiated outbound connections (NAT-friendly), mutual authentication, a small periodic payload — is fully served by plain **HTTPS with mutual TLS**, using nothing beyond `crypto/tls` and `net/http`. Pulling in a codegen toolchain to satisfy a diagram, when the diagram's real requirements don't need it, would be exactly the kind of speculative complexity avoided everywhere else in this codebase. Updated README/PRD-node-management.md/PRD-monitoring-stats.md to say HTTPS+mTLS instead — the properties those documents actually depend on (agent-initiated, NAT-friendly, mutually authenticated, periodic) hold either way.

## Design

The control-plane API becomes its own small internal Certificate Authority (CA) — this is the standard way to do agent fleet mTLS without depending on a public CA that can't issue certs for a private hostname.

- **CA**: an ECDSA P-256 keypair + self-signed cert, generated on first boot and persisted to the `/data` volume already mounted in `deploy/docker-compose.yml` (`wgpanel_api_data`). Also generates the control-plane's own server cert (signed by this CA, `DNSNames: ["wgpanel-control-plane"]` — a fixed internal name, not real DNS, matched explicitly by the agent's `ServerName` rather than the dial address, since the agent may dial an IP or any hostname the operator configured).
- **Registration** (`POST /agent/register`, plain TLS, no client cert required yet): the agent generates its own ECDSA keypair **locally — the private key never leaves the node** — builds a CSR, and submits `{join_token, csr_pem}`. The server redeems the join token (reusing `store.RedeemJoinToken` from Story 2 unchanged), and — critically — **does not trust the CSR's requested identity**: it signs a certificate with `CommonName` set to the real node UUID from the redeemed token, ignoring whatever the CSR asked for. Returns `{certificate_pem, ca_certificate_pem}`.
- **Heartbeat** (`POST /agent/heartbeat`, HTTPS+mTLS, client cert required): the node's identity comes from the verified client certificate's CN (the node UUID), cross-checked against a stored certificate fingerprint (so a node that gets re-registered invalidates any old cert still in use elsewhere). Body carries `{peer_count, load_avg}`. Updates `last_heartbeat_at` and flips status to `online`.
- **Offline sweep**: a background goroutine, ticking every 5s, flips any `online` node whose `last_heartbeat_at` is older than 30s to `offline` (matches the 30s figure already specified in `PRD-node-management.md` §6.1/§7).
- **Agent binary** (`cmd/agent`): reads the exact env vars `deploy/install-node.sh` already writes to `agent.env` (`WGPANEL_PANEL_ADDR`, `WGPANEL_JOIN_TOKEN`, `WGPANEL_NODE_NAME`, `WGPANEL_STATE_DIR`) — no changes needed to that script. Registers once, persists its cert/key/CA under `WGPANEL_STATE_DIR`, then heartbeats every 10s. Attempts to read a real peer count via `wgctrl`; falls back to 0 if no WireGuard interface exists (true in this dev environment, and true on a freshly registered node before any accounts exist) rather than failing.

## Definition of Done

- [x] A node in `registered` status (Story 2) can have an agent complete `POST /agent/register` with its join token, receiving a certificate whose CN is the real node UUID regardless of what its CSR requested. Verified with a real agent process against a real running control plane.
- [x] `POST /agent/heartbeat` without a client certificate is rejected; with a valid one, `last_heartbeat_at` updates and status flips to `online` within one heartbeat. Verified.
- [x] Stopping the agent and waiting past the 30s threshold flips the node to `offline` via the background sweep, with no manual action. Verified: killed the real agent process, waited past the threshold, confirmed both the DB row and the sweep's own log line (`nodes_marked_offline: 1`).
- [x] A stale/mismatched certificate (simulating a node that was somehow re-registered) is rejected on heartbeat even though it's signed by the same CA. Verified by overwriting the pinned fingerprint mid-flight and watching the *same still-running agent's* next scheduled heartbeat get rejected with its now-superseded certificate.
- [x] Verified against a real running control plane in Docker and a real agent process — not mocked TLS. Also cross-compiled the agent for `linux/amd64` and `linux/arm64` (the real deployment targets) to confirm it isn't accidentally darwin-only.

### Bug found and fixed during the smoke test

The control-plane container (`gcr.io/distroless/static-debian12:nonroot`) crash-looped on first boot: `mkdir /data/ca: permission denied`. Distroless's `nonroot` image has no shell, so there's no way to `chown` the volume mountpoint at container start the way a normal image could - and a Docker named volume inherits its initial ownership from whatever's already at that path *in the image* the first time it's created, not from anything done at runtime. Fixed in `backend/Dockerfile` by creating `/data` with the correct numeric ownership (`65532:65532`, distroless's fixed nonroot UID/GID) in the build stage, then `COPY --from=build --chown=65532:65532` into the final stage - no shell needed in the distroless layer, and the volume picks up correct ownership on first creation. Caught immediately by the smoke test rather than being a surprise in a real deployment.

## Tasks
1. `internal/nodeca`: CA generation/persistence (`/data/ca/`), CSR signing that overrides the requested Subject with the authorized node ID.
2. Migration: add `mtls_cert_fingerprint`, `last_heartbeat_at` to `nodes`.
3. Store methods: `RecordNodeCertFingerprint`, `RecordHeartbeat` (sets `online` + timestamp), `SweepOfflineNodes`.
4. Second HTTP server bound to `NODE_AGENT_PORT` with `tls.Config{ClientAuth: tls.VerifyClientCertIfGiven}`, serving `/agent/register` and `/agent/heartbeat`.
5. Background sweep goroutine, started from `cmd/api/main.go`.
6. `cmd/agent`: keypair/CSR generation, registration call, cert/key persistence, heartbeat loop with best-effort `wgctrl` peer count.
7. Unit tests: CA issues valid certs and honors the CN override; fingerprint mismatch rejected.
8. Docker + real-process smoke test: full register → heartbeat → online → (stop agent) → offline cycle, plus the rejected-without-cert and stale-fingerprint cases.

## Out of scope (deferred)
- Actually applying WireGuard peers via `wgctrl` on the node (this story is registration/liveness only) — natural next story once this plumbing is proven.
- Traffic stats piggybacked on the heartbeat (`PRD-monitoring-stats.md`) — the heartbeat body only carries `peer_count`/`load_avg` for now.
- `degraded` status, load-based auto-selection weighting beyond the simple accounts-count heuristic already in Story 3.
- Certificate rotation/revocation beyond "a new registration invalidates the old fingerprint."
