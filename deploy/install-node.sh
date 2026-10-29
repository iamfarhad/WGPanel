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
#   sudo bash install-node.sh
#
# You will be asked for:
#   - the control-plane address (e.g. panel.example.com:48443)
#   - a one-time join token, generated from the admin panel: Nodes -> Add Node -> Generate Token
# The WireGuard listen port and interface address are taken from the node's settings
# in the panel (they must match it exactly); you are only asked for them if the
# panel can't be queried.
#
# The node image is pulled from $NODE_IMAGE (GHCR by default). If it can't be pulled
# (private package, or before CI has published it), the script falls back to building
# it from source - override the source with WGPANEL_REPO_URL.
set -euo pipefail

NODE_DIR="/opt/wgpanel-node"
COMPOSE_FILE="$NODE_DIR/docker-compose.yml"
ENV_FILE="$NODE_DIR/.env"
NODE_IMAGE="${NODE_IMAGE:-ghcr.io/iamfarhad/wgpanel-node:latest}"
WGPANEL_REPO_URL="${WGPANEL_REPO_URL:-https://github.com/iamfarhad/WGPanel.git}"

log()  { echo -e "\033[1;32m[wgpanel-node]\033[0m $*"; }
warn() { echo -e "\033[1;33m[wgpanel-node]\033[0m $*"; }
err()  { echo -e "\033[1;31m[wgpanel-node]\033[0m $*" >&2; }

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    err "Please run as root (sudo bash install-node.sh)"
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
  curl -fsSL https://get.docker.com | sh
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

prompt_config() {
  local probe
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
  local src="/tmp/wgpanel-src.$$"
  log "Cloning ${WGPANEL_REPO_URL} and building the node image locally..."
  rm -rf "$src"
  git clone --depth 1 "$WGPANEL_REPO_URL" "$src"
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

main() {
  require_root
  detect_os
  install_prereqs
  install_docker
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
