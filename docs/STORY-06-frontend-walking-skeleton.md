# Story 6: Admin Panel Frontend — Walking Skeleton

## Story

**As** an admin, **I want** a real web UI to log in and see the nodes registered in the panel, **so that** there's an actual frontend proving the backend built across Stories 1-5 is reachable and usable by something other than `curl` — per `docs/PRD-admin-panel-ux.md`.

## Scope decision

`PRD-admin-panel-ux.md` describes a full admin panel: dashboard, accounts, nodes, API keys, admin users, audit log, real-time updates, RTL support. Building all of that in one pass would repeat the mistake this project has avoided everywhere else — shipping a large surface with only shallow verification. This story is the frontend's Story 1: **login + a protected shell + one real data view (nodes)**, proven against the actual running backend, not a mock. Accounts, API keys, and the rest follow as their own stories once this foundation is real.

## A backend gap found while scoping this

Story 1 issues an access token (15 min) and a refresh token (Redis-backed) on login, but **no endpoint ever consumes the refresh token** — there was no `POST /api/v1/auth/refresh`. A real SPA can't function on a 15-minute hard session limit with no renewal path, so this story adds that endpoint first. Kept intentionally simple: it validates the refresh token against Redis and issues a new access token; it does not rotate the refresh token itself (avoids a race between concurrent tabs/requests using the same token to refresh simultaneously — a real concern, but one that needs its own design, not a rushed addition here).

## Stack

Per the README's Tech Stack table: React + TypeScript + Vite. Tailwind for styling. A **small number of hand-written UI primitives** (button, input, card, table) rather than running the `shadcn/ui` CLI — that CLI fetches component source from a registry at generation time, an external dependency this story doesn't need just to get a login form and a table on screen. Full shadcn/ui adoption (if wanted) is a cosmetic follow-up, not a functional one. TanStack Query for data fetching, `react-router-dom` for routing.

## Definition of Done

- [x] `POST /api/v1/auth/refresh` exists, verified against real Redis: a valid refresh token yields a new access token; tested directly with curl before the frontend ever touched it.
- [x] The frontend's login page authenticates against the real `POST /api/v1/auth/login` and lands on a protected dashboard shell.
- [x] The Nodes page fetches `GET /api/v1/nodes` with the real access token and renders actual node data from a real running backend (not fixture/mock data).
- [x] An unauthenticated visit to a protected route redirects to login.
- [x] `tsc -b` and `vite build` both pass cleanly, including inside the Docker build itself.
- [x] Verified end-to-end with an actual browser (see below), not just a manual claim: unauthenticated redirect, login, real node data rendering, session survival across a reload (the refresh token doing its job), and logout.

### Browser verification note: Playwright's bundled Chromium download was unreliable here

`npx playwright install chromium` repeatedly stalled/timed out downloading from `storage.googleapis.com` in this environment (two attempts, ~15 minutes combined, one with `--with-deps` that appears to hang entirely on macOS since that flag targets Linux system package managers). Rather than keep retrying an external download, switched to driving the **already-installed system Google Chrome** via Playwright's `channel: 'chrome'` option, which needs no separate browser download. The resulting `frontend/e2e-smoke.mjs` script drove a real Chrome instance through the full flow against the real dev server and real backend - genuine browser verification, just via a different browser acquisition path than the default.

### A design decision worth surfacing: the frontend proxies `/api`, no CORS needed

The Vite dev server proxies `/api/*` to the real backend (`vite.config.ts`), exactly mirroring how Caddy routes the same path in production. This means the Go backend needed zero CORS middleware added for local development - the browser always sees same-origin requests, in dev and in production alike. Confirmed this works end-to-end (not just in theory) via the dev-server proxy successfully forwarding a real login request to the real backend.

## Tasks
1. `POST /api/v1/auth/refresh` backend endpoint + unit/smoke coverage.
2. Vite + React + TS scaffold (`frontend/`), Tailwind configured, hand-written `components/ui` primitives.
3. `lib/api.ts`: fetch wrapper attaching the bearer token, redirecting to login on a 401 it can't refresh past.
4. `lib/auth.tsx`: auth context (login/logout, in-memory access token, refresh token in `localStorage`, auto-refresh attempt on load).
5. Routing: `/login` (public), `/` dashboard shell with a sidebar, `/nodes` (protected) - `ProtectedRoute` wrapper.
6. `NodesPage`: TanStack Query fetch of `GET /api/v1/nodes`, rendered as a table (name, group, status, capacity).
7. `frontend/Dockerfile` (multi-stage: Node build → nginx) + `build: ../frontend` override in `deploy/docker-compose.yml`, matching the pattern already used for `api`.
8. Verification: `tsc --noEmit`, `vite build`, then a real backend + real dev server + browser-automated login/nodes-list check.

## Out of scope (deferred)
- Accounts, API Keys, Admin Users, Audit Log screens — `PRD-admin-panel-ux.md` §3.2-3.7, later stories once this shell is proven.
- Real-time (WebSocket) status updates — §4, needs a backend push mechanism that doesn't exist yet.
- RTL/Farsi localization — §5's acceptance criteria, a follow-up once the base screens exist to localize.
- Refresh-token rotation - noted above, deferred pending its own design.
