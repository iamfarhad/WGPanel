package main

import (
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestGlobalV6Source(t *testing.T) {
	cases := map[string]bool{
		"2a01:4f8::1":    true,  // global unicast
		"fd12:3456::1":   false, // ULA: the host itself is behind NAT66
		"fe80::1":        false, // link-local
		"::1":            false,
		"::ffff:1.2.3.4": false, // v4-mapped
	}
	for in, want := range cases {
		if got := globalV6Source(netip.MustParseAddr(in)); got != want {
			t.Errorf("globalV6Source(%s) = %v, want %v", in, got, want)
		}
	}
}

// fakeV6Host records every action; failProc/failRun make the matching one fail, and
// present marks iptables rules as already installed (so -C succeeds).
type fakeV6Host struct {
	procs    map[string]string
	runs     []string
	failProc string
	failRun  string
	present  map[string]bool
}

func (f *fakeV6Host) WriteProc(path, value string) error {
	if f.failProc != "" && strings.Contains(path, f.failProc) {
		return errors.New("read-only /proc/sys")
	}
	if f.procs == nil {
		f.procs = map[string]string{}
	}
	f.procs[path] = value
	f.runs = append(f.runs, "write "+path+"="+value)
	return nil
}

func (f *fakeV6Host) Run(argv ...string) error {
	cmd := strings.Join(argv, " ")
	f.runs = append(f.runs, cmd)
	if f.failRun != "" && strings.Contains(cmd, f.failRun) {
		return errors.New("boom")
	}
	if len(argv) > 1 && argv[0] == "ip6tables" {
		for i, a := range argv {
			if a == "-C" {
				key := strings.Join(append(append([]string(nil), argv[:i]...), argv[i+1:]...), " ")
				if f.present[key] {
					return nil
				}
				return errors.New("no such rule")
			}
		}
	}
	return nil
}

var testV6 = heartbeatIPv6{Enabled: true, Prefix: "fd12:3456:789a:1::/64", Gateway: "fd12:3456:789a:1::1/64"}

func TestApplyIPv6Sequence(t *testing.T) {
	h := &fakeV6Host{}
	if err := applyIPv6(h, "wg0", "eth0", testV6); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(h.runs, "\n")
	// accept_ra must be set BEFORE forwarding, or the host loses its default route.
	ra := strings.Index(all, "write /proc/sys/net/ipv6/conf/eth0/accept_ra=2")
	fwd := strings.Index(all, "write /proc/sys/net/ipv6/conf/all/forwarding=1")
	if ra < 0 || fwd < 0 || ra > fwd {
		t.Fatalf("accept_ra=2 must precede enabling forwarding:\n%s", all)
	}
	for _, want := range []string{
		"ip -6 addr replace fd12:3456:789a:1::1/64 dev wg0",
		"ip6tables -w -t nat -A POSTROUTING -s fd12:3456:789a:1::/64 ! -o wg0 -j MASQUERADE",
		"ip6tables -w -A FORWARD -i wg0 -j ACCEPT",
		"-t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
}

func TestApplyIPv6IsIdempotentOnRules(t *testing.T) {
	h := &fakeV6Host{present: map[string]bool{}}
	for _, r := range v6Rules("wg0", testV6.Prefix) {
		// Mark each rule as installed: the check argv minus the -C op is the key.
		for i, a := range r.check {
			if a == "-C" {
				h.present[strings.Join(append(append([]string(nil), r.check[:i]...), r.check[i+1:]...), " ")] = true
			}
		}
	}
	if err := applyIPv6(h, "wg0", "eth0", testV6); err != nil {
		t.Fatal(err)
	}
	for _, run := range h.runs {
		if strings.Contains(run, " -A ") {
			t.Fatalf("rule re-added although -C found it: %s", run)
		}
	}
}

func TestApplyIPv6ReportsFailures(t *testing.T) {
	if err := applyIPv6(&fakeV6Host{failProc: "forwarding"}, "wg0", "eth0", testV6); err == nil || !strings.Contains(err.Error(), "net.ipv6.conf.all.forwarding") {
		t.Fatalf("a read-only /proc must yield an actionable error, got %v", err)
	}
	if err := applyIPv6(&fakeV6Host{failRun: "MASQUERADE"}, "wg0", "eth0", testV6); err == nil {
		t.Fatal("a failing NAT rule must fail the apply")
	}
	bad := testV6
	bad.Gateway = "nonsense"
	if err := applyIPv6(&fakeV6Host{}, "wg0", "eth0", bad); err == nil {
		t.Fatal("an invalid gateway must be rejected")
	}
}

func TestRemoveIPv6(t *testing.T) {
	h := &fakeV6Host{}
	removeIPv6(h, "wg0", testV6)
	all := strings.Join(h.runs, "\n")
	if !strings.Contains(all, "-D POSTROUTING -s fd12:3456:789a:1::/64") || !strings.Contains(all, "ip -6 addr del fd12:3456:789a:1::1/64 dev wg0") {
		t.Fatalf("teardown incomplete:\n%s", all)
	}
}

func TestIPv6StateSyncRules(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// An older control plane (nil instruction) never makes the node report ready.
	h := &fakeV6Host{}
	st := &ipv6State{host: h}
	st.sync("wg0", nil, logger)
	if st.ready || len(h.runs) != 0 {
		t.Fatal("no instruction: must do nothing and not be ready")
	}
	// Disabled and nothing applied: still nothing to do.
	st.sync("wg0", &heartbeatIPv6{}, logger)
	if st.ready || len(h.runs) != 0 {
		t.Fatal("disabled: must do nothing")
	}
}

func TestPeerSignatureIncludesPSK(t *testing.T) {
	a := []heartbeatPeer{{PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32"}}}
	b := []heartbeatPeer{{PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32"}, PresharedKey: "psk"}}
	c := []heartbeatPeer{{PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32", "fd00::2/128"}}}
	if peerSignature(a) == peerSignature(b) {
		t.Fatal("a PSK appearing must change the signature, or it would never be applied")
	}
	if peerSignature(a) == peerSignature(c) {
		t.Fatal("a v6 address appearing must change the signature")
	}
}

func TestPeerConfigFor(t *testing.T) {
	pub, _ := wgtypes.GeneratePrivateKey()
	psk, _ := wgtypes.GenerateKey()
	pc, ok := peerConfigFor(heartbeatPeer{
		PublicKey:    pub.PublicKey().String(),
		AllowedIPs:   []string{"10.0.0.2/32", "fd00::2/128", "garbage"},
		PresharedKey: psk.String(),
	})
	if !ok || len(pc.AllowedIPs) != 2 || pc.PresharedKey == nil || *pc.PresharedKey != psk {
		t.Fatalf("peerConfigFor = %+v ok=%v", pc, ok)
	}
	pc, ok = peerConfigFor(heartbeatPeer{PublicKey: pub.PublicKey().String(), AllowedIPs: []string{"10.0.0.2/32"}})
	if !ok || pc.PresharedKey != nil {
		t.Fatal("a peer without a PSK must be configured without one")
	}
	if _, ok := peerConfigFor(heartbeatPeer{PublicKey: "not-a-key"}); ok {
		t.Fatal("a malformed public key must be skipped")
	}
}
