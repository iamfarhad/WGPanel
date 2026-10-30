# Marzban-compatible API

Iranian sellers pick panels that the popular Telegram sales bots support. Mirza panel
([botmirzapanel](https://github.com/mahdiMGF2/botmirzapanel)), Faoxima and ShopVPN speak
the REST API of [Marzban](https://github.com/Gozargah/Marzban). With this facade switched
on, a bot configured with panel type **Marzban** works against Jirjirak WireGuard
unmodified.

It is a translation layer over the normal account logic, not a second implementation:
quota and reseller caps, "enable refuses an account that used its whole quota", renewal
semantics, subscription-token rotation, events/webhooks and the audit log are the ones
`/api/v1/accounts` uses. Code: `backend/internal/httpapi/marzbancompat_*.go`.

## Turn it on

Settings → **Sales bot compatibility (Marzban API)** (super admin). Off by default; while
off every route below answers `404 {"detail":"Not Found"}`, as if it did not exist.
Setting: `panel_settings.marzban_compat_enabled` (migration 0031); API
`GET|PUT /api/v1/marzban-compat`.

There is no per-key switch: revoking an API key (or disabling the reseller owning it)
already cuts that bot off at its next request.

## Authentication

| Marzban says | We map it to |
|---|---|
| admin username | the API key's **key ID** |
| admin password | the API key's **secret** |
| `POST /api/admin/token` (OAuth2 password form) | checks the secret, returns `{"access_token", "token_type":"bearer"}` |
| `Authorization: Bearer …` | the key's scoping: namespace, permissions, node groups, per-key rate limit |

Why the raw secret can be checked: keys store their secret *encrypted* (the HMAC scheme
needs it to recompute signatures), not hashed, so no second credential is needed. A secret
rotated within the last 24 hours still logs in, exactly as for HMAC.

The token is a 24 h HS256 JWT signed with a key derived from (not equal to) the admin JWT
secret, so it is useless against the admin API and vice versa. It is only a pointer: every
request re-loads the key and rebuilds its identity with the same code the HMAC scheme
uses. Consequences: a revoked key, a rotated secret (tokens minted under the old secret
die), a disabled or expired reseller, or the switch going off stop working immediately,
not at token expiry. The per-key token bucket (Settings → Bot API rate) is shared between
HMAC and Marzban calls. Token requests are additionally limited per source IP.

Permissions required (a key without one gets `403`):

| Route | Permission |
|---|---|
| `GET` routes, `/api/system`, `/api/inbounds` | `read` |
| `POST /api/user` | `create` |
| `PUT /api/user/{u}`, `/reset`, `/revoke_sub`, `/active-next` | `update` |
| status change inside `PUT` (`active`/`disabled`) | also `suspend` |
| `DELETE /api/user/{u}`, `DELETE /api/users/expired` | `delete` |
| `GET /api/admin` | any valid key |

## Endpoints

| Endpoint | Notes | Used by Mirza |
|---|---|---|
| `POST /api/admin/token` | form `username`, `password`; 401 `Incorrect username or password` | login (cached 1 h) |
| `GET /api/admin` | `{username (key id), is_sudo:false, users_usage…}` | |
| `POST /api/user` | create; 200; 409 `User already exists`; 422 on bad username | buy |
| `GET /api/user/{username}` | 404 `User not found` | status, renew checks |
| `PUT /api/user/{username}` | partial update, see below | renew, extra volume |
| `DELETE /api/user/{username}` | `{}` | remove |
| `POST /api/user/{username}/reset` | zero the usage | before renew |
| `POST /api/user/{username}/revoke_sub` | new subscription token | revoke link |
| `GET /api/user/{username}/usage` | one `Master` entry = usage since last reset | |
| `GET /api/users` | `offset, limit, username (repeatable), search, status, sort`; `{"users","total"}`; default and max `limit` 1000 | |
| `GET`/`DELETE /api/users/expired` | `expired_after`, `expired_before` (datetime or unix) | cleanup |
| `POST /api/user/{username}/active-next` | always 404 `User doesn't have next plan` | |
| `GET /api/system` | `version` is `0.8.4` (see below), user counts, zeros for host stats | version probe |
| `GET /api/inbounds` | `{"wireguard":[{"tag":"wireguard",…}]}` | tag picker |
| `GET /api/user-templates`, `GET /api/nodes` | `[]` | |

Errors: `{"detail": "text"}` for 401/403/404/409/429 (`WWW-Authenticate: Bearer` on 401),
and `422 {"detail":[{"loc":["body","field"],"msg":…,"type":…}]}` for validation.
Marzban's own username rule applies (3-32 leading word characters, `a-zA-Z0-9-_@.`,
at most 34 characters).

The version is deliberately `0.8.4`: Mirza switches to Marzban's newer *groups* API
(`/api/group`, `/api/cores`, `group_ids`) above that version, which we do not have.

## Field mapping

| Marzban | Account |
|---|---|
| `username` | `external_ref` (`label` is set to it on create) |
| `status` | derived: `active`; `disabled` (any manual/abuse/device/reseller suspension); `limited` (suspended `quota_exceeded`); `expired` (suspended `expired`, or past `expiry_at`); `on_hold` (waiting for first handshake) |
| `expire` (unix s, 0 = never) | `expiry_at` |
| `data_limit` (bytes, 0 = unlimited) | `data_quota_bytes` |
| `data_limit_reset_strategy` | `reset_strategy` (`no_reset` ↔ `none`; `day`, `week`, `month`, `year`) |
| `on_hold_expire_duration` (s) | `hold_duration_days`, **rounded to whole days** (min 1) |
| `on_hold_timeout` | `hold_deadline_at` |
| `used_traffic`, `lifetime_used_traffic` | `data_used_bytes` (no separate lifetime counter) |
| `created_at`, `online_at` | `created_at`, newest `last_handshake_at` (naive UTC ISO, like Marzban) |
| `subscription_url` | the public subscription page `/s/{token}`, absolute, on the **subscription domain** when set, else the panel domain |
| `links` | one per-node config URL (`/api/v1/sub/{token}?node_id=…`) |
| `proxies`, `inbounds` | accepted and ignored; echoed as `{"wireguard":{}}` / `{"wireguard":["wireguard"]}` |
| `note` | accepted, **not stored** (returned `null`) |
| `sub_updated_at`, `sub_last_user_agent`, `next_plan`, `admin` | `null` |

`subscription_url` is checked against the regexp Mirza applies; if the origin is an IP or
`localhost` (which the regexp rejects and the bot would then corrupt by prefixing its panel
URL) a relative `/s/{token}` is returned instead.

### How `PUT /api/user/{username}` is applied

Only present, non-null fields change (as in Marzban). In order:

1. `expire` > 0: through the renewal path, so an **expired account is revived**; `0`
   clears the expiry; for an on-hold account it activates it with that fixed date.
   (An on-hold account ignores `expire: 0`.)
2. `data_limit` (`0` = unlimited), `data_limit_reset_strategy`, `on_hold_expire_duration`:
   one update; raising the cap above the usage lifts a `limited` state.
3. `status`: `disabled` suspends (reason `manual`); `active` enables, refused with `409`
   when the account has used its whole quota. `active` on an on-hold account is a no-op.

The steps are separate transactions: a failure midway (e.g. a reseller's data credit)
leaves earlier steps applied and returns the error; a retry is idempotent.

## Set up Mirza panel

1. Settings → turn on **Sales bot compatibility (Marzban API)**.
2. API Keys → **New API key** with permissions `read, create, update, suspend, delete`
   (and optionally node groups to restrict where the bot's users are placed). Copy the
   secret now; it is shown once. A reseller can do this for a key that acts as the
   reseller, with the reseller's limits.
3. In the bot's admin menu add a panel: type **Marzban**; URL = the **panel** URL
   (`https://panel.example.com`, no trailing slash, no `/api`); username = the **key ID**;
   password = the **secret**. The page API Keys → *Connect a sales bot* shows the URL.
4. Leave inbounds/proxies at defaults. Add products (volume in GB, time in days) as usual.
5. Test: buy a test account, then check Accounts: it appears with the username the bot
   chose, owned by the key.

The panel URL must be the panel domain: the separate subscription domain only serves
`/api/v1/sub/*` and the `/s/*` page (see `internal/caddyadmin`), not the API. Caddy
already proxies `/api/*` to the Go API (`deploy/Caddyfile`), and nothing here collides with
`/api/v1`.

## Not supported / differences

- **Soft-deleted usernames stay reserved**: a deleted user cannot be re-created under the
  same username (`409 User already exists`), because `external_ref` is globally unique and
  kept after delete. Bots that generate random usernames never notice.
- `note`, user templates, nodes, groups (`/api/group(s)`, `/api/cores`), `next_plan`
  and `active-next`, admin management, `/api/users/reset`, usage per node or per time
  window, `online_at` granularity beyond the last handshake, `sub_updated_at`.
- Switching an *existing* user to `on_hold` (create it on hold instead).
- Marzban protocol links (`vless://…`): this panel sells WireGuard, so `links` holds
  per-node config URLs and the customer is meant to open `subscription_url`.
- `/api/users` filters in memory over the key's accounts: fine for thousands, not
  designed for hundreds of thousands.
- Host figures in `/api/system` (memory, CPU) are zero; traffic is the key's total usage.
- Bots that only accept `vless/vmess/...` links in `links` cannot show a config inline.
- Faoxima and ShopVPN were not run against it; they use the same Marzban endpoints
  (token, user CRUD, reset, revoke_sub, system), which are covered here by contract tests.
  Differences in how they read optional fields would show up as a bug report.

## Tests

`marzbancompat_test.go` (unit) and `marzbancompat_integration_test.go` (needs
`WGPANEL_TEST_POSTGRES_DSN`): replays the Mirza call sequence (token, create, get, modify
with renew and extra volume, reset, revoke_sub, disable/enable, delete) and checks every
other endpoint, the error shapes, namespace and permission scoping, and the revoke /
rotate / switch-off behaviour of issued tokens.
