#!/usr/bin/env bash
#
# WGPanel - Node agent installer
# Run this on EACH server that will actually run WireGuard and terminate customer connections.
# The agent connects OUTBOUND to the control plane (no inbound access to the panel needed),
# applies peer changes via wgctrl, and streams traffic stats back over HTTPS+mTLS.
#
# Usage:
#   sudo bash install-node.sh
#
# You will be asked for:
#   - the control-plane address (e.g. panel.example.com:9090)
#   - a one-time join token, generated from the admin panel: Nodes -> Add Node -> Generate Token
#
set -euo pipefail

AGENT_BIN="/usr/local/bin/wgpanel-agent"
AGENT_DIR="/etc/wgpanel"
AGENT_ENV="$AGENT_DIR/agent.env"
# Replace with your published release URL once the agent binary is built by CI.
AGENT_RELEASE_BASE_URL="https://github.com/yourorg/wgpanel/releases/latest/download"

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
  log "Installing WireGuard tools and base packages..."
  apt-get update -y
  apt-get install -y wireguard wireguard-tools curl ufw

  if ! modprobe wireguard 2>/dev/null; then
    warn "Could not load the wireguard kernel module directly (kernel may need wireguard-dkms, or you're on a kernel with it built in already). Continuing."
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64)  echo "amd64" ;;
    aarch64) echo "arm64" ;;
    *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
  esac
}

install_agent_binary() {
  local arch
  arch="$(detect_arch)"
  log "Downloading node agent (${arch})..."
  curl -fsSL "${AGENT_RELEASE_BASE_URL}/wgpanel-agent-linux-${arch}" -o "$AGENT_BIN"
  chmod +x "$AGENT_BIN"
}

configure_agent() {
  mkdir -p "$AGENT_DIR"

  read -rp "Control plane address (host:port, e.g. panel.example.com:9090): " PANEL_ADDR
  read -rp "Join token (from admin panel -> Nodes -> Add Node): " JOIN_TOKEN
  read -rp "A name for this node (e.g. de-frankfurt-1): " NODE_NAME
  read -rp "WireGuard listen port [51820]: " WG_PORT
  WG_PORT=${WG_PORT:-51820}
  read -rp "WireGuard interface name [wg0]: " WG_IFACE
  WG_IFACE=${WG_IFACE:-wg0}

  cat > "$AGENT_ENV" <<EOF
WGPANEL_PANEL_ADDR=${PANEL_ADDR}
WGPANEL_JOIN_TOKEN=${JOIN_TOKEN}
WGPANEL_NODE_NAME=${NODE_NAME}
WGPANEL_WG_INTERFACE=${WG_IFACE}
WGPANEL_WG_PORT=${WG_PORT}
WGPANEL_STATE_DIR=${AGENT_DIR}/state
EOF
  chmod 600 "$AGENT_ENV"
  mkdir -p "${AGENT_DIR}/state"

  export WG_PORT WG_IFACE
}

# setup_wireguard creates the actual server-side WireGuard interface - until now
# this script only installed wireguard-tools and the agent, never a real interface
# (docs/PRD-node-management.md §5's "public_key" field, added in STORY-03, had
# nothing populating it end-to-end). The generated public key is written into
# agent.env so the agent submits it during /agent/register (STORY-04's endpoint),
# closing the loop automatically rather than requiring a manual copy-paste into the
# admin panel - see backend/internal/httpapi/agentserver.go's WGPublicKey handling.
setup_wireguard() {
  local wg_conf_dir="/etc/wireguard"
  local wg_conf="${wg_conf_dir}/${WG_IFACE}.conf"
  mkdir -p "$wg_conf_dir"
  chmod 700 "$wg_conf_dir"

  if [[ -f "$wg_conf" ]]; then
    warn "${wg_conf} already exists - leaving its identity (keys/address/port) untouched."
    WG_PUBLIC_KEY="$(wg pubkey < "${wg_conf_dir}/${WG_IFACE}-private.key" 2>/dev/null || true)"
    # Still worth patching in NAT/forwarding fixes added to this script after this
    # config was first generated - "leave it untouched" was never meant to mean
    # "never benefit from a real bug fix again." Idempotent (checks the marker
    # string first).
    if ! grep -q 'DOCKER-USER' "$wg_conf"; then
      log "Applying a newer NAT/forwarding fix to the existing ${WG_IFACE} config..."
      cat >> "$wg_conf" <<'EOF'
PostUp = iptables -I DOCKER-USER -i %i -j ACCEPT; iptables -I DOCKER-USER -o %i -j ACCEPT
PostDown = iptables -D DOCKER-USER -i %i -j ACCEPT; iptables -D DOCKER-USER -o %i -j ACCEPT
EOF
      systemctl restart "wg-quick@${WG_IFACE}" 2>/dev/null || warn "Could not restart wg-quick@${WG_IFACE} automatically - restart it manually to apply."
    fi
    return
  fi

  read -rp "This node's own WireGuard interface address, with prefix (must be the .1 address of the subnet you configured for this node in the panel, e.g. 10.66.0.1/24): " WG_IFACE_ADDR

  umask 077
  wg genkey > "${wg_conf_dir}/${WG_IFACE}-private.key"
  wg pubkey < "${wg_conf_dir}/${WG_IFACE}-private.key" > "${wg_conf_dir}/${WG_IFACE}-public.key"
  WG_PUBLIC_KEY="$(cat "${wg_conf_dir}/${WG_IFACE}-public.key")"

  # Generated client configs use AllowedIPs = 0.0.0.0/0 (full-tunnel) - without IP
  # forwarding + NAT on this box, a connected client gets a handshake but no actual
  # internet access through the tunnel (found the hard way testing a real client
  # connection - this was previously missing from every real install, not just a
  # test-environment gap). PostUp/PostDown mirror wg-quick's own documented pattern.
  local egress_iface
  egress_iface="$(ip route show default | awk '{print $5; exit}')"
  if [[ -z "$egress_iface" ]]; then
    warn "Could not detect a default route interface - full-tunnel client internet access won't work until you add NAT manually. Continuing without it."
  fi

  cat > "$wg_conf" <<EOF
[Interface]
PrivateKey = $(cat "${wg_conf_dir}/${WG_IFACE}-private.key")
Address = ${WG_IFACE_ADDR}
ListenPort = ${WG_PORT}
EOF
  if [[ -n "$egress_iface" ]]; then
    # A plain `-A FORWARD` ACCEPT rule is not enough on any host where Docker is
    # also installed (true here - install.sh/install-node.sh always install Docker
    # before this runs): modern Docker versions insert their own DOCKER-USER and
    # DOCKER-FORWARD chains ahead of it in the FORWARD chain, and traffic through a
    # non-Docker interface like wg0 gets intercepted there first - confirmed live,
    # the wg0 ACCEPT rule showed 0 matched packets while DOCKER-FORWARD had already
    # processed thousands. DOCKER-USER is the one chain Docker guarantees it will
    # never overwrite, which is exactly what it's for.
    cat >> "$wg_conf" <<EOF
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -I DOCKER-USER -i %i -j ACCEPT; iptables -I DOCKER-USER -o %i -j ACCEPT; iptables -t nat -A POSTROUTING -o ${egress_iface} -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -D DOCKER-USER -i %i -j ACCEPT; iptables -D DOCKER-USER -o %i -j ACCEPT; iptables -t nat -D POSTROUTING -o ${egress_iface} -j MASQUERADE
EOF
  fi
  chmod 600 "$wg_conf"

  echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-wgpanel-forward.conf
  sysctl -p /etc/sysctl.d/99-wgpanel-forward.conf >/dev/null

  systemctl enable --now "wg-quick@${WG_IFACE}"

  log "WireGuard interface ${WG_IFACE} is up. Its public key is:"
  echo
  echo "  ${WG_PUBLIC_KEY}"
  echo
  log "This will be submitted automatically when the agent registers - no manual copy-paste needed."

  # Append rather than regenerate agent.env wholesale - configure_agent already wrote it.
  echo "WGPANEL_WG_PUBLIC_KEY=${WG_PUBLIC_KEY}" >> "$AGENT_ENV"
}

setup_firewall() {
  log "Configuring firewall (ufw)..."
  ufw allow OpenSSH >/dev/null 2>&1 || true
  ufw allow "${WG_PORT}/udp"
  # ufw's own default FORWARD policy is DROP - that overrides wg-quick's PostUp
  # FORWARD ACCEPT rule (setup_wireguard above) for packets ufw's chains see first,
  # which is the other half of why full-tunnel client traffic silently went nowhere
  # even with NAT configured. Without this, ip_forward+MASQUERADE alone isn't enough.
  if [[ -f /etc/default/ufw ]]; then
    sed -i 's/^DEFAULT_FORWARD_POLICY=.*/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw
  fi
  yes | ufw enable >/dev/null 2>&1 || true
  ufw reload >/dev/null 2>&1 || true
}

install_systemd_service() {
  cat > /etc/systemd/system/wgpanel-agent.service <<EOF
[Unit]
Description=WGPanel node agent
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

verify() {
  sleep 2
  if systemctl is-active --quiet wgpanel-agent; then
    log "Agent service is running. Check registration status with: journalctl -u wgpanel-agent -f"
  else
    err "Agent failed to start. Check logs with: journalctl -u wgpanel-agent -e"
    exit 1
  fi
}

main() {
  require_root
  detect_os
  install_prereqs
  install_agent_binary
  configure_agent
  setup_wireguard
  setup_firewall
  install_systemd_service
  verify
  log "Done. This node should now appear as 'online' in the admin panel's Nodes list, with its WireGuard public key already set."
}

main "$@"
