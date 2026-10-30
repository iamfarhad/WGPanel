package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// BitTorrent blocking on the forward path, modelled like tc.go: a pure "plan" function
// (buildTorrentRestore) that is unit-tested, and an apply step that runs iptables.
//
// What it is, honestly: a signature filter. It drops forwarded packets from clients
// whose payload contains one of the well-known plaintext BitTorrent markers below. It
// does NOT see inside encrypted or obfuscated BitTorrent (Message Stream Encryption /
// protocol encryption, which most clients enable by default and negotiate with a
// pseudo-random handshake), uTP-over-encryption, or HTTPS trackers, and a client that
// finds its plaintext handshake dropped will often retry with encryption and succeed.
// Treat it as a deterrent that stops casual and misconfigured clients and gives the
// seller a signal (the per-account hit counter), not as a guarantee. Blocking the
// well-known tracker ports or DPI-grade classification is out of scope here.
//
// Layout (all in the filter table, IPv4 and IPv6):
//
//	FORWARD -i wg0 -j WG_TORRENT                    (inserted at position 1)
//	WG_TORRENT      one rule per signature:  -m string --algo bm ... -j WG_TORRENT_HIT
//	WG_TORRENT_HIT  one rule per tunnel IP:  -s 10.66.0.7/32 -j DROP      (counts hits)
//	                final rule:              -j DROP                      (unknown source)
//
// Counting per source IP is the cheap part of the design: the expensive string match
// runs once per packet in WG_TORRENT, but the per-IP rules in WG_TORRENT_HIT are only
// evaluated for packets that already matched a signature - rare - so hundreds of peers
// cost nothing on the normal path, and iptables' own packet counters ARE the per-account
// counters (no NFLOG/LOG parsing, no conntrack). Reading them with -Z zeroes them in the
// same call, so every heartbeat reports a delta.
//
// The string match only looks at the first 512 bytes of each packet: every signature
// sits at the start of the L4 payload (handshake, bencoded DHT message, HTTP request
// line), and bounding the scan keeps the per-packet cost low on busy nodes. Still, it
// is a per-packet payload scan on all client traffic - that is why it is opt-in.

const (
	torrentChain    = "WG_TORRENT"
	torrentHitChain = "WG_TORRENT_HIT"
	// torrentScanBytes bounds the string match (see above).
	torrentScanBytes = 512
)

// torrentSignatures are plaintext markers, matched case-sensitively with Boyer-Moore.
var torrentSignatures = []struct{ name, pattern string }{
	{"bt-handshake", "BitTorrent protocol"}, // \x13BitTorrent protocol, the peer wire handshake
	{"dht-query", "d1:ad2:id20:"},           // bencoded DHT query
	{"dht-reply", "d1:rd2:id20:"},           // bencoded DHT reply
	{"dht-get-peers", "get_peers"},
	{"dht-find-node", "find_node"},
	{"dht-announce", "announce_peer"},
	{"tracker-info-hash", "info_hash="}, // HTTP tracker announce/scrape query strings
	{"tracker-peer-id", "peer_id="},
	{"tracker-announce", "/announce?"},
	{"tracker-scrape", "/scrape?"},
}

// torrentPeerIPs returns the sorted /32 tunnel addresses of the desired peers: the
// sources the per-IP counters are built for. Peers without a /32 are skipped (still
// blocked by the final catch-all, just not attributed).
func torrentPeerIPs(peers []heartbeatPeer) []string {
	var ips []string
	for _, p := range peers {
		if len(p.AllowedIPs) == 0 {
			continue
		}
		if ip, ok := strings.CutSuffix(p.AllowedIPs[0], "/32"); ok && ip != "" {
			ips = append(ips, ip)
		}
	}
	sort.Strings(ips)
	return ips
}

// buildTorrentRestore renders the iptables-restore input (applied with --noflush) that
// (re)defines both chains in one atomic step: declaring an existing user chain in the
// input empties it, so a rebuild never leaves a window with the chain half-populated
// and never touches any rule outside these two chains. peerIPs may be empty (IPv6).
func buildTorrentRestore(peerIPs []string) string {
	var b strings.Builder
	b.WriteString("*filter\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n:%s - [0:0]\n", torrentChain, torrentHitChain)
	for _, sig := range torrentSignatures {
		fmt.Fprintf(&b, "-A %s -m string --algo bm --to %d --string \"%s\" -m comment --comment \"%s\" -j %s\n",
			torrentChain, torrentScanBytes, sig.pattern, sig.name, torrentHitChain)
	}
	for _, ip := range peerIPs {
		fmt.Fprintf(&b, "-A %s -s %s/32 -j DROP\n", torrentHitChain, ip)
	}
	fmt.Fprintf(&b, "-A %s -j DROP\n", torrentHitChain)
	b.WriteString("COMMIT\n")
	return b.String()
}

// torrentJumpArgs is the FORWARD rule (without the -I/-C/-D verb) that enters the chain,
// scoped to packets arriving from the tunnel interface: only client uploads are
// inspected, never the replies coming back to them.
func torrentJumpArgs(iface string) []string {
	return []string{"FORWARD", "-i", iface, "-j", torrentChain}
}

// parseTorrentCounters reads `iptables -nvxL WG_TORRENT_HIT` output into packets per
// source address. The catch-all rule (source 0.0.0.0/0 or ::/0) and the header lines are
// ignored.
func parseTorrentCounters(out string) map[string]int64 {
	counts := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// pkts bytes target prot opt in out source destination
		if len(f) < 9 || f[2] != "DROP" {
			continue
		}
		pkts, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || pkts <= 0 {
			continue
		}
		src := strings.TrimSuffix(f[7], "/32")
		if src == "0.0.0.0/0" || src == "::/0" || src == "anywhere" {
			continue
		}
		counts[src] += pkts
	}
	return counts
}

// torrentHitReport is one tunnel IP's new matched packets, as sent in the heartbeat.
type torrentHitReport struct {
	IP   string `json:"ip"`
	Hits int64  `json:"hits"`
}

// cmdRunner runs a command with optional stdin and returns its combined output; swapped
// for a fake in tests.
type cmdRunner func(stdin, name string, args ...string) (string, error)

func execRunner(stdin, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// torrentState is the agent's torrent-blocking state across heartbeat ticks.
type torrentState struct {
	run cmdRunner
	// installed: the chains currently hold our rules; sig identifies the peer set they
	// were built for, so a changed peer list triggers a rebuild.
	installed bool
	sig       string
	// cleaned: stale rules from a previous agent run were removed at least once.
	cleaned bool
	// pending are hits read from iptables but not yet acknowledged by the control
	// plane (heartbeat failed or is in flight). Keeping them here, instead of relying on
	// the cumulative kernel counters, is what makes a rebuild or a missed heartbeat lose
	// nothing; the price is at-least-once delivery (a heartbeat the panel processed but
	// whose response was lost is counted twice - a small over-count, never an under-count).
	pending map[string]int64
	lastErr string
}

func newTorrentState() torrentState {
	return torrentState{run: execRunner, pending: map[string]int64{}}
}

// ipTablesBinaries are the families the rules are installed for.
var ipTablesBinaries = []struct{ bin, restore string }{
	{"iptables", "iptables-restore"},
	{"ip6tables", "ip6tables-restore"},
}

// collect reads and zeroes the hit counters (IPv4 only: IPv6 has no per-IP rules, the
// panel allocates IPv4 tunnel addresses) into pending, and returns pending as the report
// for the next heartbeat. Cheap no-op while blocking is off.
func (t *torrentState) collect() []torrentHitReport {
	if t.installed {
		t.readCounters()
	}
	out := make([]torrentHitReport, 0, len(t.pending))
	for ip, n := range t.pending {
		if n > 0 {
			out = append(out, torrentHitReport{IP: ip, Hits: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

func (t *torrentState) readCounters() {
	// -Z with -L reads and zeroes the chain in one call, so no packet is counted twice
	// or lost between the read and the reset.
	out, err := t.run("", "iptables", "-w", "-nvxL", torrentHitChain, "-Z")
	if err != nil {
		return
	}
	for ip, n := range parseTorrentCounters(out) {
		t.pending[ip] += n
	}
}

// acked removes what a successful heartbeat delivered.
func (t *torrentState) acked(sent []torrentHitReport) {
	for _, s := range sent {
		t.pending[s.IP] -= s.Hits
		if t.pending[s.IP] <= 0 {
			delete(t.pending, s.IP)
		}
	}
}

// sync makes the node match the control plane's wish. enable == nil means the control
// plane does not know the field (older panel): leave everything as it is.
func (t *torrentState) sync(iface string, enable *bool, peers []heartbeatPeer, logger *slog.Logger) {
	if enable == nil {
		return
	}
	if !*enable {
		if t.installed || !t.cleaned {
			t.readCounters() // don't drop hits that happened since the last tick
			t.remove(iface)
			if t.installed {
				logger.Info("torrent_block_removed")
			}
			t.installed, t.sig, t.cleaned = false, "", true
		}
		return
	}

	ips := torrentPeerIPs(peers)
	sig := strings.Join(ips, ",")
	if t.installed && sig == t.sig {
		t.ensureJump(iface) // somebody flushing FORWARD must not silently disable us
		return
	}
	if t.installed {
		t.readCounters() // the rebuild below empties the per-IP counters
	}
	// Best effort: bare-metal nodes have modprobe; the node container does not (the host
	// kernel autoloads xt_string/nft_compat on first use, which the smoke test confirmed).
	_ = exec.Command("modprobe", "xt_string").Run()
	if err := t.apply(iface, ips); err != nil {
		if msg := err.Error(); msg != t.lastErr {
			t.lastErr = msg
			logger.Warn("torrent_block_failed", "error", msg,
				"hint", "needs the xt_string kernel module (iptables -m string): run `modprobe xt_string` on the node host")
		}
		return
	}
	t.lastErr = ""
	if !t.installed {
		logger.Info("torrent_block_applied", "tracked_peers", len(ips), "signatures", len(torrentSignatures))
	}
	t.installed, t.sig, t.cleaned = true, sig, true
}

// apply installs (or rebuilds) the chains and the FORWARD jump for IPv4 (required) and
// IPv6 (best effort - a host without ip6tables or IPv6 kernel support is fine).
func (t *torrentState) apply(iface string, peerIPs []string) error {
	for i, fam := range ipTablesBinaries {
		ips := peerIPs
		if i > 0 {
			ips = nil // tunnel addresses are IPv4; the v6 chain is the catch-all only
		}
		if out, err := t.run(buildTorrentRestore(ips), fam.restore, "--noflush", "-w"); err != nil {
			if i == 0 {
				return fmt.Errorf("%s: %w (%s)", fam.restore, err, strings.TrimSpace(out))
			}
			continue
		}
		t.ensureJumpFor(fam.bin, iface)
	}
	return nil
}

func (t *torrentState) ensureJump(iface string) {
	for _, fam := range ipTablesBinaries {
		t.ensureJumpFor(fam.bin, iface)
	}
}

func (t *torrentState) ensureJumpFor(bin, iface string) {
	jump := torrentJumpArgs(iface)
	if _, err := t.run("", bin, append([]string{"-w", "-C"}, jump...)...); err == nil {
		return
	}
	// Position 1: ahead of the unconditional "-i wg0 -j ACCEPT" the interface's PostUp
	// appends, which would otherwise accept everything before we look at it.
	_, _ = t.run("", bin, append([]string{"-w", "-I", "FORWARD", "1"}, jump[1:]...)...)
}

// remove deletes the jump and both chains, tolerating anything already being absent.
func (t *torrentState) remove(iface string) {
	jump := torrentJumpArgs(iface)
	for _, fam := range ipTablesBinaries {
		// A loop, not one -D: the jump could have been inserted twice by a crashed run.
		for i := 0; i < 8; i++ {
			if _, err := t.run("", fam.bin, append([]string{"-w", "-D"}, jump...)...); err != nil {
				break
			}
		}
		_, _ = t.run("", fam.bin, "-w", "-F", torrentChain)
		_, _ = t.run("", fam.bin, "-w", "-F", torrentHitChain)
		_, _ = t.run("", fam.bin, "-w", "-X", torrentChain)
		_, _ = t.run("", fam.bin, "-w", "-X", torrentHitChain)
	}
}
