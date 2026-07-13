# Story 3: Account (Peer) CRUD

## Story

**As** an admin, **I want** to create, inspect, suspend/enable, renew, and delete WireGuard accounts against a registered node, **so that** there's a real account lifecycle to build the bot API and frontend against, per `docs/PRD-account-management.md`.

## Scope decisions made before writing code

- **No real WireGuard peer application yet.** There's no Go node agent/gRPC/wgctrl to actually push a peer onto a live interface (that's a later story). This story implements the control-plane's view of an account faithfully — real keypairs, real IP allocation, real state machine — but "applying" a peer to a node is a no-op today. Accounts go straight from create to `active` rather than sitting in a genuinely meaningful `pending` state, since there's no agent that could ever confirm it. Modeling a timeout-to-`provisioning_failed` around a confirmation that can never arrive would be pretending to test something nothing will ever produce.
- **No bot API-key/HMAC auth yet** (`PRD-telegram-bot-api.md` §5 is its own story). These endpoints are admin-JWT-protected (`requireAdmin` from Story 2), same as node management.
- **Quota/expiry enforcement is check-on-read, not a background job.** `data_used_bytes` has no real writer yet (that's the monitoring-stats pipeline, a later story) so quota enforcement is implemented and testable but inert until then. Expiry enforcement *is* fully real (`expiry_at` is set at creation time), implemented as a single `UPDATE ... WHERE expiry_at <= now()` run before every read - correct for any account someone actually looks at, but an account nobody queries won't flip to `suspended` until it is queried. A real background sweep (or the monitoring pipeline, once it exists) should replace this; noted here rather than silently left as a gap.
- **QR code image generation is deferred.** `/accounts/{id}/config` returns the `.conf` text (real keys, real assigned IP, the node's real endpoint) - adding an image-generation dependency for a QR code is a small, separable addition once this core exists, not bundled in here.

## Definition of Done

- [x] Creating an account against a `registered` node with capacity generates a real Curve25519 keypair (private key encrypted at rest with a dedicated key, never logged), allocates the next free IP in that node's subnet, and returns `active` immediately. Verified.
- [x] Creating against a node with no remaining capacity fails `409 node_capacity_exceeded` without allocating anything. Verified with a `/29` (capacity 2) node - the 3rd account was correctly rejected.
- [x] `node_id: "auto"` picks the `registered` node with the fewest non-deleted accounts; if none have capacity, fails the same way. Verified.
- [x] `GET /accounts/{id}/config` returns a valid `wg-quick`-style config using the account's real public/private key and the node's `public_endpoint`. Verified - decrypted the real private key, matched the account's real assigned IP and the node's real public key/endpoint.
- [x] Suspend/enable/renew/delete all work and are audit-logged; renew clears a `quota_exceeded`/`expired` suspension and reactivates but leaves a `manual` suspension untouched. Verified all four transitions, including a real (not simulated) expiry-triggered suspension via `reconcileExpiry` and its renewal-triggered reactivation.
- [x] Deleting an account releases its IP only after a 24h hold - verified via the stored `ip_release_at` timestamp (`> now() + 23h`) rather than actually waiting 24h.
- [x] Verified end-to-end against real Postgres, same rigor as Stories 1-2.

### Bug found and fixed during the smoke test

`store.InsertAuditLog`'s `detail` parameter was a plain Go `string` inserted via `NULLIF($4, '')::jsonb` - callers were expected to hand it pre-formatted JSON text. `handleSuspendAccount` passed the bare word `manual` instead of `"manual"` (i.e. not valid JSON), so the cast failed. The handler caught that error and only logged it - **the suspend request still returned 200, but its audit-log row silently never existed.** Caught by grepping container logs for `audit_log_failed` during the smoke test, not by anything that would have shown up in a unit test in isolation.

Fixed at the root rather than patching the one call site: `InsertAuditLog` now takes `detail any` and marshals it with `encoding/json` internally, so a non-JSON Go value literally cannot reach the query as malformed text - the bug class is closed, not just this instance of it. All ten call sites across `accounts.go`, `nodes.go`, `admins.go`, and `auth.go` were updated (`nil` for "no detail", a `map[string]string` for the one case that needed structured data). Re-ran the full smoke test against the fixed image and confirmed the audit row now exists with `detail = {"reason": "manual"}`.

## Tasks

1. Add a dedicated `ACCOUNT_KEY_ENCRYPTION_KEY` secret (env + `install.sh` + `docker-compose.yml` + `config.go`) - deliberately separate from `API_HMAC_MASTER_KEY`, which is reserved for bot-API request signing (a different purpose) once that story lands. Reusing one secret across unrelated cryptographic purposes is the kind of shortcut worth avoiding on principle, not just in the cases that happen to bite.
2. `wgkeys` package: generate a real Curve25519 keypair (`golang.zx2c4.com/wireguard/wgctrl/wgtypes`), AES-256-GCM encrypt/decrypt the private key with the new key.
3. IP allocator: pure function taking a CIDR + already-allocated IPs, returning the next free host address (skipping network/broadcast and `.1`, reserved for the node's own WireGuard address) - unit-testable without a database.
4. `accounts` table migration, matching the PRD-account-management.md §4 data model (minus fields that depend on not-yet-built pipelines, e.g. no `owner_key_namespace` yet since there's no bot API key system to scope it to).
5. Store methods: `CreateAccount` (keypair + IP allocation + capacity check, in one transaction), `GetAccount`/`ListAccounts` (each preceded by the expiry reconciliation `UPDATE`), `UpdateAccount`, `SetStatus` (suspend/enable with reason), `RenewAccount` (add quota/extend expiry + conditional reactivation), `SoftDeleteAccount`.
6. HTTP handlers: `POST/GET /api/v1/accounts`, `GET /api/v1/accounts/{id}`, `PATCH /api/v1/accounts/{id}`, `POST .../suspend`, `POST .../enable`, `POST .../renew`, `DELETE /api/v1/accounts/{id}`, `GET .../config`.
7. Audit log entries for every lifecycle action.
8. Unit tests: IP allocator (collision avoidance, exhausted subnet), keypair encrypt/decrypt round-trip.
9. Docker-based smoke test: full lifecycle including capacity-exceeded rejection, auto node selection, renew-reactivates-suspended, and config text sanity-check.
