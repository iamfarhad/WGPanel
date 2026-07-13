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

# Same layout install-node.sh uses on a remote WireGuard node - this script can set
# the panel's own server up exactly the same way (docs/STORY-09-multi-node-accounts.md).
AGENT_BIN="/usr/local/bin/wgpanel-agent"
AGENT_DIR="/etc/wgpanel"
AGENT_ENV="$AGENT_DIR/agent.env"

log()  { echo -e "\033[1;32m[wgpanel]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel]\033[0m $*" >&2; }

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
  apt-get install -y curl ca-certificates gnupg lsb-release ufw openssl
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
    warn ".env already exists at $ENV_FILE - leaving it untouched. Delete it first if you want a fresh setup."
    return
  fi

  cp "$SCRIPT_SOURCE_DIR/.env.example" "$ENV_FILE"

  read -rp "Panel domain (must already point to this server's IP, e.g. panel.example.com): " PANEL_DOMAIN
  read -rp "Admin e-mail (used for Let's Encrypt notices): " ADMIN_EMAIL
  read -rp "Desired super-admin username [admin]: " ADMIN_USER
  ADMIN_USER=${ADMIN_USER:-admin}

  PG_PASS="$(random_secret)"
  REDIS_PASS="$(random_secret)"
  JWT_SECRET="$(random_secret)"
  HMAC_KEY="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM - encrypts api_keys.secret_encrypted (STORY-05)
  INTERNAL_TOKEN="$(random_secret)"
  ACCOUNT_KEY_ENC="$(openssl rand -hex 32)" # exactly 32 bytes for AES-256-GCM

  sed -i \
    -e "s#^PANEL_DOMAIN=.*#PANEL_DOMAIN=${PANEL_DOMAIN}#" \
    -e "s#^ADMIN_ACL_EMAIL=.*#ADMIN_ACL_EMAIL=${ADMIN_EMAIL}#" \
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
  yes | ufw enable >/dev/null 2>&1 || true
}

start_stack() {
  log "Pulling images and starting the stack..."
  (cd "$INSTALL_DIR" && docker compose pull && docker compose up -d)
}

install_cli() {
  cp "$SCRIPT_SOURCE_DIR/wgpanel" /usr/local/bin/wgpanel
  chmod +x /usr/local/bin/wgpanel
  log "Installed 'wgpanel' management command (try: wgpanel status / wgpanel doctor)"
}

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

  echo
  echo "=============================================="
  echo " Super admin created - save these credentials "
  echo "=============================================="
  echo "$creds" | sed -E 's/^.*(WGPANEL_INITIAL_ADMIN_[A-Z]+=.*)$/  \1/'
  echo "=============================================="
  echo
}

wgpanel_wait_for_health() {
  local api_port token timeout=90 waited=0
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
# Sets this same server up as this WGPanel's first WireGuard node, exactly the way
# install-node.sh would on a remote server - so a single-server install can create
# accounts immediately, with no separate "add a node" step. Guarded by the same
# idempotency check cmd/agent itself already uses for reusing a prior registration
# (state files under $AGENT_DIR/state) - re-running install.sh never creates a
# second/duplicate node for this same box.
setup_self_node() {
  if [[ -f "${AGENT_DIR}/state/node-id.txt" ]]; then
    warn "This server is already registered as a node (found ${AGENT_DIR}/state/node-id.txt) - skipping."
    return 0
  fi

  read -rp "Also set up WireGuard on this same server as your first node? [Y/n]: " DO_SELF_NODE
  if [[ "${DO_SELF_NODE:-Y}" =~ ^[Nn] ]]; then
    log "Skipping - add a node later with: Nodes -> Add Node in the panel, then install-node.sh on that server."
    return 0
  fi

  log "Configuring this server as a WireGuard node..."
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
  # Both of these used to have no default at all, unlike every other prompt in this
  # flow - a real trap found on a live install: hitting Enter through the whole
  # sequence (comfortable everywhere else, since every other prompt has one) left
  # wg_subnet empty, which the API correctly 400'd on - but bootstrap_self_node's
  # `curl -f` swallowed the actual error body, so the failure looked like a mystery
  # blank response instead of "wg_subnet is required."
  read -rp "WireGuard subnet for peer IPs [10.66.0.0/24]: " WG_SUBNET
  WG_SUBNET=${WG_SUBNET:-10.66.0.0/24}
  # Auto-derived as the subnet's .1 address (the convention used everywhere else in
  # this project) rather than asking for genuinely redundant information a moment
  # after WG_SUBNET - still overridable if you want something else.
  default_iface_addr="$(echo "$WG_SUBNET" | sed -E 's#^([0-9]+\.[0-9]+\.[0-9]+)\.[0-9]+/([0-9]+)$#\1.1/\2#')"
  read -rp "This node's own interface address, with prefix [${default_iface_addr}]: " WG_IFACE_ADDR
  WG_IFACE_ADDR=${WG_IFACE_ADDR:-$default_iface_addr}

  local panel_domain node_agent_port
  panel_domain="$(grep '^PANEL_DOMAIN=' "$ENV_FILE" | cut -d= -f2)"
  node_agent_port="$(grep '^NODE_AGENT_PORT=' "$ENV_FILE" | cut -d= -f2)"

  setup_self_wireguard || return 1
  extract_agent_binary || return 1
  bootstrap_self_node "$panel_domain" "$node_agent_port" || return 1
  configure_self_agent "$node_agent_port" || return 1
  install_self_systemd_service || return 1
  ufw allow "${WG_PORT}/udp" >/dev/null 2>&1 || true

  sleep 2
  if systemctl is-active --quiet wgpanel-agent; then
    log "This server is now registered as node '${NODE_NAME}' and should show 'online' in the panel shortly."
  else
    err "Agent service failed to start. Check logs with: journalctl -u wgpanel-agent -e"
  fi
}

# Same as install-node.sh's setup_wireguard(), run against this same box.
setup_self_wireguard() {
  local wg_conf_dir="/etc/wireguard"
  local wg_conf="${wg_conf_dir}/${WG_IFACE}.conf"
  apt-get install -y wireguard wireguard-tools >/dev/null
  modprobe wireguard 2>/dev/null || warn "Could not load the wireguard kernel module directly - continuing (may already be built in)."

  mkdir -p "$wg_conf_dir"
  chmod 700 "$wg_conf_dir"

  if [[ -f "$wg_conf" ]]; then
    warn "${wg_conf} already exists - leaving it untouched."
    WG_PUBLIC_KEY="$(wg pubkey < "${wg_conf_dir}/${WG_IFACE}-private.key" 2>/dev/null || true)"
    return
  fi

  umask 077
  wg genkey > "${wg_conf_dir}/${WG_IFACE}-private.key"
  wg pubkey < "${wg_conf_dir}/${WG_IFACE}-private.key" > "${wg_conf_dir}/${WG_IFACE}-public.key"
  WG_PUBLIC_KEY="$(cat "${wg_conf_dir}/${WG_IFACE}-public.key")"

  cat > "$wg_conf" <<EOF
[Interface]
PrivateKey = $(cat "${wg_conf_dir}/${WG_IFACE}-private.key")
Address = ${WG_IFACE_ADDR}
ListenPort = ${WG_PORT}
EOF
  chmod 600 "$wg_conf"

  systemctl enable --now "wg-quick@${WG_IFACE}"
  log "WireGuard interface ${WG_IFACE} is up (public key: ${WG_PUBLIC_KEY})"
}

# Extracts the wgpanel-agent binary from the already-built/pulled API image via
# `docker create`+`docker cp` (never started) - avoids depending on
# install-node.sh's separate, currently-unpublished release-download URL, and
# guarantees an architecture match since Docker itself already resolved the correct
# image for this host.
extract_agent_binary() {
  mkdir -p "$AGENT_DIR" "${AGENT_DIR}/state"
  local api_image cid
  api_image="$(grep '^API_IMAGE=' "$ENV_FILE" | cut -d= -f2)"
  log "Extracting the node agent binary from ${api_image}..."
  cid="$(docker create "$api_image")"
  docker cp "${cid}:/wgpanel-agent" "$AGENT_BIN"
  docker rm "$cid" >/dev/null
  chmod +x "$AGENT_BIN"
}

# Calls the internal bootstrap-self endpoint (combines CreateNode + join-token
# generation in one call - see backend/internal/httpapi/nodes_bootstrap.go) using
# the token already available in this script.
bootstrap_self_node() {
  local panel_domain="$1" node_agent_port="$2"
  local api_port token resp
  api_port="$(grep '^API_PORT=' "$ENV_FILE" | cut -d= -f2)"
  token="$(grep '^INTERNAL_API_TOKEN=' "$ENV_FILE" | cut -d= -f2)"

  # No -f here (deliberately): it would swallow the response body on a non-2xx,
  # turning a real error message (e.g. "wg_subnet is required") into an empty
  # string - exactly what made an earlier real failure look like a mystery blank
  # response instead of a diagnosable one. JOIN_TOKEN being empty below is what
  # actually detects failure here.
  resp=$(curl -sS -X POST "http://127.0.0.1:${api_port}/internal/nodes/bootstrap-self" \
    -H "X-Internal-Token: ${token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"${NODE_NAME}\",\"node_group\":\"${NODE_GROUP}\",\"public_endpoint\":\"${panel_domain}:${WG_PORT}\",\"wg_subnet\":\"${WG_SUBNET}\",\"capacity_max_peers\":${NODE_CAPACITY},\"public_key\":\"${WG_PUBLIC_KEY}\"}")

  JOIN_TOKEN="$(echo "$resp" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)"
  if [[ -z "$JOIN_TOKEN" ]]; then
    err "Could not bootstrap this node - response: ${resp}"
    return 1
  fi
}

configure_self_agent() {
  local node_agent_port="$1"
  cat > "$AGENT_ENV" <<EOF
WGPANEL_PANEL_ADDR=127.0.0.1:${node_agent_port}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PUBLIC_KEY=${WG_PUBLIC_KEY}
WGPANEL_STATE_DIR=${AGENT_DIR}/state
EOF
  chmod 600 "$AGENT_ENV"
}

install_self_systemd_service() {
  cat > /etc/systemd/system/wgpanel-agent.service <<EOF
[Unit]
Description=WGPanel node agent (self-registered core node)
After=network-online.target wg-quick@${WG_IFACE}.service
Wants=network-online.target
Requires=wg-quick@${WG_IFACE}.service

[Service]
Type=simple
EnvironmentFile=${AGENT_ENV}
ExecStart=${AGENT_BIN}
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now wgpanel-agent
}

main() {
  require_root
  detect_os
  ensure_deploy_files
  install_prereqs
  install_docker
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

  PANEL_DOMAIN="$(grep '^PANEL_DOMAIN=' "$ENV_FILE" | cut -d= -f2)"
  echo
  log "Done. Panel should be reachable shortly at: https://${PANEL_DOMAIN}"
  log "(Caddy needs a minute to obtain the TLS certificate on first boot - check with: wgpanel logs caddy)"
  log "Manage the stack with: wgpanel {start|stop|restart|status|logs|update|rollback|backup|restore|doctor|create-admin}"
  log "To add more WireGuard nodes later: Nodes -> Add Node in the panel, then run install-node.sh on that server."
}

main "$@"
