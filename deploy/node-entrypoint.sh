#!/usr/bin/env bash
#
# Containerized equivalent of install-node.sh's setup_wireguard() - generates a
# wg0.conf on first boot (persisted via the wg_node_state volume so keys survive
# container recreation), brings the interface up, then execs the agent. No
# DOCKER-USER chain handling here (unlike install-node.sh): that chain only exists
# on a host that also runs the Docker daemon itself, not inside this container's
# own network namespace.
set -euo pipefail

WG_IFACE="${WGPANEL_WG_INTERFACE:-wg0}"
WG_PORT="${WGPANEL_WG_PORT:-51820}"
WG_CONF="/etc/wireguard/${WG_IFACE}.conf"
WG_IFACE_ADDR="${WG_IFACE_ADDR:-10.66.0.1/24}"

egress_iface="$(ip route show default | awk '{print $5; exit}')"
egress_iface="${egress_iface:-eth0}"

# MSS clamping: forwarded TCP is clamped to the path MTU, so a tunnel MTU below the
# client's native one (or a relay in front) never produces black-holed large packets.
# The mangle rules end in `|| true` on purpose: wg-quick aborts `up` when a PostUp
# command fails, and an IPv6-less kernel (no ip6table_mangle) must not take the node
# down. The matching -D rules in PostDown are tolerant for the same reason.
MSS_ARGS='FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu'

postup_rules() {
  echo "iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o ${egress_iface} -j MASQUERADE; iptables -t mangle -A ${MSS_ARGS} || true; ip6tables -t mangle -A ${MSS_ARGS} || true"
}

postdown_rules() {
  echo "iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o ${egress_iface} -j MASQUERADE; iptables -t mangle -D ${MSS_ARGS} || true; ip6tables -t mangle -D ${MSS_ARGS} || true"
}

mkdir -p /etc/wireguard
chmod 700 /etc/wireguard

# Via /proc, not sysctl(8): the image has no procps, so the old `sysctl -w` always
# failed and printed this warning even with forwarding correctly enabled by compose.
if [[ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null)" != 1 ]] &&
   ! { echo 1 > /proc/sys/net/ipv4/ip_forward; } 2>/dev/null; then
  echo "node-entrypoint: ip_forward is off and can't be set in-container - make sure the 'sysctls: [net.ipv4.ip_forward=1]' compose setting is applied" >&2
fi

if [[ ! -f "$WG_CONF" ]]; then
  umask 077
  wg genkey > "/etc/wireguard/${WG_IFACE}-private.key"
  wg pubkey < "/etc/wireguard/${WG_IFACE}-private.key" > "/etc/wireguard/${WG_IFACE}-public.key"

  cat > "$WG_CONF" <<EOF
[Interface]
PrivateKey = $(cat "/etc/wireguard/${WG_IFACE}-private.key")
Address = ${WG_IFACE_ADDR}
ListenPort = ${WG_PORT}
PostUp = $(postup_rules)
PostDown = $(postdown_rules)
EOF
  chmod 600 "$WG_CONF"
else
  # The conf outlives the container (state volume), so env changes would otherwise
  # never apply: a re-install with a new WGPANEL_WG_PORT publishes the new port on
  # the host while the interface keeps listening on the old one. Sync the mutable
  # fields on every boot; the generated keys are left untouched. '#' as the sed
  # delimiter because the address value contains '/'.
  # PostUp/PostDown are re-rendered too, so existing nodes pick up the MSS-clamp rules
  # (and a changed egress interface) on their next restart.
  sed -i \
    -e "s#^Address = .*#Address = ${WG_IFACE_ADDR}#" \
    -e "s#^ListenPort = .*#ListenPort = ${WG_PORT}#" \
    -e "s#^PostUp = .*#PostUp = $(postup_rules)#" \
    -e "s#^PostDown = .*#PostDown = $(postdown_rules)#" \
    "$WG_CONF"
fi

# Idempotent across container restarts and crashes: if a stale interface or leftover
# routes survived (e.g. an unclean stop), tear it down first so `up` starts clean
# instead of failing with "Device or resource busy".
wg-quick down "$WG_IFACE" 2>/dev/null || true
wg-quick up "$WG_IFACE"

WGPANEL_WG_PUBLIC_KEY="$(cat "/etc/wireguard/${WG_IFACE}-public.key")"
export WGPANEL_WG_PUBLIC_KEY

exec /usr/local/bin/wgpanel-agent
