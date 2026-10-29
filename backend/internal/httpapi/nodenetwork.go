package httpapi

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"

	"wgpanel-api/internal/ipalloc"
	"wgpanel-api/internal/store"
)

// Subnet size bounds for a node's peer range. /30 is the smallest that still leaves a
// peer address once ipalloc reserves the network, .1 gateway, and broadcast; below /16
// is far beyond any node's capacity and only invites overlap with real networks.
const (
	minWGSubnetPrefix = 16
	maxWGSubnetPrefix = 30
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// normalizePublicEndpoint validates the host:port that lands verbatim in every client
// config's "Endpoint =" line and returns it in canonical form. Both failure modes it
// catches used to be accepted silently: a missing port makes WireGuard apps refuse to
// import the config, and a pasted URL ("https://...") can never resolve.
func normalizePublicEndpoint(raw string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("public_endpoint must be host:port, e.g. vpn1.example.com:51820 (IPv6: [2001:db8::1]:51820)")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", errors.New("public_endpoint port must be a number between 1 and 65535 (the node's WireGuard listen port)")
	}
	if net.ParseIP(host) == nil && !hostnamePattern.MatchString(host) {
		return "", errors.New("public_endpoint host must be an IP address or a hostname, without a scheme or path")
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), nil
}

// normalizeWGSubnet validates a node's peer subnet and returns it in network form
// ("10.66.0.5/24" -> "10.66.0.0/24"), so the subnet stored, the "in use" check in the
// panel, and the interface address derived from it (ipalloc.Gateway) all agree.
// IPv4 only: ipalloc's broadcast math is IPv4-specific.
func normalizeWGSubnet(raw string) (string, error) {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil || ipNet.IP.To4() == nil {
		return "", errors.New("wg_subnet must be an IPv4 CIDR, e.g. 10.66.0.0/24")
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 || ones < minWGSubnetPrefix || ones > maxWGSubnetPrefix {
		return "", errors.New("wg_subnet prefix length must be between /16 and /30")
	}
	return ipNet.String(), nil
}

// nodeNetwork is what a node's own WireGuard interface must look like for the peer
// configs the panel hands out to work: the interface address is exactly the address
// ipalloc reserves for the node (the subnet's .1, see ipalloc.Gateway), and the listen
// port is the one clients dial in public_endpoint. A wrong interface address is the
// nasty case - clients handshake fine but no traffic is ever routed back to them.
// Served by /agent/preflight (install-node.sh fills its answers from it) and
// /agent/register (the agent refuses to run on a mismatched interface).
type nodeNetwork struct {
	WGSubnet           string `json:"wg_subnet"`
	WGInterfaceAddress string `json:"wg_interface_address"`
	PublicEndpoint     string `json:"public_endpoint"`
	// WGListenPort is public_endpoint's port; 0 when a legacy endpoint has none.
	WGListenPort int `json:"wg_listen_port"`
}

func nodeNetworkFor(n store.Node) nodeNetwork {
	nn := nodeNetwork{WGSubnet: n.WGSubnet, PublicEndpoint: n.PublicEndpoint}
	if addr, err := ipalloc.Gateway(n.WGSubnet); err == nil {
		nn.WGInterfaceAddress = addr
	}
	if _, port, err := net.SplitHostPort(n.PublicEndpoint); err == nil {
		nn.WGListenPort, _ = strconv.Atoi(port)
	}
	return nn
}

// validateClientDNS accepts the client_dns setting: "node" (see resolveClientDNS), or
// a wg-quick DNS = list - resolver IPs, optionally with search domains, at least one IP.
func validateClientDNS(raw string) error {
	if raw == nodeClientDNS {
		return nil
	}
	resolvers := 0
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		switch {
		case net.ParseIP(entry) != nil:
			resolvers++
		case hostnamePattern.MatchString(entry):
			// search domain
		default:
			return errors.New(`client_dns must be "node" or comma-separated resolver IPs (optionally with search domains), e.g. 1.1.1.1, 1.0.0.1`)
		}
	}
	if resolvers == 0 {
		return errors.New(`client_dns needs at least one resolver IP, or "node"`)
	}
	return nil
}
