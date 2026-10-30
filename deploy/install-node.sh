#!/usr/bin/env bash
#
# WGPanel - Node agent installer (production Docker)
# Run this on EACH server that will actually run WireGuard and terminate customer
# connections. It installs Docker, then runs the WireGuard node as a production
# container (deploy/node.Dockerfile image) managed by docker compose - the same
# proven model the in-repo dev node uses, just pulled from the registry and set to
# restart on boot. The container connects OUTBOUND to the control plane (no inbound
# access to the panel needed), applies peer changes via wgctrl, terminates client
# tunnels, and NATs their traffic to the internet.
#
# Usage:
#   sudo bash install-node.sh                          # install the newest release
#   sudo bash install-node.sh --version v0.1.0         # install a specific release
#   sudo bash install-node.sh --update [--version vX]  # move an installed node to a release
# Use the same version as the panel (`wgpanel version` on the panel server).
#
# You will be asked for:
#   - the control-plane address (e.g. panel.example.com:48443)
#   - a one-time join token, generated from the admin panel: Nodes -> Add Node -> Generate Token
# The WireGuard listen port and interface address are taken from the node's settings
# in the panel (they must match it exactly); you are only asked for them if the
# panel can't be queried.
#
# Non-interactive (used by the panel's "Install over SSH", but works anywhere):
#   sudo env NODE_PANEL_ADDR=panel.example.com:48443 NODE_JOIN_TOKEN=... \
#        bash install-node.sh --non-interactive      (or WGPANEL_NONINTERACTIVE=1)
# NODE_PANEL_ADDR and NODE_JOIN_TOKEN are required. NODE_NAME, NODE_WG_PORT,
# NODE_WG_IFACE and NODE_WG_IFACE_ADDR are optional: the panel's preflight answers win
# for name/port/address, so they only matter when the panel cannot be queried.
# Instead of prompting, it fails with a distinct exit code: 10 not root, 11 unsupported
# OS, 12 Docker install failed, 13 invalid/missing settings, 14 join token rejected.
#
# The node image is ghcr.io/iamfarhad/wgpanel-node pinned to the chosen release (set
# NODE_IMAGE to override it entirely). If it can't be pulled (private package, or
# before CI has published it), the script falls back to building that release from
# source - override the source with WGPANEL_REPO_URL.
set -euo pipefail

NODE_DIR="/opt/wgpanel-node"
COMPOSE_FILE="$NODE_DIR/docker-compose.yml"
ENV_FILE="$NODE_DIR/.env"
NODE_IMAGE="${NODE_IMAGE:-}"
WGPANEL_REPO_URL="${WGPANEL_REPO_URL:-https://github.com/iamfarhad/WGPanel.git}"
REQUESTED_VERSION="${WGPANEL_VERSION:-latest}"
RELEASE=""
UPDATE=0

NONINTERACTIVE="${WGPANEL_NONINTERACTIVE:-0}"
for arg in "$@"; do
  case "$arg" in
    --non-interactive|-y) NONINTERACTIVE=1 ;;
  esac
done

log()  { echo -e "\033[1;32m[wgpanel-node]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel-node]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel-node]\033[0m $*" >&2; }

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    err "Please run as root (sudo bash install-node.sh)"
    exit 10
  fi
}

detect_os() {
  if ! command -v apt-get >/dev/null 2>&1; then
    err "This installer currently supports Debian/Ubuntu (apt-based) systems only."
    exit 11
  fi
}

install_prereqs() {
  log "Installing base packages and the WireGuard kernel module..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  # wireguard-tools is not needed on the host (it lives in the container), but the
  # kernel MODULE is: the container's wg-quick drives the host kernel's WireGuard.
  # git is only used by the build-from-source fallback.
  apt-get install -y curl ca-certificates ufw wireguard git

  # Load the module now and on every boot - the container relies on it being present.
  if ! modprobe wireguard 2>/dev/null; then
    warn "Could not load the wireguard kernel module (kernel may have it built in, or needs wireguard-dkms). Continuing."
  fi
  echo wireguard > /etc/modules-load.d/wireguard.conf
}

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "Docker + Compose plugin already installed, skipping."
    # Still make sure it starts on boot - a preinstalled-but-disabled daemon would
    # leave the node dead after the first reboot, despite restart: unless-stopped.
    systemctl enable --now docker >/dev/null 2>&1 || true
    return
  fi
  log "Installing Docker Engine + Compose plugin..."
  if ! curl -fsSL https://get.docker.com | sh; then
    err "Docker could not be installed (see the output above)."
    exit 12
  fi
  systemctl enable --now docker
}

# read_required re-prompts until the answer is non-empty. Without this, a stray
# blank line in a paste (very easy to hit when copy-pasting the join token with a
# trailing newline) silently answers the NEXT prompt, and required settings land
# empty in .env - the node then fails in ways that only surface much later.
# Pasted CRs (CRLF clipboards) are stripped too.
read_required() {
  local prompt="$1" var="$2" val=""
  while [[ -z "$val" ]]; do
    read -rp "$prompt" val
    val="${val//$'\r'/}"
  done
  printf -v "$var" '%s' "$val"
}

# read_default applies a default on Enter (also strips pasted CRs). The node-name
# prompt in prompt_config needs it; install.sh carries the same helper.
read_default() {
  local prompt="$1" var="$2" def="$3" val
  read -rp "$prompt" val
  val="${val//$'\r'/}"
  printf -v "$var" '%s' "${val:-$def}"
}

# valid_cidr4 checks the whole value, not just its shape: 999.1.1.1/40 matches a
# naive digits regex but only fails much later, inside the container's wg-quick,
# where the error is far harder to trace back to this prompt.
valid_cidr4() {
  local addr="$1" prefix o
  [[ "$addr" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})/([0-9]{1,2})$ ]] || return 1
  # 10# forces base-10: a leading zero ("08") would otherwise be parsed as
  # (invalid) octal and abort the script under set -e.
  prefix="${BASH_REMATCH[5]}"
  (( 10#$prefix >= 1 && 10#$prefix <= 32 )) || return 1
  for o in "${BASH_REMATCH[@]:1:4}"; do
    (( 10#$o <= 255 )) || return 1
  done
}

# preflight asks the panel which node JOIN_TOKEN belongs to and the exact WireGuard
# settings it expects (POST /agent/preflight - the token is NOT redeemed). On success
# sets PF_NAME / PF_PORT / PF_IFACE_ADDR; PF_STATUS is the HTTP status either way.
# Taking these from the panel instead of asking matters: a wrong interface address
# handshakes fine but routes no traffic back to clients, and a listen port that
# differs from the endpoint's port means clients never reach the node at all.
preflight() {
  local resp
  PF_NAME="" PF_PORT="" PF_IFACE_ADDR=""
  resp="$(curl -sk -m 10 -w '\n%{http_code}' -X POST "https://${PANEL_ADDR}/agent/preflight" \
    -H 'Content-Type: application/json' -d "{\"join_token\":\"${JOIN_TOKEN}\"}" 2>/dev/null || true)"
  PF_STATUS="${resp##*$'\n'}"
  [[ "$PF_STATUS" == 200 ]] || return 1
  resp="${resp%$'\n'*}"
  PF_NAME="$(grep -o '"name":"[^"]*"' <<<"$resp" | head -n1 | cut -d'"' -f4 || true)"
  PF_PORT="$(grep -o '"wg_listen_port":[0-9]*' <<<"$resp" | cut -d: -f2 || true)"
  PF_IFACE_ADDR="$(grep -o '"wg_interface_address":"[^"]*"' <<<"$resp" | cut -d'"' -f4 || true)"
  # Only use what would also pass the manual prompts' validation; anything else
  # (a legacy endpoint without a port -> 0) falls back to asking.
  [[ "$PF_NAME" =~ ^[A-Za-z0-9._-]+$ ]] || PF_NAME=""
  { [[ "$PF_PORT" =~ ^[0-9]+$ ]] && (( 10#$PF_PORT >= 1 && 10#$PF_PORT <= 65535 )); } || PF_PORT=""
  valid_cidr4 "$PF_IFACE_ADDR" || PF_IFACE_ADDR=""
  return 0
}

# write_env_file persists the answers for docker compose (shared by both modes).
write_env_file() {
  cat > "$ENV_FILE" <<EOF
NODE_IMAGE=${NODE_IMAGE}
WGPANEL_PANEL_ADDR=${PANEL_ADDR}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PORT=${WG_PORT}
WG_IFACE_ADDR=${WG_IFACE_ADDR}
EOF
  chmod 600 "$ENV_FILE"
}

# config_from_env is the non-interactive twin of prompt_config: same validation, but
# a bad value is a hard failure (exit 13/14) instead of a re-prompt.
config_from_env() {
  PF_NAME="" PF_PORT="" PF_IFACE_ADDR="" PF_STATUS=""
  mkdir -p "$NODE_DIR"
  PANEL_ADDR="${NODE_PANEL_ADDR:-}"
  JOIN_TOKEN="${NODE_JOIN_TOKEN:-}"
  PANEL_ADDR="${PANEL_ADDR#http://}"
  PANEL_ADDR="${PANEL_ADDR#https://}"
  PANEL_ADDR="${PANEL_ADDR%%/*}"
  if [[ "$PANEL_ADDR" != *:* ]] || [[ -z "${PANEL_ADDR%:*}" ]] ||
     [[ ! "${PANEL_ADDR##*:}" =~ ^[0-9]+$ ]] ||
     (( 10#${PANEL_ADDR##*:} < 1 || 10#${PANEL_ADDR##*:} > 65535 )); then
    err "NODE_PANEL_ADDR must be host:port with a numeric port (the panel's NODE_AGENT_PORT)."
    exit 13
  fi
  if [[ ! "$JOIN_TOKEN" =~ ^[A-Za-z0-9_-]+$ ]]; then
    err "NODE_JOIN_TOKEN is missing or malformed."
    exit 13
  fi
  if ! timeout 5 bash -c ": </dev/tcp/${PANEL_ADDR%:*}/${PANEL_ADDR##*:}" 2>/dev/null; then
    warn "Cannot reach ${PANEL_ADDR} over TCP - registration will probably fail."
  fi
  if preflight; then
    log "Join token accepted for node '${PF_NAME:-?}'."
  else
    case "$PF_STATUS" in
      401) err "The panel rejected this join token: it is invalid, expired, or already used."; exit 14 ;;
      404) warn "This panel version can't report the node's WireGuard settings - using NODE_* values." ;;
      *)   warn "Could not check the token with the panel (HTTP ${PF_STATUS:-none}) - continuing." ;;
    esac
  fi
  NODE_NAME="${PF_NAME:-${NODE_NAME:-}}"
  [[ "$NODE_NAME" =~ ^[A-Za-z0-9._-]+$ ]] || { err "No valid node name (the panel did not supply one and NODE_NAME is unset)."; exit 13; }
  WG_PORT="${PF_PORT:-${NODE_WG_PORT:-51820}}"
  { [[ "$WG_PORT" =~ ^[0-9]+$ ]] && (( 10#$WG_PORT >= 1 && 10#$WG_PORT <= 65535 )); } || { err "Invalid WireGuard port: ${WG_PORT}"; exit 13; }
  WG_IFACE="${NODE_WG_IFACE:-wg0}"
  [[ "$WG_IFACE" =~ ^[A-Za-z0-9_=+.-]{1,15}$ ]] || { err "Invalid WireGuard interface name: ${WG_IFACE}"; exit 13; }
  WG_IFACE_ADDR="${PF_IFACE_ADDR:-${NODE_WG_IFACE_ADDR:-}}"
  valid_cidr4 "$WG_IFACE_ADDR" || { err "No valid WireGuard interface address (the panel did not supply one and NODE_WG_IFACE_ADDR is unset)."; exit 13; }
  write_env_file
}

prompt_config() {
  local probe
  if [[ "$NONINTERACTIVE" == 1 ]]; then
    config_from_env
    return
  fi
  PF_NAME="" PF_PORT="" PF_IFACE_ADDR="" PF_STATUS=""
  mkdir -p "$NODE_DIR"

  # The agent dials https://<addr>/agent/* - that's the panel's NODE_AGENT_PORT
  # (48443 by default), NOT the panel web UI port and NOT WireGuard's UDP port.
  # Getting this wrong is the most common install mistake, so probe it over TCP
  # before accepting the answer.
  while true; do
    read_required "Control plane address (host:port - the panel's NODE_AGENT_PORT, e.g. panel.example.com:48443): " PANEL_ADDR
    # The agent prepends https:// itself, so a pasted URL scheme or path would
    # produce https://https://... and break registration - strip both.
    PANEL_ADDR="${PANEL_ADDR#http://}"
    PANEL_ADDR="${PANEL_ADDR#https://}"
    PANEL_ADDR="${PANEL_ADDR%%/*}"
    if [[ "$PANEL_ADDR" != *:* ]] || [[ -z "${PANEL_ADDR%:*}" ]] ||
       [[ ! "${PANEL_ADDR##*:}" =~ ^[0-9]+$ ]] ||
       (( 10#${PANEL_ADDR##*:} < 1 || 10#${PANEL_ADDR##*:} > 65535 )); then
      warn "Expected host:port with a numeric port, e.g. panel.example.com:48443."
      continue
    fi
    if ! timeout 5 bash -c ": </dev/tcp/${PANEL_ADDR%:*}/${PANEL_ADDR##*:}" 2>/dev/null; then
      warn "Cannot reach ${PANEL_ADDR} over TCP. Check the address: the port must be the"
      warn "panel's node-agent port (NODE_AGENT_PORT in the panel's .env, 48443 by default),"
      warn "not the WireGuard port, and it must be open in the panel server's firewall."
      read -rp "Use ${PANEL_ADDR} anyway? [y/N]: " CONFIRM
      [[ "${CONFIRM,,}" == y* ]] && break
      continue
    fi
    # Reachable is not enough: the panel's WEB port (443) also answers TCP. The real
    # agent endpoint replies with plain text/JSON, never HTML - an HTML answer means
    # this is the web UI and registration would die with an nginx "405 Not Allowed".
    # Buffered in a variable rather than piped to grep -q: under pipefail, grep's
    # early exit can SIGPIPE curl and turn a positive match into a skipped warning.
    probe="$(curl -skm 5 "https://${PANEL_ADDR}/agent/register" 2>/dev/null || true)"
    if grep -qiE '<html|<!doctype' <<<"$probe"; then
      warn "${PANEL_ADDR} answers like the panel's WEB UI, not the node-agent API."
      warn "Enter the panel's node-agent port instead: NODE_AGENT_PORT in the panel"
      warn "server's /opt/wgpanel/.env (48443 by default) - e.g. ${PANEL_ADDR%:*}:48443."
      read -rp "Use ${PANEL_ADDR} anyway? [y/N]: " CONFIRM
      [[ "${CONFIRM,,}" == y* ]] && break
      continue
    fi
    break
  done

  while true; do
    read_required "Join token (from admin panel -> Nodes -> Add Node): " JOIN_TOKEN
    # Tokens are hex; anything else (a stray quote, a space from a bad paste) would
    # also break the JSON bodies built from it.
    if [[ ! "$JOIN_TOKEN" =~ ^[A-Za-z0-9_-]+$ ]]; then
      warn "That doesn't look like a join token - copy it again from the panel."
      continue
    fi
    if preflight; then
      log "Join token accepted for node '${PF_NAME:-?}'."
      break
    fi
    case "$PF_STATUS" in
      401)
        warn "The panel rejected this join token: it is invalid, expired, or already used."
        warn "Generate a new one in the panel (Nodes -> the node -> Generate Token). If this server"
        warn "is already registered and you are only reinstalling it, you can continue - it keeps"
        warn "its existing registration."
        ;;
      404)
        warn "This panel version can't report the node's WireGuard settings - you'll be asked for them."
        break
        ;;
      000|"")
        warn "Could not reach the panel at ${PANEL_ADDR} to check the token."
        ;;
      *)
        warn "Could not check the token with the panel (HTTP ${PF_STATUS})."
        ;;
    esac
    read -rp "Continue with this token anyway? [y/N]: " CONFIRM
    [[ "${CONFIRM,,}" == y* ]] && break
  done

  if [[ -n "$PF_NAME" ]]; then
    read_default "A name for this node [${PF_NAME}]: " NODE_NAME "$PF_NAME"
  else
    read_required "A name for this node (e.g. de-frankfurt-1): " NODE_NAME
  fi

  if [[ -n "$PF_PORT" ]]; then
    WG_PORT="$PF_PORT"
    log "WireGuard listen port: ${WG_PORT} (the port of this node's public endpoint in the panel)."
  else
    while true; do
      read -rp "WireGuard listen port - must be the port in this node's public endpoint in the panel [51820]: " WG_PORT
      WG_PORT="${WG_PORT//$'\r'/}"
      WG_PORT=${WG_PORT:-51820}
      [[ "$WG_PORT" =~ ^[0-9]+$ ]] && (( 10#$WG_PORT >= 1 && 10#$WG_PORT <= 65535 )) && break
      warn "The port must be a number between 1 and 65535."
    done
  fi

  # Linux caps interface names at 15 chars (IFNAMSIZ); slashes/spaces would also
  # break the wg-quick config path inside the container.
  while true; do
    read -rp "WireGuard interface name [wg0]: " WG_IFACE
    WG_IFACE="${WG_IFACE//$'\r'/}"
    WG_IFACE=${WG_IFACE:-wg0}
    [[ "$WG_IFACE" =~ ^[A-Za-z0-9_=+.-]{1,15}$ ]] && break
    warn "Interface names must be 1-15 characters (letters, digits, . _ = + -)."
  done

  if [[ -n "$PF_IFACE_ADDR" ]]; then
    WG_IFACE_ADDR="$PF_IFACE_ADDR"
    log "WireGuard interface address: ${WG_IFACE_ADDR} (from this node's subnet in the panel)."
  else
    # The agent re-checks this against the panel once registered and refuses to run
    # on a mismatch, so a wrong answer here fails loudly instead of silently.
    while true; do
      read_required "This node's own WireGuard interface address: the .1 of the node's subnet in the panel (Nodes list), with its prefix, e.g. 10.66.0.1/24: " WG_IFACE_ADDR
      valid_cidr4 "$WG_IFACE_ADDR" && break
      warn "Expected a valid IPv4 address with a prefix length, e.g. 10.66.0.1/24."
    done
  fi

  write_env_file
}

write_compose() {
  # Bridge networking (not host): the container terminates WireGuard and NATs client
  # traffic to the internet inside its OWN network namespace, so it never has to touch
  # the host's Docker-managed FORWARD/DOCKER-USER chains - the exact model the in-repo
  # dev node is verified against. Only the WireGuard UDP port is published to the host.
  cat > "$COMPOSE_FILE" <<'EOF'
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
    # AmneziaWG (obfuscation profiles) runs in userspace and needs the TUN device;
    # plain WireGuard keeps using the kernel module and never touches it.
    devices:
      - /dev/net/tun:/dev/net/tun
    security_opt:
      - no-new-privileges:true
    sysctls:
      - net.ipv4.ip_forward=1
    env_file:
      - .env
    ports:
      - "${WGPANEL_WG_PORT:-51820}:${WGPANEL_WG_PORT:-51820}/udp"
    volumes:
      - node_wg_state:/etc/wireguard
      - node_agent_state:/etc/wgpanel/state
    deploy:
      resources:
        limits:
          memory: ${NODE_MEM_LIMIT:-256m}

volumes:
  node_wg_state:
  node_agent_state:
EOF
}

# Pull the published node image; if that fails (private package, or CI hasn't
# published it yet), build it from source so the install still completes.
ensure_image() {
  if [[ -n "${WGPANEL_NODE_BUILD:-}" ]]; then
    build_image_from_source
    return
  fi
  log "Pulling node image ${NODE_IMAGE} ..."
  if (cd "$NODE_DIR" && docker compose pull); then
    return
  fi
  warn "Could not pull ${NODE_IMAGE} - falling back to building the image from source."
  build_image_from_source
}

build_image_from_source() {
  local src="/tmp/wgpanel-src.$$" branch=()
  # Build the chosen release; with a custom NODE_IMAGE there is none, so the default branch.
  [[ -n "$RELEASE" ]] && branch=(--branch "$RELEASE")
  log "Cloning ${WGPANEL_REPO_URL} ${RELEASE:-(default branch)} and building the node image locally..."
  rm -rf "$src"
  git clone --depth 1 ${branch[@]+"${branch[@]}"} "$WGPANEL_REPO_URL" "$src"
  docker build -f "$src/deploy/node.Dockerfile" -t wgpanel-node:local "$src"
  rm -rf "$src"
  # Repoint the compose at the locally-built image.
  sed -i 's#^NODE_IMAGE=.*#NODE_IMAGE=wgpanel-node:local#' "$ENV_FILE"
  NODE_IMAGE="wgpanel-node:local"
}

setup_firewall() {
  log "Configuring firewall (ufw)..."
  # Never lock out SSH: the OpenSSH app profile only exists when openssh-server is
  # installed from apt, and sshd may listen on a custom port - "ufw allow OpenSSH"
  # silently failing followed by "ufw enable" (default deny incoming) would cut this
  # session off. Allow the ports sshd is actually listening on as well.
  ufw allow OpenSSH >/dev/null 2>&1 || true
  local ssh_port
  while read -r ssh_port; do
    [[ -n "$ssh_port" ]] && ufw allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
  done < <(ss -tlnpH 2>/dev/null | awk '/sshd/ {n = split($4, a, ":"); print a[n]}' | sort -u)
  # A broken ufw/iptables ("ERROR: problem running iptables/ufw-init" - classically a
  # kernel upgraded without a reboot, leaving the running kernel unable to load
  # iptables modules) must not abort the install this late. The node works without
  # the rule; the port just has to be opened once ufw is healthy again.
  if ! ufw allow "${WG_PORT}/udp"; then
    warn "ufw could not add the ${WG_PORT}/udp rule (see the error above) - continuing anyway."
    warn "Clients can't connect until UDP ${WG_PORT} is open. If the error mentions iptables,"
    warn "a reboot usually fixes it (pending kernel upgrade); then run: ufw allow ${WG_PORT}/udp"
  fi
  # ufw's default FORWARD policy is DROP, which also drops the traffic Docker forwards
  # for the node container's clients (container bridge -> host egress). Set it to ACCEPT
  # so Docker's own specific per-bridge FORWARD rules govern forwarding - without this,
  # clients handshake but get no internet. (The node's client-subnet -> egress
  # MASQUERADE itself lives inside the container; see node-entrypoint.sh.)
  if [[ -f /etc/default/ufw ]]; then
    sed -i 's/^DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw
  fi
  yes | ufw enable >/dev/null 2>&1 || true
  ufw reload >/dev/null 2>&1 || true
}

start_node() {
  log "Starting the node container..."
  # --force-recreate: on a re-install with an unchanged .env, `up -d` would leave the
  # old container (and its old logs) running untouched - verify() would then read
  # stale failure lines from a previous run instead of this one.
  (cd "$NODE_DIR" && docker compose up -d --force-recreate)
}

# warn_client_dns surfaces the agent's client_dns_unreachable check (cmd/agent's
# checkClientDNS): clients resolve names through this node, so resolvers it can't
# reach mean "connected but no internet". Shared verbatim with install.sh.
warn_client_dns() {
  local line
  line="$(grep -m1 '"msg":"client_dns_unreachable"' <<<"$1" || true)"
  [[ -n "$line" ]] || return 0
  warn "The DNS servers clients are given (panel: Settings -> Client DNS) don't answer from this"
  warn "server: $(grep -o '"unreachable":\[[^]]*\]' <<<"$line" | cut -d: -f2 || true)"
  warn "Clients will connect but can't resolve names. In the panel, set Client DNS to \"Each node"
  warn "resolves for its clients\" (the default), or to resolvers this server can reach."
}

# The container being "running" says nothing about registration: a bad join token or
# wrong panel address makes the agent crash-loop while restart: unless-stopped keeps
# the container alive - the old 3-second check reported success anyway. Watch the
# agent's own log markers instead (cmd/agent). Success is the first accepted
# heartbeat, not the "registered" line: on a reinstall the agent may carry on with a
# saved registration the panel no longer accepts (node deleted), which only the
# heartbeat reveals.
verify() {
  log "Waiting for the node to register with the control plane..."
  local logs deadline=$((SECONDS + 45))
  while (( SECONDS < deadline )); do
    sleep 3
    logs="$(cd "$NODE_DIR" && docker compose logs --no-color 2>/dev/null || true)"
    if grep -q '"msg":"fatal"' <<<"$logs"; then
      err "The node agent failed to start - last log lines:"
      (cd "$NODE_DIR" && docker compose logs --no-color --tail 5) >&2 || true
      err "The usual causes are a wrong/expired join token, a wrong control-plane address, or a"
      err "WireGuard interface address that doesn't match the node's subnet (the error says which)."
      err "Fix .env in ${NODE_DIR}, then run: cd ${NODE_DIR} && docker compose up -d --force-recreate"
      exit 1
    fi
    if grep -qE '"msg":"heartbeat_rejected","status":40[14]' <<<"$logs"; then
      err "The control plane rejected this node's saved registration from a previous install"
      err "(the node was deleted in the panel, or registered again from another server)."
      err "Generate a new join token for this node in the panel (or add it again), then re-run install-node.sh with it."
      exit 1
    fi
    if grep -q '"msg":"heartbeat_ok"' <<<"$logs"; then
      if grep -q '"msg":"join_token_rejected_using_existing_registration"' <<<"$logs"; then
        warn "The join token you entered was rejected, so this server kept its existing registration"
        warn "from a previous install - that registration is still valid and in use."
      elif grep -q '"reused":true' <<<"$logs"; then
        log "Reusing this server's existing registration from a previous install."
      fi
      if grep -q '"msg":"wg_listen_port_mismatch"' <<<"$logs"; then
        warn "WireGuard listens on ${WG_PORT}/udp, but this node's public endpoint in the panel uses"
        warn "another port - clients dial that one. Fix the endpoint (Nodes -> Edit) unless a"
        warn "port-forward in front of this server maps it to ${WG_PORT}."
      fi
      warn_client_dns "$logs"
      log "Node registered and reporting to the control plane. Follow it with:"
      log "    cd ${NODE_DIR} && docker compose logs -f"
      return
    fi
  done
  if (cd "$NODE_DIR" && docker compose ps --status running --format '{{.Name}}' | grep -q .); then
    warn "Node container is running but registration is not confirmed yet. Follow it with:"
    warn "    cd ${NODE_DIR} && docker compose logs -f"
  else
    err "Node container failed to start. Check logs with: cd ${NODE_DIR} && docker compose logs"
    exit 1
  fi
}

# >>> wgpanel version (this block is identical in install.sh, install-node.sh and wgpanel; deploy/check-net-tune-sync.sh enforces it)
# Releases are git tags (vX.Y.Z), and CI publishes ghcr.io/iamfarhad/wgpanel-{api,frontend,node}
# under the same tag. Installs pin that tag instead of :latest, so a plain `docker compose
# pull` never changes the running version behind the operator's back, and `wgpanel
# rollback` always has a concrete previous version to go back to.
WGPANEL_GITHUB_REPO="${WGPANEL_GITHUB_REPO:-iamfarhad/WGPanel}"
WGPANEL_IMAGE_REGISTRY="${WGPANEL_IMAGE_REGISTRY:-ghcr.io/iamfarhad}"

# version_normalize accepts "0.1.0" or "v0.1.0" (with an optional -rc.1 style suffix)
# and prints it the way the tags are written, with the leading v.
version_normalize() {
  local v="${1#v}"
  [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || return 1
  printf 'v%s\n' "$v"
}

# version_list prints the stable release tags (no pre-releases), oldest first.
version_list() {
  if command -v git >/dev/null 2>&1; then
    git ls-remote --tags --refs "https://github.com/${WGPANEL_GITHUB_REPO}.git" 'v*' 2>/dev/null \
      | sed -n 's#.*refs/tags/##p'
  else
    curl -fsSL --max-time 15 "https://api.github.com/repos/${WGPANEL_GITHUB_REPO}/tags?per_page=100" 2>/dev/null \
      | sed -n 's/.*"name": *"\([^"]*\)".*/\1/p'
  fi | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V
}

# version_latest prints the newest release. GitHub's releases/latest skips drafts and
# pre-releases; the highest stable tag is the fallback when the API is unreachable or
# rate-limited (60 unauthenticated calls an hour per IP).
version_latest() {
  local tag=""
  tag="$(curl -fsSL --max-time 15 "https://api.github.com/repos/${WGPANEL_GITHUB_REPO}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)" || true
  [[ -n "$tag" ]] || tag="$(version_list | tail -n1)" || true
  [[ -n "$tag" ]] || return 1
  printf '%s\n' "$tag"
}

# version_exists returns 0 when TAG is a published tag, 2 when GitHub says it isn't,
# and 1 when GitHub can't be reached to tell.
version_exists() {
  local rc=0 code=""
  if command -v git >/dev/null 2>&1; then
    git ls-remote --exit-code --tags --refs "https://github.com/${WGPANEL_GITHUB_REPO}.git" "refs/tags/$1" >/dev/null 2>&1 || rc=$?
    case "$rc" in 0) return 0 ;; 2) return 2 ;; *) return 1 ;; esac
  fi
  code="$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "https://api.github.com/repos/${WGPANEL_GITHUB_REPO}/git/ref/tags/$1" 2>/dev/null)" || true
  case "$code" in 200) return 0 ;; 404) return 2 ;; *) return 1 ;; esac
}

# version_resolve prints the release to use: the newest one for "" or "latest", or the
# requested one once GitHub confirms the tag exists. Messages go to stderr, so callers
# can capture the result with $(...).
version_resolve() {
  local want="${1:-latest}" tag rc=0
  if [[ "$want" == latest ]]; then
    tag="$(version_latest)" || {
      err "Could not look up the latest WGPanel release - is github.com reachable? Pass a version (e.g. --version v0.1.0) to pin one."
      return 1
    }
  else
    tag="$(version_normalize "$want")" || { err "'$want' is not a version - use a release tag such as v0.1.0."; return 1; }
    version_exists "$tag" || rc=$?
    if [[ $rc -eq 2 ]]; then
      err "$tag is not a WGPanel release. Recent releases: $(version_list | tail -n 5 | tr '\n' ' ')"
      return 1
    elif [[ $rc -ne 0 ]]; then
      warn "Could not reach GitHub to confirm $tag exists - continuing; pulling its images will fail if it doesn't." >&2
    fi
  fi
  printf '%s\n' "$tag"
}

# image_ref prints the pinned image for one component: api, frontend or node.
image_ref() { printf '%s/wgpanel-%s:%s\n' "$WGPANEL_IMAGE_REGISTRY" "$1" "$2"; }

# image_tag prints the tag of an image reference, or nothing when it has none.
image_tag() {
  local last="${1%%@*}"
  last="${last##*/}"
  [[ "$last" == *:* ]] && printf '%s\n' "${last##*:}"
  return 0
}

# deploy_file_url prints the raw GitHub URL of deploy/NAME at git ref REF.
deploy_file_url() { printf 'https://raw.githubusercontent.com/%s/%s/deploy/%s\n' "$WGPANEL_GITHUB_REPO" "$1" "$2"; }

# env_set_key sets KEY=VALUE in an env file: it replaces the existing line, or appends
# one when the key is missing.
env_set_key() {
  local file="$1" key="$2" val="$3"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    sed -i "s#^${key}=.*#${key}=${val}#" "$file"
  else
    printf '%s=%s\n' "$key" "$val" >> "$file"
  fi
}
# <<< wgpanel version

# >>> wgpanel net-tune (this block is identical in install.sh, install-node.sh and wgpanel; deploy/check-net-tune-sync.sh enforces it)
# Host network tuning for WireGuard throughput: BBR + fq, bigger socket buffers and
# backlog, a conntrack table sized to RAM, TCP MTU probing, and (when the NIC supports
# it) UDP GRO forwarding. Idempotent, and never fatal: an unsupported knob is skipped.
NET_TUNE_CONF="/etc/sysctl.d/90-wgpanel-net.conf"
NET_TUNE_UNIT="/etc/systemd/system/wgpanel-net-tune.service"
NET_TUNE_NIC_SCRIPT="/usr/local/sbin/wgpanel-net-tune-nic"
NET_TUNE_PREV="/var/lib/wgpanel/net-tune.prev"

net_tune_default_iface() {
  ip route show default 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit }}'
}

# ~65536 entries per GB of RAM, clamped to 32768..262144.
net_tune_conntrack_max() {
  local mem_kb max
  mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null)"
  max=$(( ${mem_kb:-0} / 16 ))
  (( max < 32768 )) && max=32768
  (( max > 262144 )) && max=262144
  echo "$max"
}

net_tune_bbr_available() {
  grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null && return 0
  modprobe tcp_bbr >/dev/null 2>&1 || true
  grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null
}

# Target sysctls as "key value" lines - the single source for apply, status and revert.
net_tune_targets() {
  if net_tune_bbr_available; then
    echo "net.core.default_qdisc fq"
    echo "net.ipv4.tcp_congestion_control bbr"
  fi
  echo "net.core.rmem_max 16777216"
  echo "net.core.wmem_max 16777216"
  echo "net.ipv4.tcp_rmem 4096 131072 16777216"
  echo "net.ipv4.tcp_wmem 4096 65536 16777216"
  echo "net.core.netdev_max_backlog 16384"
  echo "net.netfilter.nf_conntrack_max $(net_tune_conntrack_max)"
  echo "net.ipv4.tcp_mtu_probing 1"
}

net_tune_render() {
  echo "# Managed by wgpanel (wgpanel tune). Remove with: wgpanel tune revert"
  local key value
  while read -r key value; do
    # The leading "-" tells sysctl to ignore a key this kernel does not have.
    echo "-${key} = ${value}"
  done < <(net_tune_targets)
}

net_tune_nic_script() {
  cat <<'NICEOF'
#!/bin/sh
# Installed by wgpanel (wgpanel tune): UDP GRO forwarding on the default-route NIC.
dev="$(ip route show default 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit }}')"
[ -n "$dev" ] || exit 0
command -v ethtool >/dev/null 2>&1 || exit 0
ethtool -k "$dev" 2>/dev/null | grep -q '^rx-udp-gro-forwarding:' || exit 0
ethtool -K "$dev" rx-udp-gro-forwarding on rx-gro-list off >/dev/null 2>&1 || true
exit 0
NICEOF
}

net_tune_unit() {
  cat <<UNITEOF
[Unit]
Description=WGPanel NIC tuning (UDP GRO forwarding)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=${NET_TUNE_NIC_SCRIPT}

[Install]
WantedBy=multi-user.target
UNITEOF
}

# net_tune_write PATH MODE: stdin -> PATH, only touching the file when content changed.
net_tune_write() {
  local path="$1" mode="$2" tmp
  tmp="$(mktemp)" || return 1
  cat > "$tmp"
  if [[ -f "$path" ]] && cmp -s "$tmp" "$path"; then
    rm -f "$tmp"
    return 0
  fi
  install -D -m "$mode" "$tmp" "$path"
  rm -f "$tmp"
}

net_tune_apply() {
  log "Applying network tuning (BBR, socket buffers, conntrack, MTU probing)..."
  modprobe nf_conntrack >/dev/null 2>&1 || true

  # Remember the pre-tuning values once, so `wgpanel tune revert` can restore them.
  if [[ ! -f "$NET_TUNE_PREV" ]]; then
    local key value cur
    mkdir -p "$(dirname "$NET_TUNE_PREV")" 2>/dev/null || true
    while read -r key value; do
      cur="$(sysctl -n "$key" 2>/dev/null | tr '\t' ' ')" || cur=""
      [[ -n "$cur" ]] && echo "${key} ${cur}"
    done < <(net_tune_targets) > "$NET_TUNE_PREV" 2>/dev/null || true
  fi

  if net_tune_render | net_tune_write "$NET_TUNE_CONF" 644; then
    sysctl -q -p "$NET_TUNE_CONF" >/dev/null 2>&1 || warn "Some network settings are not supported by this kernel and were skipped."
  else
    warn "Could not write ${NET_TUNE_CONF} - skipping sysctl tuning."
  fi

  if ! command -v ethtool >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y ethtool >/dev/null 2>&1 || true
  fi
  if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    if net_tune_nic_script | net_tune_write "$NET_TUNE_NIC_SCRIPT" 755 && net_tune_unit | net_tune_write "$NET_TUNE_UNIT" 644; then
      systemctl daemon-reload >/dev/null 2>&1 || true
      systemctl enable wgpanel-net-tune.service >/dev/null 2>&1 || true
      systemctl restart wgpanel-net-tune.service >/dev/null 2>&1 || warn "NIC tuning service did not start (the NIC may not support UDP GRO forwarding) - continuing."
    else
      warn "Could not install the NIC tuning service - continuing."
    fi
  else
    warn "systemd not available - skipped the NIC tuning service."
  fi
  return 0
}

net_tune_status() {
  local key value cur mark iface
  printf '%-36s %-26s %-26s\n' "SETTING" "CURRENT" "TARGET"
  while read -r key value; do
    cur="$(sysctl -n "$key" 2>/dev/null | tr '\t' ' ')" || cur=""
    mark=" "
    [[ "$cur" == "$value" ]] && mark="*"
    printf '%-36s %-26s %-26s %s\n' "$key" "${cur:-n/a}" "$value" "$mark"
  done < <(net_tune_targets)
  echo "(* = already at target)"
  if [[ -f "$NET_TUNE_CONF" ]]; then
    echo "sysctl file: ${NET_TUNE_CONF} (present)"
  else
    echo "sysctl file: ${NET_TUNE_CONF} (absent - run: wgpanel tune apply)"
  fi
  iface="$(net_tune_default_iface)"
  if [[ -n "$iface" ]] && command -v ethtool >/dev/null 2>&1; then
    echo "NIC ${iface}: $(ethtool -k "$iface" 2>/dev/null | grep -E '^(rx-udp-gro-forwarding|rx-gro-list):' | tr -s ' ' | tr '\n' ' ')"
  else
    echo "NIC: ethtool not available"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    echo "wgpanel-net-tune.service: $(systemctl is-enabled wgpanel-net-tune.service 2>/dev/null || echo not-installed)"
  fi
}

net_tune_revert() {
  local key value
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now wgpanel-net-tune.service >/dev/null 2>&1 || true
  fi
  rm -f "$NET_TUNE_UNIT" "$NET_TUNE_NIC_SCRIPT" "$NET_TUNE_CONF"
  command -v systemctl >/dev/null 2>&1 && { systemctl daemon-reload >/dev/null 2>&1 || true; }
  if [[ -f "$NET_TUNE_PREV" ]]; then
    while read -r key value; do
      sysctl -q -w "${key}=${value}" >/dev/null 2>&1 || true
    done < "$NET_TUNE_PREV"
    rm -f "$NET_TUNE_PREV"
    log "Network tuning removed and the previous kernel values restored."
  else
    log "Network tuning files removed (no saved values; kernel settings return to default on reboot)."
  fi
  return 0
}
# <<< wgpanel net-tune

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --update) UPDATE=1; shift ;;
      --version)
        [[ $# -ge 2 ]] || { err "--version needs a value, e.g. --version v0.1.0"; exit 1; }
        REQUESTED_VERSION="$2"; shift 2 ;;
      --version=*) REQUESTED_VERSION="${1#--version=}"; shift ;;
      # Already read by the early loop at the top (it has to be known before any
      # prompt); accepted here so this parser doesn't reject it.
      --non-interactive|-y) shift ;;
      -h|--help)
        echo "Usage: sudo bash install-node.sh [--update] [--version vX.Y.Z] [--non-interactive]"
        echo "  --version   Release to install, e.g. v0.1.0 (default: the newest release)."
        echo "              Use the same version as the panel (wgpanel version on the panel server)."
        echo "  --update    Move an already installed node to that release, keeping its"
        echo "              registration - no join token needed."
        echo "  --non-interactive, -y"
        echo "              Never prompt: settings come from NODE_* variables (see the header)."
        exit 0
        ;;
      *) err "Unknown argument: $1 (try --help)"; exit 1 ;;
    esac
  done
}

# resolve_node_image pins NODE_IMAGE to the chosen release, unless NODE_IMAGE was set
# explicitly in the environment.
resolve_node_image() {
  if [[ -n "$NODE_IMAGE" ]]; then
    log "Using node image ${NODE_IMAGE} (from NODE_IMAGE)."
    return
  fi
  log "Looking up the WGPanel release to install (${REQUESTED_VERSION})..."
  RELEASE="$(version_resolve "$REQUESTED_VERSION")" || exit 1
  NODE_IMAGE="$(image_ref node "$RELEASE")"
  log "Node version: ${RELEASE} (${NODE_IMAGE})"
}

# update_node moves an installed node to another release: only NODE_IMAGE changes, so
# the node keeps its registration, keys and settings. If the new image can't be
# pulled, the previous one is put back and nothing restarts.
update_node() {
  local current
  if [[ ! -f "$ENV_FILE" || ! -f "$COMPOSE_FILE" ]]; then
    err "No node install found in ${NODE_DIR} - run install-node.sh without --update to install one."
    exit 1
  fi
  resolve_node_image
  current="$(grep -m1 '^NODE_IMAGE=' "$ENV_FILE" | cut -d= -f2- || true)"
  # verify() reports a listen-port mismatch with WG_PORT, which a fresh install gets
  # from prompt_config; here it comes from the saved settings.
  WG_PORT="$(grep -m1 '^WGPANEL_WG_PORT=' "$ENV_FILE" | cut -d= -f2- || true)"
  if [[ "$current" == "$NODE_IMAGE" ]]; then
    log "This node already runs ${NODE_IMAGE} - nothing to do."
    return
  fi
  log "Updating the node: ${current:-unknown} -> ${NODE_IMAGE}"
  env_set_key "$ENV_FILE" NODE_IMAGE "$NODE_IMAGE"
  if ! (cd "$NODE_DIR" && docker compose pull); then
    env_set_key "$ENV_FILE" NODE_IMAGE "$current"
    err "Could not pull ${NODE_IMAGE} - kept ${current}. A release tagged minutes ago may still be publishing in CI."
    exit 1
  fi
  start_node
  verify
  log "Node updated to ${NODE_IMAGE}. To go back: sudo bash install-node.sh --update --version $(image_tag "$current")"
}

main() {
  parse_args "$@"
  require_root
  detect_os
  if [[ "$UPDATE" -eq 1 ]]; then
    update_node
    return
  fi
  install_prereqs
  install_docker
  resolve_node_image
  prompt_config
  write_compose
  ensure_image
  setup_firewall
  net_tune_apply || true
  start_node
  verify
  log "Done. This node should appear as 'online' in the admin panel's Nodes list shortly, with its WireGuard public key already set."
  log "Manage it with: cd ${NODE_DIR} && docker compose {ps|logs|restart|pull|up -d|down}"
}

main "$@"
