package main

import (
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
)

func mustAddrs(t *testing.T, cidrs ...string) []net.Addr {
	t.Helper()
	var out []net.Addr
	for _, c := range cidrs {
		ip, ipNet, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		ipNet.IP = ip
		out = append(out, ipNet)
	}
	return out
}

func TestInterfaceAddressMatches(t *testing.T) {
	if err := interfaceAddressMatches("10.67.0.1/24", mustAddrs(t, "fe80::1/64", "10.67.0.1/24")); err != nil {
		t.Fatalf("expected a match, got %v", err)
	}

	mismatches := map[string][]net.Addr{
		"wrong subnet":      mustAddrs(t, "10.66.0.1/24"),
		"not the .1":        mustAddrs(t, "10.67.0.254/24"),
		"narrower prefix":   mustAddrs(t, "10.67.0.1/25"),
		"no IPv4 addresses": mustAddrs(t, "fe80::1/64"),
		"no addresses":      nil,
	}
	for name, addrs := range mismatches {
		if err := interfaceAddressMatches("10.67.0.1/24", addrs); err == nil {
			t.Errorf("%s: expected a mismatch error", name)
		}
	}
}

// loopbackWithIPv4 finds this machine's loopback interface and its IPv4 address in
// CIDR form ("127.0.0.1/8"), standing in for wg0 so the check runs on any machine.
func loopbackWithIPv4(t *testing.T) (string, string) {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				return iface.Name, ipNet.String()
			}
		}
	}
	t.Skip("no loopback interface with an IPv4 address")
	return "", ""
}

// A node registered before /agent/register returned its network has nothing saved;
// the first heartbeat carrying it must still catch a wrong interface address.
func TestHeartbeatNetworkCheckCatchesLegacyNodeMismatch(t *testing.T) {
	iface, addr := loopbackWithIPv4(t)
	cfg := config{WGInterface: iface, StateDir: t.TempDir()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := &heartbeatState{}

	wrong := &nodeNetwork{WGSubnet: "10.67.0.0/24", WGInterfaceAddress: "10.67.0.1/24"}
	if err := state.checkNetwork(cfg, wrong, logger); err == nil {
		t.Fatal("expected a fatal error for a mismatched interface address")
	}
	if _, err := os.Stat(statePaths(cfg.StateDir).network); !os.IsNotExist(err) {
		t.Fatal("a mismatched network must not be saved as verified")
	}

	right := &nodeNetwork{WGSubnet: addr, WGInterfaceAddress: addr}
	if err := state.checkNetwork(cfg, right, logger); err != nil {
		t.Fatalf("expected the matching address to pass, got %v", err)
	}
	saved, err := os.ReadFile(statePaths(cfg.StateDir).network)
	if err != nil || !strings.Contains(string(saved), addr) {
		t.Fatalf("expected the verified network to be saved, got %q, %v", saved, err)
	}

	// An older control plane sends no network at all - nothing to check.
	if err := state.checkNetwork(cfg, nil, logger); err != nil {
		t.Fatalf("nil network: %v", err)
	}
}
