package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

// IPv6 egress for the node's clients (opt-in per node in the panel). The agent owns the
// node-side setup so it works the same on the container and on any host that runs the
// agent: the panel sends the node's ULA /64 in the heartbeat response; the agent
//
//   - puts the node's address (index 1 of the /64) on the tunnel interface,
//   - turns on IPv6 forwarding (and accept_ra=2 on the egress interface first - with
//     forwarding on, the kernel otherwise drops router advertisements and the host loses
//     its default route and global address),
//   - installs NAT66 (masquerade of the ULA prefix) plus the FORWARD accepts and the TCP
//     MSS clamp, each idempotently (-C before -A),
//
// and reports back whether the host has a global IPv6 route (ipv6_global) and whether
// all of the above is in place (ipv6_ready). The panel renders IPv6 for the node only
// when it is enabled AND ready.

// heartbeatIPv6 mirrors the control plane's agentIPv6.
type heartbeatIPv6 struct {
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix"`
	Gateway string `json:"gateway"`
}

// v6Probe is a well-known public IPv6 address used only to ask the kernel which source
// address and route it would pick; a UDP "connect" sends no packet.
const v6Probe = "[2001:4860:4860::8888]:53"

// globalV6Source reports whether addr is a source address that proves real IPv6
// connectivity: global unicast, not link-local and not unique-local (a ULA source means
// the host itself sits behind NAT66, which is not something to build a second NAT on).
func globalV6Source(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.Is6() && addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLinkLocalUnicast()
}

// detectGlobalIPv6 returns the interface the host would use for global IPv6 and whether
// there is one.
func detectGlobalIPv6() (egress string, ok bool) {
	conn, err := net.Dial("udp6", v6Probe)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	ua, isUDP := conn.LocalAddr().(*net.UDPAddr)
	if !isUDP {
		return "", false
	}
	local, parsed := netip.AddrFromSlice(ua.IP)
	if !parsed || !globalV6Source(local) {
		return "", false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", true
	}
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, isNet := a.(*net.IPNet); isNet && ipn.IP.Equal(ua.IP) {
				return ifc.Name, true
			}
		}
	}
	return "", true
}

// v6Rule is one iptables rule installed idempotently: add only if check says it is
// missing.
type v6Rule struct {
	check, add, del []string
}

func v6Rules(wgIface, prefix string) []v6Rule {
	mk := func(table string, spec ...string) v6Rule {
		pre := []string{"ip6tables", "-w"}
		if table != "" {
			pre = append(pre, "-t", table)
		}
		with := func(op string) []string {
			return append(append(append([]string(nil), pre...), op), spec...)
		}
		return v6Rule{check: with("-C"), add: with("-A"), del: with("-D")}
	}
	return []v6Rule{
		// NAT66: the clients' ULA addresses leave as the node's own global address.
		mk("nat", "POSTROUTING", "-s", prefix, "!", "-o", wgIface, "-j", "MASQUERADE"),
		mk("", "FORWARD", "-i", wgIface, "-j", "ACCEPT"),
		mk("", "FORWARD", "-o", wgIface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"),
		// Same clamp node-entrypoint.sh installs for IPv4/IPv6 at wg-quick up; -C makes this
		// a no-op there and a real rule on installs that predate it.
		mk("mangle", "FORWARD", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"),
	}
}

// v6Host abstracts what applyIPv6 does to the machine, so the sequencing is testable.
type v6Host interface {
	WriteProc(path, value string) error
	Run(argv ...string) error
}

type realV6Host struct{}

func (realV6Host) WriteProc(path, value string) error {
	return os.WriteFile(path, []byte(value), 0o644)
}

func (realV6Host) Run(argv ...string) error {
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w (%s)", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyIPv6 makes the node ready for IPv6 egress. Any failing step aborts with an error
// (the node then reports not-ready and the next tick retries).
func applyIPv6(h v6Host, wgIface, egress string, want heartbeatIPv6) error {
	if _, err := netip.ParsePrefix(want.Gateway); err != nil {
		return fmt.Errorf("invalid gateway %q: %w", want.Gateway, err)
	}
	if egress != "" {
		// Before forwarding is switched on, or the default route is lost.
		if err := h.WriteProc("/proc/sys/net/ipv6/conf/"+egress+"/accept_ra", "2"); err != nil {
			return fmt.Errorf("set accept_ra=2 on %s: %w", egress, err)
		}
	}
	if err := h.WriteProc("/proc/sys/net/ipv6/conf/"+wgIface+"/disable_ipv6", "0"); err != nil {
		return fmt.Errorf("enable IPv6 on %s (in a container: add sysctl net.ipv6.conf.all.disable_ipv6=0): %w", wgIface, err)
	}
	if err := h.WriteProc("/proc/sys/net/ipv6/conf/all/forwarding", "1"); err != nil {
		return fmt.Errorf("enable IPv6 forwarding (in a container: add sysctl net.ipv6.conf.all.forwarding=1): %w", err)
	}
	if err := h.Run("ip", "-6", "addr", "replace", want.Gateway, "dev", wgIface); err != nil {
		return err
	}
	for _, r := range v6Rules(wgIface, want.Prefix) {
		if h.Run(r.check...) == nil {
			continue
		}
		if err := h.Run(r.add...); err != nil {
			return err
		}
	}
	return nil
}

// removeIPv6 undoes what applyIPv6 added to the node (best effort: each step may have
// nothing to remove). Forwarding is left on - harmless, and the host may rely on it.
func removeIPv6(h v6Host, wgIface string, last heartbeatIPv6) {
	for _, r := range v6Rules(wgIface, last.Prefix) {
		_ = h.Run(r.del...)
	}
	if last.Gateway != "" {
		_ = h.Run("ip", "-6", "addr", "del", last.Gateway, "dev", wgIface)
	}
}

// ipv6State tracks what has been applied across heartbeat ticks.
type ipv6State struct {
	host v6Host
	// applied is the last desired state that was successfully applied (Enabled=false
	// zero value = nothing applied).
	applied heartbeatIPv6
	// reportedGlobal / ready are what the next heartbeat reports.
	reportedGlobal bool
	ready          bool
}

// sync reconciles the node with the panel's instruction. want == nil is an older panel:
// leave IPv6 alone. Detection runs every tick (it is a single route lookup) so a host
// that gains or loses connectivity is reported within ~10s.
func (s *ipv6State) sync(wgIface string, want *heartbeatIPv6, logger *slog.Logger) {
	if s.host == nil {
		s.host = realV6Host{}
	}
	egress, global := detectGlobalIPv6()
	s.reportedGlobal = global
	if want == nil {
		s.ready = false
		return
	}

	switch {
	case !want.Enabled:
		if s.applied.Enabled {
			removeIPv6(s.host, wgIface, s.applied)
			logger.Info("ipv6_disabled")
			s.applied = heartbeatIPv6{}
		}
		s.ready = false
	case !global:
		// Enabled but the host cannot reach the IPv6 internet: do not hand the clients
		// an address that would black-hole their v6 traffic.
		s.ready = false
	default:
		if s.applied != *want {
			if s.applied.Enabled {
				removeIPv6(s.host, wgIface, s.applied) // prefix changed
			}
			if err := applyIPv6(s.host, wgIface, egress, *want); err != nil {
				logger.Warn("ipv6_apply_failed", "error", err)
				s.ready = false
				return
			}
			s.applied = *want
			logger.Info("ipv6_applied", "prefix", want.Prefix, "egress", egress)
		}
		s.ready = true
	}
}
