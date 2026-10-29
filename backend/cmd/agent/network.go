package main

import (
	"fmt"
	"log/slog"
	"net"

	"golang.zx2c4.com/wireguard/wgctrl"
)

// nodeNetwork mirrors the control plane's nodeNetwork (internal/httpapi/nodenetwork.go):
// what this node's WireGuard interface must look like for the peer configs the panel
// hands out to work. Returned by /agent/register and persisted next to the identity
// so every later start can re-check it; empty when the panel predates it.
type nodeNetwork struct {
	WGSubnet           string `json:"wg_subnet"`
	WGInterfaceAddress string `json:"wg_interface_address"`
	PublicEndpoint     string `json:"public_endpoint"`
	WGListenPort       int    `json:"wg_listen_port"`
}

// checkNodeNetwork compares the real interface against want before the agent starts
// syncing peers. A wrong interface address is fatal: peers are added via wgctrl, which
// installs no routes, so the only route back to clients is the one wg-quick derives
// from Address - with the wrong subnet there, clients handshake fine but every reply
// leaves via eth0 and they get no traffic at all. A listen-port mismatch only warns:
// a port-forward in front of the node can legitimately map a different public port.
func checkNodeNetwork(cfg config, want nodeNetwork, logger *slog.Logger) error {
	if want.WGInterfaceAddress == "" {
		return nil // older control plane, or a registration from before this check existed
	}

	iface, err := net.InterfaceByName(cfg.WGInterface)
	if err != nil {
		logger.Warn("wg_interface_check_skipped", "interface", cfg.WGInterface, "error", err)
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		logger.Warn("wg_interface_check_skipped", "interface", cfg.WGInterface, "error", err)
		return nil
	}
	if err := interfaceAddressMatches(want.WGInterfaceAddress, addrs); err != nil {
		return fmt.Errorf("WireGuard interface %s %w, but this node's subnet in the panel is %s - clients would connect but get no traffic back. Set WG_IFACE_ADDR=%s in the node's .env and recreate the container (docker compose up -d --force-recreate)",
			cfg.WGInterface, err, want.WGSubnet, want.WGInterfaceAddress)
	}

	if want.WGListenPort != 0 {
		if port, ok := readListenPort(cfg.WGInterface); ok && port != want.WGListenPort {
			logger.Warn("wg_listen_port_mismatch",
				"listen_port", port, "public_endpoint", want.PublicEndpoint,
				"hint", "clients dial the public endpoint's port - fix the endpoint in the panel unless a port-forward maps it to the listen port")
		}
	}
	return nil
}

// interfaceAddressMatches reports (as an error describing what IS there) whether none
// of addrs is exactly want - same address AND prefix length, since a narrower prefix
// would leave part of the peer subnet unrouted.
func interfaceAddressMatches(want string, addrs []net.Addr) error {
	wantIP, wantNet, err := net.ParseCIDR(want)
	if err != nil {
		return fmt.Errorf("cannot be checked against %q: %w", want, err)
	}
	wantOnes, _ := wantNet.Mask.Size()

	var have []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil {
			continue
		}
		ones, _ := ipNet.Mask.Size()
		if ipNet.IP.Equal(wantIP) && ones == wantOnes {
			return nil
		}
		have = append(have, ipNet.String())
	}
	if len(have) == 0 {
		return fmt.Errorf("has no IPv4 address (expected %s)", want)
	}
	return fmt.Errorf("has address %v (expected %s)", have, want)
}

func readListenPort(iface string) (int, bool) {
	client, err := wgctrl.New()
	if err != nil {
		return 0, false
	}
	defer client.Close()

	device, err := client.Device(iface)
	if err != nil {
		return 0, false
	}
	return device.ListenPort, true
}
