# WGPanel

WGPanel is a self-hosted admin panel for running your own WireGuard VPN service. It manages accounts (peers), servers (nodes), and gives you a real-time view of usage, connectivity, and node health — all from one web dashboard.

- **Multi-node by default** — create an account once and it's usable across every server in your fleet, not pinned to a single node.
- **Live monitoring** — per-account traffic charts, node CPU/RAM history, and online/offline status for every connection.
- **Self-service domain & TLS** — change the panel's domain from the Settings page; Caddy provisions the certificate automatically, no restart needed.
- **Role-based admin accounts** — super admin, operator, and read-only support roles.
- **A scoped API** for bots/resellers to provision accounts programmatically (e.g. from a Telegram sales bot), kept completely separate from WireGuard/infrastructure logic.

## Requirements

- A Linux server (Ubuntu/Debian) with a public IP, for the panel itself.
- A domain name pointed at that server's IP, if you want automatic HTTPS.
- Docker is installed automatically by the installer if it's missing.

## Installing the panel

Run this on the server that will host the admin panel:

```bash
curl -fsSL https://raw.githubusercontent.com/iamfarhad/WGPanel/main/deploy/install.sh -o install.sh
sudo bash install.sh
```

The installer will ask for:

- **Panel domain** — must already point at this server's IP (used for automatic HTTPS via Caddy).
- **Admin e-mail** — used for Let's Encrypt certificate notices.
- **Super-admin username** — the account you'll use to log into the panel.
- Whether to also set up WireGuard **on this same server** as your first node (recommended — you can add more servers later).

When it finishes, your panel is live at `https://<your-domain>`. The auto-generated super-admin password is printed once at the end of the install — save it immediately, since it's never shown again (you can also recover it later with `wgpanel show-bootstrap-admin`, as long as the container's log history hasn't rotated it out).

## Adding more WireGuard servers

Every node — including the panel's own server — is added the same way:

1. In the panel, go to **Nodes → New node** and fill in its name, capacity, and WireGuard subnet.
2. Click **Join token** to generate a one-time token.
3. On the new server, run:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/iamfarhad/WGPanel/main/deploy/install-node.sh -o install-node.sh
   sudo bash install-node.sh
   ```

4. Paste in the control-plane address and the join token when prompted.

The node will appear as **online** in the panel within a few seconds, and every existing account automatically gets a peer on it — no manual sync step.

> Re-registering a node's agent (rebuilt server, replaced hardware)? Generate an **unlimited** join token instead of a normal one (checkbox in the Join token dialog) so you don't have to reset anything manually.

## Managing the stack

The installer places a `wgpanel` CLI on the panel server:

```bash
wgpanel status               # container + API health
wgpanel logs [service]       # tail logs
wgpanel backup               # dump the database + .env to deploy/backups
wgpanel update                # backup, pull latest images, redeploy, auto-rollback on failed health check
wgpanel doctor                # disk space, container/DB/Redis health, TLS cert expiry, backup freshness
wgpanel create-admin          # add another admin account
```

Run `wgpanel` with no arguments for the full command list.

## Using the panel

- **Accounts** — create a WireGuard account, set a data quota/device limit/expiry, and download the `.conf` or scan the QR code from any device it has a peer on.
- **Nodes** — see live status, edit capacity/endpoint, and view CPU/RAM history for each server.
- **Dashboard** — fleet-wide stats: nodes online, accounts online, active/suspended counts, total data transferred.
- **Settings** — panel defaults (quota, device limit, node capacity) and live domain/TLS management.
- **API Keys** — issue scoped, HMAC-signed keys for bots/resellers to create and manage accounts via the API.

## For developers

Architecture notes, the full API reference, and design docs for each feature area live in [`docs/`](docs) — start with [`docs/openapi.yaml`](docs/openapi.yaml) for the API surface.
