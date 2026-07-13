# Story 5: Bot/Reseller API Key Authentication (HMAC-signed)

## Story

**As** a Telegram bot backend, **I want** to authenticate to the panel API with a scoped, HMAC-signed key instead of an admin login, **so that** the bot can create/manage/inspect accounts on its own, isolated from other bots and from the admin panel's session model — per `docs/PRD-telegram-bot-api.md` §5.

## Why this one now

Every account endpoint built in Story 3 is admin-JWT-only. That's a real gap against the project's stated purpose from message one: this panel exists specifically to sit behind a Telegram sales bot. Story 3 already left the seam for this — `accounts.owner_key_namespace` was deliberately omitted with a comment ("there's no bot API-key system yet to scope it to... add it then"). This is "then." It's also the more honestly-verifiable choice among the two next candidates: the alternative (agent-side `wgctrl` peer application) needs a real Linux WireGuard interface to verify against, which doesn't exist in this environment — this story is pure HTTP/crypto/DB, fully testable here with the same rigor as Stories 1-4.

## Design

- **`api_keys` table**: `key_id` (public identifier, sent as `X-API-Key`), `secret_encrypted` (AES-256-GCM). Unlike admin passwords, this secret can't be one-way hashed: HMAC verification requires the server to reconstruct the same signature, which needs the raw secret back, not just a comparison hash. Encrypted with `API_HMAC_MASTER_KEY` — a secret that's existed in `.env.example`/`config.go` since the very first PRD draft but was never actually wired to anything in code. Rather than adding yet another new env var next to it (which would repeat Story 3's one-key-per-purpose reasoning while leaving the old one dangling and unused), this story gives it its first real, precise purpose instead.
- **Signature**: `X-Signature: HMAC-SHA256(secret, method + "\n" + path + "\n" + timestamp + "\n" + body)`, checked in constant time. `X-Timestamp` (unix seconds) must be within ±5 minutes (`PRD-telegram-bot-api.md` §5.2) — this is what makes a captured request unreplayable after the window closes.
- **Scoping**: each key has `node_groups` (which nodes it may provision on) and `permissions` (`read`/`create`/`update`/`suspend`/`delete`). Accounts it creates are tagged `owner_key_namespace = key_id`; a key can only see/act on accounts in its own namespace — a request for another namespace's account returns `404`, not `403`, so existence isn't leaked (§5.2 explicitly). Admin JWT callers bypass namespace filtering entirely (they can see everything, by design).
- **Rotation**: rotating a key generates a new secret while keeping the old one valid for 24h (`previous_secret_encrypted` + `previous_secret_expires_at`) — signature verification tries the current secret, then the previous one if still within its grace window.
- **The account endpoints from Story 3 gain a second, alternative auth path** (`requireAdminOrAPIKey`) rather than being duplicated — the same handlers now serve both the admin panel and bot clients, exactly as `PRD-account-management.md` §1 requires ("one unambiguous account lifecycle... whether triggered from the admin UI or the bot API").

## Definition of Done

- [x] An admin can create an API key (returns `key_id` + the raw secret exactly once), list keys (secrets never re-exposed), and revoke one. Verified.
- [x] A correctly-signed request from that key can create/list/get/suspend/enable/renew/delete accounts, scoped to its own namespace and node groups. Verified with real computed HMAC-SHA256 signatures over real HTTP, not asserted in isolation.
- [x] A request signed with the wrong secret, a stale timestamp, or a tampered body/path is rejected `401`. All three verified independently.
- [x] A key without `delete` permission gets `403` on `DELETE`; a request for another key's (or the admin's) account gets `404`. Verified.
- [x] A key restricted to node group `eu` gets rejected trying to create an account on a node in group `us`. Verified.
- [x] A revoked key is rejected immediately; a rotated key's old secret keeps working until its grace period ends, then stops. Verified the full rotation lifecycle: new secret works, old secret still works within the grace window, then (grace period expired via direct DB manipulation, same technique as Story 2's token-TTL test) the old secret is rejected.
- [x] Verified against real Postgres, same rigor as Stories 1-4 — actual HMAC signatures computed independently in Python and sent over real HTTP.

### Note: `API_HMAC_MASTER_KEY` finally has a job

This secret existed in `.env.example`/`config.go` since the very first PRD draft, required but never consumed anywhere in the code - confirmed via `grep` before starting this story. It's now the AES-256-GCM key protecting `api_keys.secret_encrypted` at rest. Its generator in `install.sh` also needed a fix while wiring this up: it was using the generic 24-byte `random_secret()` helper, but AES-256-GCM needs exactly 32 bytes - the same class of issue `ACCOUNT_KEY_ENCRYPTION_KEY` had to get right in Story 3, caught here before it became a runtime decryption failure rather than after.

## Tasks
1. Wire the existing (previously-unused) `API_HMAC_MASTER_KEY` secret to actually do something: AES-256-GCM key for `api_keys.secret_encrypted`. Verify `install.sh` already generates it correctly (it does, as a generic secret — confirm it's 32 raw bytes like `ACCOUNT_KEY_ENCRYPTION_KEY`, or fix the generator if not).
2. Migration: `api_keys` table; `accounts.owner_key_namespace` column.
3. Store methods: `CreateAPIKey`, `GetAPIKeyByKeyID`, `ListAPIKeys`, `RevokeAPIKey`, `RotateAPIKeySecret`.
4. `requireAdminOrAPIKey` middleware: verifies either a JWT or an HMAC signature (trying current then previous secret within grace), attaches a common caller identity (namespace + permissions + node groups, or "admin" with none of those restrictions) to context.
5. Admin-only endpoints: `POST/GET /api/v1/api-keys`, `POST /api/v1/api-keys/{id}/revoke`, `POST /api/v1/api-keys/{id}/rotate`.
6. Wire the Story 3 account handlers behind `requireAdminOrAPIKey` instead of `requireAdmin`; add namespace filtering (`GetAccount`/`ListAccounts`/mutations all scoped when the caller is an API key) and per-action permission checks.
7. Node-group enforcement in `CreateAccount`: an API-key caller can only target (or auto-select within) nodes in its allowed groups.
8. Audit log entries for API key lifecycle actions, with the acting admin's identity.
9. Unit tests: signature verification (valid/wrong-secret/stale-timestamp/tampered-body), permission checks, rotation grace-period logic.
10. Docker-based smoke test: full bot-client flow with real computed signatures — cross-namespace `404`, missing-permission `403`, wrong node-group rejection, revoked-key rejection, rotation grace period.

## Out of scope (deferred)
- Per-key rate limiting (`PRD-telegram-bot-api.md` §9) — a distinct concern from authentication, later story.
- Webhooks (§8) — needs the events (expiry, quota threshold, node down) this story doesn't produce yet.
- Bulk endpoints (§6's `POST /accounts/bulk`) — the single-account endpoints are the priority; bulk is additive.
