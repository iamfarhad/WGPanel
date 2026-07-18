#!/usr/bin/env bash
#
# WGPanel - Main panel installer (control-plane API + Postgres/Timescale + Redis + frontend + Caddy)
# Run this on the server that will host the admin panel and API (NOT on the WireGuard nodes
# themselves - those use install-node.sh).
#
# Usage:
#   sudo bash install.sh
#
set -euo pipefail

INSTALL_DIR="/opt/wgpanel"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
ENV_FILE="$INSTALL_DIR/.env"
SCRIPT_SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The README's documented install method is `curl .../install.sh -o install.sh &&
# sudo bash install.sh` - a single file, not a git clone - but setup_files() below
# needs docker-compose.yml/Caddyfile/.env.example/wgpanel sitting right next to this
# script. ensure_deploy_files (called from main, before anything reads
# SCRIPT_SOURCE_DIR) detects that mismatch and fetches those companion files from
# the same repo/branch into a temp dir, repointing SCRIPT_SOURCE_DIR there - a real
# bug hit verifying the README's own instructions on a fresh server, not a
# hypothetical.
REPO_RAW_BASE="https://raw.githubusercontent.com/iamfarhad/WGPanel/main/deploy"
DEPLOY_COMPANION_FILES=(docker-compose.yml Caddyfile .env.example wgpanel)

# The panel's own server can optionally run as this WGPanel's first WireGuard node,
# the same way install-node.sh sets up a remote one: a production Docker container
# (NODE_IMAGE) managed by compose under NODE_DIR (docs/STORY-09-multi-node-accounts.md).
NODE_DIR="/opt/wgpanel-node"
NODE_COMPOSE_FILE="$NODE_DIR/docker-compose.yml"
NODE_ENV_FILE="$NODE_DIR/.env"
WGPANEL_REPO_URL="${WGPANEL_REPO_URL:-https://github.com/iamfarhad/WGPanel.git}"

log()  { echo -e "\033[1;32m[wgpanel]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel]\033[0m $*" >&2; }

# --fresh / --reset wipes any prior install (containers, volumes, secrets) for a
# genuinely clean setup. Off by default so a normal re-run never destroys data.
FRESH=0
parse_args() {
  for a in "$@"; do
    case "$a" in
      --fresh|--reset) FRESH=1 ;;
      -h|--help)
        echo "Usage: sudo bash install.sh [--fresh]"
        echo "  --fresh   Remove any existing WGPanel stack, self-node, secrets, and data"
        echo "            volumes first, then install cleanly. DESTROYS the database."
        exit 0
        ;;
      *) err "Unknown argument: $a (try --help)"; exit 1 ;;
    esac
  done
}

# fresh_reset tears down a prior install so the run below starts from nothing. Runs
# only under --fresh, and only after Docker is available (it uses docker compose to
# remove the named volumes, which a plain `rm` can't touch).
fresh_reset() {
  [[ "$FRESH" -eq 1 ]] || return 0
  warn "--fresh: removing any existing WGPanel install (containers, volumes, secrets, self-node)."
  if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
    (cd "$INSTALL_DIR" && docker compose down -v --remove-orphans 2>/dev/null) || true
  fi
  if [[ -f "$NODE_COMPOSE_FILE" ]]; then
    (cd "$NODE_DIR" && docker compose down -v --remove-orphans 2>/dev/null) || true
  fi
  # Belt-and-suspenders for a stack whose compose file is already gone but whose
  # volumes/containers linger from an older layout.
  docker ps -aq --filter "name=^/wgpanel" | xargs -r docker rm -f >/dev/null 2>&1 || true
  rm -f "$ENV_FILE"
  rm -rf "$NODE_DIR" "$INSTALL_DIR/state"
  log "Previous install removed - continuing with a clean setup."
}

ensure_deploy_files() {
  local f
  for f in "${DEPLOY_COMPANION_FILES[@]}"; do
    if [[ ! -f "$SCRIPT_SOURCE_DIR/$f" ]]; then
      log "Running as a standalone script - downloading companion files (docker-compose.yml, Caddyfile, .env.example, wgpanel) from GitHub..."
      local tmpdir; tmpdir="$(mktemp -d)"
      for f in "${DEPLOY_COMPANION_FILES[@]}"; do
        curl -fsSL "$REPO_RAW_BASE/$f" -o "$tmpdir/$f"
      done
      SCRIPT_SOURCE_DIR="$tmpdir"
      return
    fi
  done
}

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    err "Please run as root (sudo bash install.sh)"
    exit 1
  fi
}

detect_os() {
  if ! command -v apt-get >/dev/null 2>&1; then
    err "This installer currently supports Debian/Ubuntu (apt-based) systems only."
    exit 1
  fi
}

install_prereqs() {
  log "Installing base packages..."
  apt-get update -y
  # git is only used by the self-node's build-from-source fallback (ensure_self_node_image).
  apt-get install -y curl ca-certificates gnupg lsb-release ufw openssl git
}

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "Docker + Compose plugin already installed, skipping."
    return
  fi
  log "Installing Docker Engine + Compose plugin..."
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
}

random_secret() { openssl rand -hex 24; }

setup_files() {
  mkdir -p "$INSTALL_DIR"
  cp "$SCRIPT_SOURCE_DIR/docker-compose.yml" "$INSTALL_DIR/docker-compose.yml"
  cp "$SCRIPT_SOURCE_DIR/Caddyfile" "$INSTALL_DIR/Caddyfile"

  if [[ -f "$ENV_FILE" ]]; then
    warn ".env already exists at $ENV_FILE - leaving existing values untouched. Re-run with --fresh (or delete it) for a clean setup."
    # An existing .env predates whatever keys this script has added since it was
    # first created (e.g. PANEL_HTTP_PORT/PANEL_HTTPS_PORT) - the fresh-install
    # branch below prompts for these once, but "leave it untouched" then means the
    # prompt is skipped forever on a pre-existing .env, silently falling back to
    # docker-compose's :-80/:-443 defaults with no way to know that happened until
    # a port conflict shows up. Found live: a second server's .env predated these
    # keys, install.sh never asked, and Caddy failed to bind because 443 was
    # already taken by something else on that host.
    if ! grep -q '^PANEL_HTTP_PORT=' "$ENV_FILE"; then
      read -rp "Public HTTP port [80]: " PANEL_HTTP_PORT
      echo "PANEL_HTTP_PORT=${PANEL_HTTP_PORT:-80}" >> "$ENV_FILE"
    fi
    if ! grep -q '^PANEL_HTTPS_PORT=' "$ENV_FILE"; then
      read -rp "Public HTTPS port [443]: " PANEL_HTTPS_PORT
      echo "PANEL_HTTPS_PORT=${PANEL_HTTPS_PORT:-443}" >> "$ENV_FILE"
    fi
    return
  fi

  cp "$SCRIPT_SOURCE_DIR/.env.example" "$ENV_FILE"

  read -rp "Panel domain (must already point to this server's IP, e.g. panel.example.com): " PANEL_DOMAIN
  read -rp "Admin e-mail (used for Let's Encrypt notices): " ADMIN_EMAIL
  read -rp "Desired super-admin username [admin]: " ADMIN_USER
  ADMIN_USER=${ADMIN_USER:-admin}
  # Only relevant if 80/443 are already taken by another service on this host -
  # Caddy itself still binds :80/:443 inside its own container either way (its
  # automatic-HTTPS/ACME logic assumes that internally); this only changes which
  # host-side port maps to it.
  read -rp "Public HTTP port [80]: " PANEL_HTTP_PORT
  PANEL_HTTP_PORT=${PANEL_HTTP_PORT:-80}
  read -rp "Public HTTPS port [443]: " PANEL_HTTPS_PORT
  PANEL_HTTPS_PORT=${PANEL_HTTPS_PORT:-443}

  PG_PASS="$(random_secret)"
  REDIS_PASS="$(random_secret)"
  JWT_SECRET="$(random_secret)"
  HMAC_KEY="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM - encrypts api_keys.secret_encrypted (STORY-05)
  INTERNAL_TOKEN="$(random_secret)"
  ACCOUNT_KEY_ENC="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM

  sed -i \
    -e "s#^PANEL_DOMAIN=.*#PANEL_DOMAIN=${PANEL_DOMAIN}#" \
    -e "s#^ADMIN_ACL_EMAIL=.*#ADMIN_ACL_EMAIL=${ADMIN_EMAIL}#" \
    -e "s#^PANEL_HTTP_PORT=.*#PANEL_HTTP_PORT=${PANEL_HTTP_PORT}#" \
    -e "s#^PANEL_HTTPS_PORT=.*#PANEL_HTTPS_PORT=${PANEL_HTTPS_PORT}#" \
    -e "s#^POSTGRES_PASSWORD=.*#POSTGRES_PASSWORD=${PG_PASS}#" \
    -e "s#^REDIS_PASSWORD=.*#REDIS_PASSWORD=${REDIS_PASS}#" \
    -e "s#^JWT_SECRET=.*#JWT_SECRET=${JWT_SECRET}#" \
    -e "s#^API_HMAC_MASTER_KEY=.*#API_HMAC_MASTER_KEY=${HMAC_KEY}#" \
    -e "s#^INTERNAL_API_TOKEN=.*#INTERNAL_API_TOKEN=${INTERNAL_TOKEN}#" \
    -e "s#^ACCOUNT_KEY_ENCRYPTION_KEY=.*#ACCOUNT_KEY_ENCRYPTION_KEY=${ACCOUNT_KEY_ENC}#" \
    -e "s#^ADMIN_BOOTSTRAP_USERNAME=.*#ADMIN_BOOTSTRAP_USERNAME=${ADMIN_USER}#" \
    "$ENV_FILE"

  chmod 600 "$ENV_FILE"
  log "Secrets generated and saved to $ENV_FILE (permissions 600)."
}

setup_firewall() {
  log "Configuring firewall (ufw)..."
  ufw allow OpenSSH >/dev/null 2>&1 || true
  ufw allow 80/tcp
  ufw allow 443/tcp
  # Node agents connect back to the control plane on this port over the public internet.
  NODE_AGENT_PORT="$(grep '^NODE_AGENT_PORT=' "$ENV_FILE" | cut -d= -f2)"
  ufw allow "${NODE_AGENT_PORT}/tcp"
  # ufw's default FORWARD policy is DROP, which also drops the traffic Docker forwards
  # for the self-node container's clients. Setting it to ACCEPT lets Docker's own
  # per-bridge FORWARD rules govern forwarding (they're specific, not blanket), which
  # is what the containerized node relies on for full-tunnel client internet.
  if [[ -f /etc/default/ufw ]]; then
    sed -i 's/^DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw
  fi
  yes | ufw enable >/dev/null 2>&1 || true
  ufw reload >/dev/null 2>&1 || true
}

# preflight_ports fails fast with a clear message if a host port the stack must
# publish is already taken - far friendlier than the mid-`docker compose up`
# "failed to start userland proxy / docker-proxy" error you get otherwise. Only
# checks the publicly-bound NODE_AGENT_PORT; the loopback API/frontend ports rarely
# clash and Docker's own message for those is at least specific.
preflight_ports() {
  local port pid
  port="$(grep '^NODE_AGENT_PORT=' "$ENV_FILE" | cut -d= -f2)"
  [[ -n "$port" ]] || return 0
  if command -v ss >/dev/null 2>&1 && ss -tlnH "sport = :${port}" 2>/dev/null | grep -q .; then
    pid="$(ss -tlnpH "sport = :${port}" 2>/dev/null | grep -oE 'users:\(\("[^"]+"' | head -n1 | sed -E 's/.*"([^"]+)"/\1/')"
    err "Port ${port} (NODE_AGENT_PORT) is already in use${pid:+ by \"${pid}\"} - the API container can't publish it."
    err "  Common culprit: cockpit or prometheus (both default to 9090)."
    err "  Fix: free the port (e.g. 'systemctl disable --now cockpit.socket'), or set a"
    err "       different NODE_AGENT_PORT in ${ENV_FILE} and open it in the firewall, then re-run."
    exit 1
  fi
}

start_stack() {
  preflight_ports
  log "Pulling images and starting the stack..."
  # --force-recreate: plain `up -d` only recreates a container when compose detects
  # its own service definition changed - it does NOT notice that a bind-mounted
  # config file (Caddyfile, docker-compose.yml itself) changed on disk, so a
  # container can keep running against a stale file indefinitely. Found live: fixing
  # a real Caddyfile routing bug and updating the file on disk did nothing until the
  # container was explicitly force-recreated.
  (cd "$INSTALL_DIR" && docker compose pull)
  # Bring the stack up, but never let a slow first boot abort the whole install: the
  # api's first start migrates a fresh DB before it reports healthy, and compose can
  # return non-zero while waiting. show_first_admin_credentials waits for real health
  # next, so treat a non-zero here as "keep going" rather than a fatal error (which,
  # under `set -e`, would otherwise kill the script right at the finish line).
  if ! (cd "$INSTALL_DIR" && docker compose up -d --force-recreate); then
    warn "compose returned non-zero while starting (often just the api still migrating on first boot) - continuing and waiting for health below."
  fi
}

install_cli() {
  cp "$SCRIPT_SOURCE_DIR/wgpanel" /usr/local/bin/wgpanel
  chmod +x /usr/local/bin/wgpanel
  log "Installed 'wgpanel' management command (try: wgpanel status / wgpanel doctor)"
}

# Populated by show_first_admin_credentials, printed as part of the final summary at
# the very end of main() instead of immediately - previously this printed right
# before setup_self_node's own long, scrolling output (WireGuard setup, docker pulls,
# more prompts), so by the time the script finished the one-time password had
# already scrolled off screen. Same information, just held until the end so it's the
# last thing on screen, not the first.
ADMIN_CREDS_BLOCK=""

show_first_admin_credentials() {
  # The API auto-creates the super admin itself on its very first successful boot
  # against a fresh database (gated on the admins table being empty - see
  # cmd/api/main.go's bootstrapFirstAdmin), using ADMIN_BOOTSTRAP_USERNAME from .env.
  # This just waits for that boot to finish, then re-displays the one-time password it
  # printed to its own logs - it is never recoverable after this, so surface it clearly
  # rather than leaving the admin to go find it in `docker compose logs api` themselves.
  log "Waiting for the API to finish booting/migrating and creating the super admin..."
  if ! wgpanel_wait_for_health; then
    err "API did not become healthy within the timeout. Check: wgpanel logs api"
    err "If it recovers on its own, find the generated credentials with: wgpanel logs api | grep WGPANEL_INITIAL_ADMIN"
    return 1
  fi

  local creds
  creds="$(cd "$INSTALL_DIR" && docker compose logs api 2>/dev/null | grep 'WGPANEL_INITIAL_ADMIN_' || true)"
  if [[ -z "$creds" ]]; then
    warn "API is healthy but no bootstrap-admin log line was found (an admin may already exist from a prior install)."
    warn "If you need a new one, run: wgpanel create-admin --username <name>"
    return 0
  fi

  ADMIN_CREDS_BLOCK="$(echo "$creds" | sed -E 's/^.*(WGPANEL_INITIAL_ADMIN_[A-Z]+=.*)$/  \1/')"
}

wgpanel_wait_for_health() {
  # 180s, not 90: a fresh DB's first-boot migration + admin bootstrap can legitimately
  # run past 90s on a small/loaded VPS, and this wait is what surfaces the one-time
  # admin password - timing out early makes install.sh claim failure on a panel that's
  # actually still coming up.
  local api_port token timeout=180 waited=0
  api_port="$(grep '^API_PORT=' "$ENV_FILE" | cut -d= -f2)"
  token="$(grep '^INTERNAL_API_TOKEN=' "$ENV_FILE" | cut -d= -f2)"
  while (( waited < timeout )); do
    curl -fsS -H "X-Internal-Token: ${token}" "http://127.0.0.1:${api_port}/internal/healthz" >/dev/null 2>&1 && return 0
    sleep 3
    waited=$((waited + 3))
  done
  return 1
}

# ---- core server as a node (docs/STORY-09-multi-node-accounts.md) ----
#
# Sets this same server up as this WGPanel's first WireGuard node - as a production
# Docker container (the same NODE_IMAGE install-node.sh uses on remote nodes), so a
# single-server install can create accounts immediately with no separate "add a node"
# step. The node container joins the panel's own compose network so its agent dials
# the API directly at api:NODE_AGENT_PORT. Idempotent: the presence of NODE_COMPOSE_FILE
# means this box is already set up, so re-running install.sh never creates a second node.
setup_self_node() {
  if [[ -f "$NODE_COMPOSE_FILE" ]]; then
    warn "This server is already set up as a node (found ${NODE_COMPOSE_FILE}) - skipping."
    return 0
  fi

  read -rp "Also set up WireGuard on this same server as your first node? [Y/n]: " DO_SELF_NODE
  if [[ "${DO_SELF_NODE:-Y}" =~ ^[Nn] ]]; then
    log "Skipping - add a node later with: Nodes -> Add Node in the panel, then install-node.sh on that server."
    return 0
  fi

  log "Configuring this server as a WireGuard node (Docker)..."
  read -rp "A name for this node [core]: " NODE_NAME
  NODE_NAME=${NODE_NAME:-core}
  read -rp "Node group [default]: " NODE_GROUP
  NODE_GROUP=${NODE_GROUP:-default}
  read -rp "Max peers on this node [250]: " NODE_CAPACITY
  NODE_CAPACITY=${NODE_CAPACITY:-250}
  read -rp "WireGuard listen port [51820]: " WG_PORT
  WG_PORT=${WG_PORT:-51820}
  read -rp "WireGuard interface name [wg0]: " WG_IFACE
  WG_IFACE=${WG_IFACE:-wg0}
  # Every prompt has a default so hitting Enter through the whole sequence produces a
  # valid config (an empty wg_subnet used to reach the API and 400).
  read -rp "WireGuard subnet for peer IPs [10.66.0.0/24]: " WG_SUBNET
  WG_SUBNET=${WG_SUBNET:-10.66.0.0/24}
  # Auto-derived as the subnet's .1 (the convention used everywhere else here), still overridable.
  default_iface_addr="$(echo "$WG_SUBNET" | sed -E 's#^([0-9]+\.[0-9]+\.[0-9]+)\.[0-9]+/([0-9]+)$#\1.1/\2#')"
  read -rp "This node's own interface address, with prefix [${default_iface_addr}]: " WG_IFACE_ADDR
  WG_IFACE_ADDR=${WG_IFACE_ADDR:-$default_iface_addr}

  local panel_domain node_agent_port node_image
  panel_domain="$(grep '^PANEL_DOMAIN=' "$ENV_FILE" | cut -d= -f2)"
  node_agent_port="$(grep '^NODE_AGENT_PORT=' "$ENV_FILE" | cut -d= -f2)"
  node_image="$(grep '^NODE_IMAGE=' "$ENV_FILE" | cut -d= -f2)"
  node_image="${node_image:-ghcr.io/iamfarhad/wgpanel-node:latest}"

  setup_self_node_host || return 1
  bootstrap_self_node "$panel_domain" || return 1
  write_self_node_files "$node_image" "$node_agent_port" || return 1
  ensure_self_node_image "$node_image" || return 1
  (cd "$NODE_DIR" && docker compose up -d) || return 1
  ufw allow "${WG_PORT}/udp" >/dev/null 2>&1 || true

  sleep 3
  if (cd "$NODE_DIR" && docker compose ps --status running --format '{{.Name}}' | grep -q .); then
    log "This server is now registered as node '${NODE_NAME}' and should show 'online' in the panel shortly."
  else
    err "Node container failed to start. Check logs with: cd ${NODE_DIR} && docker compose logs"
  fi
}

# Host prep for running WireGuard in a container: the kernel module (the container's
# wg-quick drives the host kernel), loaded now and on every boot. IP forwarding for
# the container's internet egress is handled by the Docker daemon itself.
setup_self_node_host() {
  apt-get install -y wireguard >/dev/null 2>&1 || true
  modprobe wireguard 2>/dev/null || warn "Could not load the wireguard kernel module - continuing (may be built in)."
  echo wireguard > /etc/modules-load.d/wireguard.conf
}

# Creates the node record and returns a join token via the internal bootstrap-self
# endpoint (CreateNode + join-token in one call - nodes_bootstrap.go). No public_key
# is sent: the container generates its keypair on first boot and submits the public
# key when its agent registers (agentserver.go's SetNodePublicKey), same as a remote
# node installed with install-node.sh.
bootstrap_self_node() {
  local panel_domain="$1"
  local api_port token resp
  api_port="$(grep '^API_PORT=' "$ENV_FILE" | cut -d= -f2)"
  token="$(grep '^INTERNAL_API_TOKEN=' "$ENV_FILE" | cut -d= -f2)"

  # No -f (deliberately): it would swallow a non-2xx body, turning a real error (e.g.
  # "wg_subnet is required") into an empty string. An empty JOIN_TOKEN detects failure.
  resp=$(curl -sS -X POST "http://127.0.0.1:${api_port}/internal/nodes/bootstrap-self" \
    -H "X-Internal-Token: ${token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"${NODE_NAME}\",\"node_group\":\"${NODE_GROUP}\",\"public_endpoint\":\"${panel_domain}:${WG_PORT}\",\"wg_subnet\":\"${WG_SUBNET}\",\"capacity_max_peers\":${NODE_CAPACITY}}")

  JOIN_TOKEN="$(echo "$resp" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)"
  if [[ -z "$JOIN_TOKEN" ]]; then
    err "Could not bootstrap this node - response: ${resp}"
    return 1
  fi
}

# Writes the node's compose + env under NODE_DIR. Bridge networking (the container
# NATs client traffic in its own netns, so no host DOCKER-USER handling is needed),
# but attached to the panel's existing compose network so the agent reaches the API at
# api:NODE_AGENT_PORT with no public hairpin. Only the WireGuard UDP port is published.
write_self_node_files() {
  local node_image="$1" node_agent_port="$2"
  mkdir -p "$NODE_DIR"

  cat > "$NODE_ENV_FILE" <<EOF
NODE_IMAGE=${node_image}
WGPANEL_PANEL_ADDR=api:${node_agent_port}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PORT=${WG_PORT}
WG_IFACE_ADDR=${WG_IFACE_ADDR}
EOF
  chmod 600 "$NODE_ENV_FILE"

  cat > "$NODE_COMPOSE_FILE" <<'EOF'
name: wgpanel-node

services:
  node:
    image: ${NODE_IMAGE}
    restart: unless-stopped
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    cap_add:
      - NET_ADMIN
    security_opt:
      - no-new-privileges:true
    sysctls:
      - net.ipv4.ip_forward=1
    env_file:
      - .env
    networks:
      - panel
    ports:
      - "${WGPANEL_WG_PORT:-51820}:${WGPANEL_WG_PORT:-51820}/udp"
    volumes:
      - node_wg_state:/etc/wireguard
      - node_agent_state:/etc/wgpanel/state
    deploy:
      resources:
        limits:
          memory: ${NODE_MEM_LIMIT:-256m}

networks:
  panel:
    # The panel stack (name: wgpanel) creates this; the node joins it to reach api:PORT.
    external: true
    name: wgpanel_default

volumes:
  node_wg_state:
  node_agent_state:
EOF
}

# Pulls the node image; falls back to building it from source if the pull fails
# (private GHCR package, or before CI has published it) - mirrors install-node.sh.
ensure_self_node_image() {
  local node_image="$1"
  if (cd "$NODE_DIR" && docker compose pull); then
    return 0
  fi
  warn "Could not pull ${node_image} - building the node image from source instead."
  local src="/tmp/wgpanel-src.$$"
  rm -rf "$src"
  git clone --depth 1 "$WGPANEL_REPO_URL" "$src" || return 1
  docker build -f "$src/deploy/node.Dockerfile" -t wgpanel-node:local "$src" || { rm -rf "$src"; return 1; }
  rm -rf "$src"
  sed -i 's#^NODE_IMAGE=.*#NODE_IMAGE=wgpanel-node:local#' "$NODE_ENV_FILE"
}

main() {
  parse_args "$@"
  require_root
  detect_os
  ensure_deploy_files
  install_prereqs
  install_docker
  fresh_reset
  setup_files
  setup_firewall
  start_stack
  install_cli
  show_first_admin_credentials || true

  # Run in a subshell with errexit disabled: setup_self_node is a nice-to-have on
  # top of an already-fully-working panel, and every step inside it is explicitly
  # `|| return 1`-guarded, so a failure partway through (docker cp, wg genkey,
  # the bootstrap API call, whatever) should stop just that flow, not abort this
  # whole script this late after the panel and its first admin already exist.
  ( set +e; setup_self_node ); self_node_rc=$?
  if [[ $self_node_rc -ne 0 ]]; then
    warn "Self-node setup did not complete - the panel itself is still fully usable; add a node manually with install-node.sh whenever you're ready."
  fi

  # Read back from the env file rather than reusing setup_files()'s PANEL_DOMAIN/
  # PANEL_HTTPS_PORT variables directly - those are only ever set on a fresh
  # install; setup_files() returns early without touching them at all when .env
  # already existed, which under `set -u` would otherwise crash here exactly like
  # cmd_backup's leaked RETURN trap did.
  PANEL_DOMAIN="$(grep '^PANEL_DOMAIN=' "$ENV_FILE" | cut -d= -f2)"
  PANEL_HTTPS_PORT="$(grep '^PANEL_HTTPS_PORT=' "$ENV_FILE" | cut -d= -f2)"
  panel_display_url="https://${PANEL_DOMAIN}"
  if [[ -n "$PANEL_HTTPS_PORT" && "$PANEL_HTTPS_PORT" != "443" ]]; then
    panel_display_url="${panel_display_url}:${PANEL_HTTPS_PORT}"
  fi
  echo
  echo "=================================================================="
  echo " WGPanel install complete"
  echo "=================================================================="
  echo "  Panel address:  ${panel_display_url}"
  if [[ -n "$ADMIN_CREDS_BLOCK" ]]; then
    echo
    echo "  Login (save these now - shown only this once):"
    echo "$ADMIN_CREDS_BLOCK"
  else
    echo
    echo "  An admin already existed - recover credentials with: wgpanel show-bootstrap-admin"
  fi
  echo "=================================================================="
  log "(Caddy needs a minute to obtain the TLS certificate on first boot - check with: wgpanel logs caddy)"
  log "Manage the stack with: wgpanel {start|stop|restart|status|logs|update|rollback|backup|restore|doctor|create-admin}"
  log "To add more WireGuard nodes later: Nodes -> Add Node in the panel, then run install-node.sh on that server."
}

main "$@"
