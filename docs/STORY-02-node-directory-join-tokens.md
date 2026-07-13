# Story 2: Node Directory & Join-Token API

## Story

**As** an admin, **I want** to register a WireGuard node in the panel and generate a join token for it, **so that** `install-node.sh` (already written) has something real to redeem, and later stories (account assignment, the actual Go node agent) have a node directory to build on.

## Why this one, and why not the full agent handshake yet

`docs/PRD-node-management.md` §5 describes the full registration flow: admin generates a token → agent redeems it → control plane issues an mTLS client certificate → node goes `online` on first heartbeat. Building real mTLS certificate issuance now, with no actual Go node agent to redeem it, would be speculative infrastructure with nothing to verify it against - the same trap Story 1 avoided by not building `/internal/*` protections nobody could yet exercise. This story implements the control-plane half only: node CRUD, token generation, and token redemption (marking a node `registered`). Real `online`/`offline` status via heartbeats is monitoring-pipeline work (`PRD-monitoring-stats.md`) for once an actual agent exists to send them.

## Prerequisite found while scoping this

Story 1 issues JWTs on login but nothing **verifies** one — there's no middleware protecting a route with "must be a logged-in admin." This story adds that (`requireAdmin`), since the new node endpoints need it and nothing built so far needed it before now.

## Definition of Done

- [x] A logged-in admin (Story 1's JWT) can create a node record, list nodes, and get one by id; an unauthenticated request to any of these is rejected `401`. Verified, including a dedicated unit test suite for `requireAdmin` (missing/malformed/wrong-secret token cases).
- [x] Generating a join token overwrites any previous one for that node and returns the raw token exactly once - only its SHA-256 hash is stored. Verified.
- [x] Redeeming a valid, unexpired, unused token transitions the node from `pending` to `registered`; redeeming an expired, already-used, or unknown token fails without changing node state. Verified all four cases end-to-end against a real Postgres: wrong token (401), correct token (200, flips to `registered`), same token replayed (401 - already consumed), and a genuinely expired token after waiting out a shortened TTL (401).
- [x] Every create/join-token-issued/redeemed action writes an `audit_log` row with the correct actor - `admin` for admin-triggered actions, `node-agent` for the redemption call itself (since that's not an admin-authenticated request). Verified by querying the table directly after the full flow.
- [x] Verified against real Postgres/Redis, the same way Story 1 was (not just unit tests) - full docker compose stack, real HTTP round-trips, cleaned up afterward.

## Tasks

1. `requireAdmin` middleware: parse `Authorization: Bearer <jwt>`, validate signature + expiry, attach admin id/role to request context; reject with `401` otherwise.
2. `nodes` table migration: id, name, node_group, public_endpoint, wg_subnet, capacity_max_peers, status, `join_token_hash`, `join_token_expires_at`, created_at.
3. Store methods: `CreateNode`, `GetNode`, `ListNodes`, `SetJoinToken`, `RedeemJoinToken` (atomic: check hash match + not expired + still `pending`, then flip to `registered`, in one transaction to avoid a race between two redemption attempts).
4. `POST /api/v1/nodes` (admin-only): create a node, status starts `pending`.
5. `GET /api/v1/nodes`, `GET /api/v1/nodes/{id}` (admin-only).
6. `POST /api/v1/nodes/{id}/join-token` (admin-only): generate a random token, store only its SHA-256 hash + expiry (`NODE_AGENT_JOIN_TOKEN_TTL_MIN`), return the raw token once.
7. `POST /api/v1/nodes/join` (no admin auth - the token *is* the credential): redeem a token, flip node to `registered`.
8. Audit log entries for all of the above.
9. Unit tests: `requireAdmin` (valid/expired/malformed JWT), join-token redemption (happy path, reuse, expiry, wrong token).
10. Docker-based smoke test mirroring Story 1's rigor: real Postgres/Redis, full HTTP round-trip including the failure cases.

## Out of scope (explicitly, deferred to later stories)
- mTLS certificate issuance and the actual Go node agent binary / gRPC heartbeat stream - `PRD-node-management.md` §5-6, once an agent exists to test against.
- Node health/load-based auto-selection - needs real heartbeats first.
- Account (peer) CRUD itself - `PRD-account-management.md`, next story after this one; accounts reference a node id, so this story needs to land first.
