package main

import (
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func peersWithIPs(ips ...string) []heartbeatPeer {
	var out []heartbeatPeer
	for _, ip := range ips {
		out = append(out, heartbeatPeer{PublicKey: "k" + ip, AllowedIPs: []string{ip + "/32"}})
	}
	return out
}

func TestTorrentPeerIPs(t *testing.T) {
	peers := append(peersWithIPs("10.66.0.9", "10.66.0.2"),
		heartbeatPeer{PublicKey: "nov4", AllowedIPs: []string{"fd00::5/128"}},
		heartbeatPeer{PublicKey: "none"},
	)
	if got, want := torrentPeerIPs(peers), []string{"10.66.0.2", "10.66.0.9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildTorrentRestore(t *testing.T) {
	plan := buildTorrentRestore([]string{"10.66.0.2", "10.66.0.9"})
	lines := strings.Split(strings.TrimSpace(plan), "\n")

	if lines[0] != "*filter" || lines[len(lines)-1] != "COMMIT" {
		t.Fatalf("not a filter-table restore document:\n%s", plan)
	}
	// Both chains are (re)declared so a rebuild is atomic and idempotent.
	if !strings.Contains(plan, ":WG_TORRENT - [0:0]\n") || !strings.Contains(plan, ":WG_TORRENT_HIT - [0:0]\n") {
		t.Fatalf("chains not declared:\n%s", plan)
	}
	// Every signature is a bm string match bounded to the packet head, jumping to the
	// counting chain.
	for _, sig := range torrentSignatures {
		want := fmt.Sprintf(`-A WG_TORRENT -m string --algo bm --to 512 --string "%s" -m comment --comment "%s" -j WG_TORRENT_HIT`, sig.pattern, sig.name)
		if !strings.Contains(plan, want+"\n") {
			t.Errorf("missing rule %q", want)
		}
	}
	for _, must := range []string{"BitTorrent protocol", "d1:ad2:id20:", "info_hash=", "peer_id=", "/announce?"} {
		if !strings.Contains(plan, `--string "`+must+`"`) {
			t.Errorf("signature %q missing", must)
		}
	}
	// Per-IP counting rules in sorted order, then the catch-all DROP last.
	hit := []string{}
	for _, l := range lines {
		if strings.HasPrefix(l, "-A WG_TORRENT_HIT") {
			hit = append(hit, l)
		}
	}
	want := []string{
		"-A WG_TORRENT_HIT -s 10.66.0.2/32 -j DROP",
		"-A WG_TORRENT_HIT -s 10.66.0.9/32 -j DROP",
		"-A WG_TORRENT_HIT -j DROP",
	}
	if !reflect.DeepEqual(hit, want) {
		t.Fatalf("hit chain:\n%s\nwant:\n%s", strings.Join(hit, "\n"), strings.Join(want, "\n"))
	}
	// Deterministic: same input, same document (the signature gate relies on it).
	if plan != buildTorrentRestore([]string{"10.66.0.2", "10.66.0.9"}) {
		t.Fatal("plan is not deterministic")
	}
	// No peers (the IPv6 chain): signatures and the catch-all only.
	v6 := buildTorrentRestore(nil)
	if strings.Contains(v6, " -s ") || !strings.Contains(v6, "-A WG_TORRENT_HIT -j DROP") {
		t.Fatalf("v6 plan:\n%s", v6)
	}
	// Nothing outside our own chains is ever touched.
	for _, l := range lines {
		if strings.HasPrefix(l, "-A ") && !strings.HasPrefix(l, "-A WG_TORRENT ") && !strings.HasPrefix(l, "-A WG_TORRENT_HIT ") {
			t.Errorf("rule outside the dedicated chains: %s", l)
		}
	}
}

func TestTorrentJumpArgs(t *testing.T) {
	want := []string{"FORWARD", "-i", "wg0", "-j", "WG_TORRENT"}
	if got := torrentJumpArgs("wg0"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestParseTorrentCounters(t *testing.T) {
	out := `Chain WG_TORRENT_HIT (2 references)
    pkts      bytes target     prot opt in     out     source               destination
      12      960 DROP       all  --  *      *       10.66.0.2            0.0.0.0/0
       0        0 DROP       all  --  *      *       10.66.0.3            0.0.0.0/0
       5      400 DROP       all  --  *      *       10.66.0.9            0.0.0.0/0
       7      560 DROP       all  --  *      *       0.0.0.0/0            0.0.0.0/0
`
	want := map[string]int64{"10.66.0.2": 12, "10.66.0.9": 5}
	if got := parseTorrentCounters(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := parseTorrentCounters("garbage\n"); len(got) != 0 {
		t.Fatalf("garbage parsed as %v", got)
	}
}

// fakeIptables records every command and answers the few it must: -C (jump present?),
// -nvxL (counters), and optionally fails the restore (module missing).
type fakeIptables struct {
	calls       []string
	stdins      []string
	jumpPresent bool
	counters    string
	failRestore bool
}

func (f *fakeIptables) run(stdin, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if stdin != "" {
		f.stdins = append(f.stdins, name+"\n"+stdin)
	}
	switch {
	case strings.HasSuffix(name, "-restore"):
		if f.failRestore {
			return "iptables-restore: Couldn't load match `string'", fmt.Errorf("exit status 2")
		}
	case len(args) > 1 && args[1] == "-C":
		if !f.jumpPresent {
			return "", fmt.Errorf("no such rule")
		}
		return "", nil
	case len(args) > 1 && args[1] == "-I":
		f.jumpPresent = true
	case len(args) > 1 && args[1] == "-D":
		if !f.jumpPresent {
			return "", fmt.Errorf("no such rule")
		}
		f.jumpPresent = false
	case len(args) > 1 && args[1] == "-nvxL":
		out := f.counters
		f.counters = "" // -Z zeroes
		return out, nil
	}
	return "", nil
}

func (f *fakeIptables) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newTestTorrentState(f *fakeIptables) *torrentState {
	st := newTorrentState()
	st.run = f.run
	return &st
}

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func boolp(b bool) *bool { return &b }

func TestTorrentSyncLifecycle(t *testing.T) {
	f := &fakeIptables{}
	st := newTestTorrentState(f)
	peers := peersWithIPs("10.66.0.2", "10.66.0.3")

	// A control plane that does not know the field: hands off.
	st.sync("wg0", nil, peers, quietLogger)
	if len(f.calls) != 0 {
		t.Fatalf("touched iptables on an absent flag: %v", f.calls)
	}

	// Off on a fresh agent: removes stale rules from a previous run, once.
	st.sync("wg0", boolp(false), peers, quietLogger)
	if f.count("iptables -w -F WG_TORRENT") == 0 || f.count("iptables-restore") != 0 {
		t.Fatalf("expected cleanup only: %v", f.calls)
	}
	before := len(f.calls)
	st.sync("wg0", boolp(false), peers, quietLogger)
	if len(f.calls) != before {
		t.Fatal("off -> off must be a no-op after the first cleanup")
	}

	// On: restore for v4 and v6 plus the FORWARD jump at position 1.
	st.sync("wg0", boolp(true), peers, quietLogger)
	if f.count("iptables-restore --noflush -w") != 1 || f.count("ip6tables-restore --noflush -w") != 1 {
		t.Fatalf("restore calls: %v", f.calls)
	}
	if f.count("iptables -w -I FORWARD 1 -i wg0 -j WG_TORRENT") != 1 {
		t.Fatalf("jump not inserted at position 1: %v", f.calls)
	}
	if !strings.Contains(f.stdins[0], "-s 10.66.0.2/32 -j DROP") {
		t.Fatalf("v4 plan lacks per-IP rule:\n%s", f.stdins[0])
	}
	if strings.Contains(f.stdins[1], " -s ") {
		t.Fatalf("v6 plan must not carry v4 sources:\n%s", f.stdins[1])
	}

	// Same peers: idempotent, only the cheap jump check runs.
	n := f.count("iptables-restore")
	st.sync("wg0", boolp(true), peers, quietLogger)
	if f.count("iptables-restore") != n {
		t.Fatal("rebuilt although nothing changed")
	}

	// Someone flushed FORWARD: the jump is restored without a rebuild.
	f.jumpPresent = false
	st.sync("wg0", boolp(true), peers, quietLogger)
	if !f.jumpPresent || f.count("iptables-restore") != n {
		t.Fatal("jump not re-inserted")
	}

	// Peer set changes: counters are read first (they would be lost by the rebuild) and
	// the chains are rebuilt.
	f.counters = "      4      320 DROP       all  --  *      *       10.66.0.2            0.0.0.0/0\n"
	st.sync("wg0", boolp(true), append(peers, peersWithIPs("10.66.0.4")...), quietLogger)
	if f.count("iptables-restore") != n+1 {
		t.Fatalf("no rebuild on a changed peer set: %v", f.calls)
	}
	if st.pending["10.66.0.2"] != 4 {
		t.Fatalf("hits before the rebuild were lost: %v", st.pending)
	}

	// Off again: removes the jump and chains.
	st.sync("wg0", boolp(false), peers, quietLogger)
	if f.jumpPresent || f.count("iptables -w -X WG_TORRENT_HIT") == 0 || st.installed {
		t.Fatalf("not removed: installed=%v %v", st.installed, f.calls)
	}
}

func TestTorrentSyncModuleMissingRetries(t *testing.T) {
	f := &fakeIptables{failRestore: true}
	st := newTestTorrentState(f)
	peers := peersWithIPs("10.66.0.2")
	st.sync("wg0", boolp(true), peers, quietLogger)
	if st.installed {
		t.Fatal("marked installed after a failed restore")
	}
	// Retried on the next tick (not gated by the signature) and recovers.
	f.failRestore = false
	st.sync("wg0", boolp(true), peers, quietLogger)
	if !st.installed {
		t.Fatal("did not recover once the restore worked")
	}
}

func TestTorrentHitReporting(t *testing.T) {
	f := &fakeIptables{}
	st := newTestTorrentState(f)
	st.sync("wg0", boolp(true), peersWithIPs("10.66.0.2", "10.66.0.3"), quietLogger)

	f.counters = `      3      240 DROP       all  --  *      *       10.66.0.2            0.0.0.0/0
      1       80 DROP       all  --  *      *       10.66.0.3            0.0.0.0/0
`
	first := st.collect()
	if !reflect.DeepEqual(first, []torrentHitReport{{"10.66.0.2", 3}, {"10.66.0.3", 1}}) {
		t.Fatalf("first report %v", first)
	}

	// The heartbeat failed (no ack): the next report carries the old hits plus new ones.
	f.counters = "      2      160 DROP       all  --  *      *       10.66.0.2            0.0.0.0/0\n"
	second := st.collect()
	if !reflect.DeepEqual(second, []torrentHitReport{{"10.66.0.2", 5}, {"10.66.0.3", 1}}) {
		t.Fatalf("unacknowledged hits must be retried: %v", second)
	}

	// Acked: gone; hits that arrived in between stay pending.
	f.counters = "      6      480 DROP       all  --  *      *       10.66.0.3            0.0.0.0/0\n"
	third := st.collect()
	st.acked(second) // (as if the ack for `second` arrived while `third` was being built)
	if len(st.pending) != 1 || st.pending["10.66.0.3"] != 6 {
		t.Fatalf("pending after ack: %v (third=%v)", st.pending, third)
	}
	st.acked([]torrentHitReport{{"10.66.0.3", 6}})
	if len(st.pending) != 0 {
		t.Fatalf("pending not drained: %v", st.pending)
	}
	if got := st.collect(); len(got) != 0 {
		t.Fatalf("empty report expected, got %v", got)
	}
}
