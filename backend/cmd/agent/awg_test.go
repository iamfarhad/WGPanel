package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wgpanel-api/internal/obfuscation"
)

type awgFixture struct {
	t      *testing.T
	cmds   []string
	failUp bool
	// dropH12 simulates the amneziawg-tools v1.0.20260618 config-parser bug: awg-quick
	// brings the interface up WITHOUT H1/H2.
	dropH12 bool
	// running is what the fake interface runs, as awg showconf reports it.
	running   obfuscation.Params
	showconfs int
	listener  net.Listener
	clock     time.Time
	logger    *slog.Logger
}

// newAWGFixture points the agent's paths at a temp dir and fakes the command runner:
// "awg-quick up" creates a real unix socket in the fake /var/run/amneziawg (that is how
// awgRunning detects the userspace process) and "awg-quick down" removes it.
func newAWGFixture(t *testing.T) *awgFixture {
	t.Helper()
	// A short path: unix socket paths are limited to ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "awg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	f := &awgFixture{t: t, clock: time.Unix(1_700_000_000, 0), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	oldConf, oldAWG, oldSock, oldCtl, oldTun := wgConfDir, awgConfDir, awgSockDir, wgctrlSockDir, tunDevicePath
	oldRun, oldLook, oldNow := awgRun, awgLookPath, awgNow
	t.Cleanup(func() {
		wgConfDir, awgConfDir, awgSockDir, wgctrlSockDir, tunDevicePath = oldConf, oldAWG, oldSock, oldCtl, oldTun
		awgRun, awgLookPath, awgNow = oldRun, oldLook, oldNow
		if f.listener != nil {
			f.listener.Close()
		}
	})
	wgConfDir = filepath.Join(dir, "wg")
	awgConfDir = filepath.Join(dir, "amn")
	awgSockDir = filepath.Join(dir, "s")
	wgctrlSockDir = filepath.Join(dir, "r", "wireguard")
	tunDevicePath = filepath.Join(dir, "tun")
	for _, d := range []string{wgConfDir, awgSockDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	must(t, os.WriteFile(tunDevicePath, nil, 0o600))
	must(t, os.WriteFile(filepath.Join(wgConfDir, "wg0.conf"),
		[]byte("[Interface]\nPrivateKey = KEY\nAddress = 10.66.0.1/24\nListenPort = 51820\nPostUp = iptables -A FORWARD -i %i -j ACCEPT\n"), 0o600))

	awgLookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	awgNow = func() time.Time { return f.clock }
	awgRun = func(name string, args ...string) ([]byte, error) {
		f.cmds = append(f.cmds, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		switch {
		case name == "awg" && args[0] == "showconf":
			f.showconfs++
			return []byte("[Interface]\nPrivateKey = KEY\nListenPort = 51820\n" + f.running.InterfaceBlock() + "\n[Peer]\nPublicKey = PUB\nAllowedIPs = 10.66.0.2/32\n"), nil
		case name == "awg" && args[0] == "set":
			var text string
			for i := 2; i+1 < len(args); i += 2 {
				text += args[i] + " = " + args[i+1] + "\n"
			}
			set, err := obfuscation.ParseLines(text)
			must(t, err)
			f.running = mergeParams(f.running, set)
			return nil, nil
		case name == "awg-quick" && args[0] == "up":
			if f.failUp {
				return []byte("boom"), errors.New("exit 1")
			}
			raw, err := os.ReadFile(filepath.Join(awgConfDir, args[1]+".conf"))
			must(t, err)
			var lines []string
			for _, l := range strings.Split(string(raw), "\n") {
				if awgParamKey(strings.TrimSpace(l)) {
					lines = append(lines, l)
				}
			}
			f.running, err = obfuscation.ParseLines(strings.Join(lines, "\n"))
			must(t, err)
			if f.dropH12 {
				f.running.H1, f.running.H2 = "", ""
			}
			l, err := net.Listen("unix", filepath.Join(awgSockDir, args[1]+".sock"))
			if err != nil {
				t.Fatal(err)
			}
			f.listener = l
		case name == "awg-quick" && args[0] == "down":
			if f.listener != nil {
				f.listener.Close()
				f.listener = nil
			}
			os.Remove(filepath.Join(awgSockDir, args[1]+".sock"))
		}
		return nil, nil
	}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mergeParams overlays the set fields of b onto a, like `awg set`.
func mergeParams(a, b obfuscation.Params) obfuscation.Params {
	pick := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	pickS := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	pick(&a.Jc, b.Jc)
	pick(&a.Jmin, b.Jmin)
	pick(&a.Jmax, b.Jmax)
	pick(&a.S1, b.S1)
	pick(&a.S2, b.S2)
	pick(&a.S3, b.S3)
	pick(&a.S4, b.S4)
	pickS(&a.H1, b.H1)
	pickS(&a.H2, b.H2)
	pickS(&a.H3, b.H3)
	pickS(&a.H4, b.H4)
	pickS(&a.I1, b.I1)
	return a
}

// take returns the commands run since the last call, leaving out the read-only
// `awg showconf` verification calls (asserted separately via f.showconfs).
func (f *awgFixture) take() []string {
	var c []string
	for _, cmd := range f.cmds {
		if cmd != "awg showconf wg0" {
			c = append(c, cmd)
		}
	}
	f.cmds = nil
	return c
}

func profile(rev int, p obfuscation.Params) *obfuscationDirective {
	return &obfuscationDirective{Profile: &obfuscationProfile{ID: "p1", Revision: rev, Compat: "awg1", Params: p}}
}

var baseParams = obfuscation.Params{Jc: 5, Jmin: 50, Jmax: 200, S1: 30, S2: 40, H1: "10", H2: "20", H3: "30", H4: "40"}

func TestAWGApplyPlan(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent

	// Nothing to do on plain WireGuard, and an old panel (nil directive) changes nothing.
	if a.apply("wg0", &obfuscationDirective{}, f.logger) || a.apply("wg0", nil, f.logger) || len(f.take()) != 0 {
		t.Fatal("plain WireGuard to plain WireGuard must not touch the interface")
	}
	if r := a.report("wg0"); r.Mode != "wireguard" || !r.Supported {
		t.Fatalf("report %+v", r)
	}

	// Enable: recreate as AWG, with the params spliced into [Interface] and persisted.
	if !a.apply("wg0", profile(1, baseParams), f.logger) {
		t.Fatal("enabling AWG must report a recreate")
	}
	if got, want := strings.Join(f.take(), "|"), "wg-quick down wg0|awg-quick up wg0"; got != want {
		t.Fatalf("enable commands %q, want %q", got, want)
	}
	conf, err := os.ReadFile(filepath.Join(awgConfDir, "wg0.conf"))
	must(t, err)
	if !strings.HasPrefix(string(conf), "[Interface]\nJc = 5\nJmin = 50\n") || !strings.Contains(string(conf), "PostUp = iptables") || !strings.Contains(string(conf), "H4 = 40\nPrivateKey = KEY") {
		t.Fatalf("awg conf:\n%s", conf)
	}
	if r := a.report("wg0"); r.Mode != "awg" || r.ProfileID != "p1" || r.Revision != 1 {
		t.Fatalf("report after enable %+v", r)
	}
	if fi, err := os.Lstat(wgctrlSockDir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("wgctrl socket dir must be a symlink to the AWG one: %v", err)
	}

	// Same revision again: nothing.
	if a.apply("wg0", profile(1, baseParams), f.logger) || len(f.take()) != 0 {
		t.Fatal("unchanged profile must be a no-op")
	}

	// Rotate (values overwritten, nothing cleared): live, no recreate.
	rotated := baseParams
	rotated.Jc, rotated.H1 = 7, "11"
	if a.apply("wg0", profile(2, rotated), f.logger) {
		t.Fatal("overwriting values must not recreate the interface")
	}
	if got, want := strings.Join(f.take(), "|"), "awg set wg0 jc 7 jmin 50 jmax 200 s1 30 s2 40 h1 11 h2 20 h3 30 h4 40"; got != want {
		t.Fatalf("live commands %q, want %q", got, want)
	}
	if r := a.report("wg0"); r.Revision != 2 {
		t.Fatalf("revision after live update: %+v", r)
	}

	// An agent restart rebuilds the same state from the persisted file + running socket.
	var restarted awgAgent
	if restarted.apply("wg0", profile(2, rotated), f.logger) || len(f.take()) != 0 {
		t.Fatal("a restarted agent must recognise the running profile")
	}

	// Clearing a value cannot be done live: recreate.
	cleared := rotated
	cleared.S1 = 0
	if !a.apply("wg0", profile(3, cleared), f.logger) {
		t.Fatal("clearing a value must recreate")
	}
	if got, want := strings.Join(f.take(), "|"), "awg-quick down wg0|wg-quick down wg0|awg-quick up wg0"; got != want {
		t.Fatalf("recreate commands %q, want %q", got, want)
	}

	// Disable: back to kernel WireGuard, persisted file gone.
	if !a.apply("wg0", &obfuscationDirective{}, f.logger) {
		t.Fatal("disabling must recreate")
	}
	if got, want := strings.Join(f.take(), "|"), "awg-quick down wg0|wg-quick down wg0|wg-quick up wg0"; got != want {
		t.Fatalf("disable commands %q, want %q", got, want)
	}
	if _, err := os.Stat(awgParamsPath("wg0")); !os.IsNotExist(err) {
		t.Fatalf("params file must be removed, got %v", err)
	}
	if r := a.report("wg0"); r.Mode != "wireguard" {
		t.Fatalf("report after disable %+v", r)
	}
}

func TestAWGFailureFallsBackAndBacksOff(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent
	f.failUp = true

	if !a.apply("wg0", profile(1, baseParams), f.logger) {
		t.Fatal("a failed enable still recreated the interface (fallback)")
	}
	cmds := strings.Join(f.take(), "|")
	if !strings.HasSuffix(cmds, "awg-quick up wg0|awg-quick down wg0|wg-quick up wg0") {
		t.Fatalf("fallback commands %q", cmds)
	}
	r := a.report("wg0")
	if r.Mode != "wireguard" || !strings.Contains(r.Error, "AmneziaWG failed to start") {
		t.Fatalf("report after failure %+v", r)
	}
	if _, err := os.Stat(awgParamsPath("wg0")); !os.IsNotExist(err) {
		t.Fatal("a failed profile must not be persisted for the next boot")
	}

	// Inside the backoff window nothing is retried; a new revision is tried at once.
	if a.apply("wg0", profile(1, baseParams), f.logger) || len(f.take()) != 0 {
		t.Fatal("must back off before retrying the same revision")
	}
	f.failUp = false
	if !a.apply("wg0", profile(2, baseParams), f.logger) {
		t.Fatal("a new revision must be tried immediately")
	}
	f.take()
	if r := a.report("wg0"); r.Mode != "awg" || r.Error != "" {
		t.Fatalf("report after recovery %+v", r)
	}
}

func TestAWGBackoffExpires(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent
	f.failUp = true
	a.apply("wg0", profile(1, baseParams), f.logger)
	f.take()
	f.clock = f.clock.Add(awgRetryAfter + time.Second)
	f.failUp = false
	if !a.apply("wg0", profile(1, baseParams), f.logger) {
		t.Fatal("must retry after the backoff window")
	}
}

func TestAWGUnsupportedStaysPlain(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent
	awgLookPath = func(string) (string, error) { return "", errors.New("not found") }
	if a.apply("wg0", profile(1, baseParams), f.logger) || len(f.take()) != 0 {
		t.Fatal("an unsupported node must not touch the interface")
	}
	r := a.report("wg0")
	if r.Mode != "wireguard" || r.Supported || !strings.Contains(r.Error, "AmneziaWG unavailable") {
		t.Fatalf("report %+v", r)
	}

	awgLookPath = func(string) (string, error) { return "/x", nil }
	must(t, os.Remove(tunDevicePath))
	a.apply("wg0", profile(1, baseParams), f.logger)
	if r := a.report("wg0"); !strings.Contains(r.Error, "/dev/net/tun") {
		t.Fatalf("missing tun device must be named in the error: %+v", r)
	}
}

func TestAWGInvalidProfileRefused(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent
	bad := baseParams
	bad.S1, bad.S2 = 30, 86 // s1+56 == s2
	if a.apply("wg0", profile(1, bad), f.logger) || len(f.take()) != 0 {
		t.Fatal("an invalid profile must not reach awg-quick")
	}
	if r := a.report("wg0"); !strings.Contains(r.Error, "profile rejected") {
		t.Fatalf("report %+v", r)
	}
}

func TestBuildAWGConf(t *testing.T) {
	got, err := buildAWGConf("# c\n[Interface]\nPrivateKey = K\n\n[Peer]\nPublicKey = P\n", obfuscation.Params{Jc: 4, Jmin: 8, Jmax: 80})
	must(t, err)
	want := "# c\n[Interface]\nJc = 4\nJmin = 8\nJmax = 80\nPrivateKey = K\n\n[Peer]\nPublicKey = P\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, err := buildAWGConf("[Peer]\n", obfuscation.Params{Jc: 4}); err == nil {
		t.Fatal("a config without [Interface] must be refused")
	}
}

func TestExposeAWGSocketDir(t *testing.T) {
	newAWGFixture(t)
	must(t, exposeAWGSocketDir())
	must(t, exposeAWGSocketDir()) // idempotent
	must(t, os.Remove(wgctrlSockDir))
	must(t, os.MkdirAll(wgctrlSockDir, 0o755))
	if err := exposeAWGSocketDir(); err == nil {
		t.Fatal("a real directory in the way must be an error, not silently replaced")
	}
}

func TestAWGVerifiesRunningParams(t *testing.T) {
	f := newAWGFixture(t)
	var a awgAgent
	a.apply("wg0", profile(1, baseParams), f.logger)
	if f.showconfs == 0 {
		t.Fatal("bringing the interface up must verify the running parameters")
	}
	f.take()

	// A restarted agent verifies once, then trusts its state.
	var restarted awgAgent
	f.showconfs = 0
	restarted.apply("wg0", profile(1, baseParams), f.logger)
	restarted.apply("wg0", profile(1, baseParams), f.logger)
	if f.showconfs != 1 {
		t.Fatalf("restarted agent verified %d times, want 1", f.showconfs)
	}

	// The interface was changed behind the agent's back: the restart check repairs it
	// (here by recreating, since the fake keeps the wrong value after `awg set`).
	f.running.Jc = 99
	var again awgAgent
	f.take()
	again.apply("wg0", profile(1, baseParams), f.logger)
	if got := strings.Join(f.take(), "|"); !strings.HasPrefix(got, "awg set wg0 jc 5") {
		t.Fatalf("expected an awg set repair, got %q", got)
	}
	if f.running != baseParams {
		t.Fatalf("running params not repaired: %+v", f.running)
	}
}

// A junk-only profile: the device reports its stock headers 1-4 in showconf, which
// must compare equal to "no headers configured" (found in the smoke test).
func TestAWGRunningParamsNormalisesStockHeaders(t *testing.T) {
	newAWGFixture(t)
	awgRun = func(name string, args ...string) ([]byte, error) {
		return []byte("[Interface]\nPrivateKey = K\nListenPort = 1\nJc = 8\nJmin = 110\nJmax = 210\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\n\n[Peer]\nPublicKey = P\nPresharedKey = Q\n"), nil
	}
	got, err := awgRunningParams("wg0")
	must(t, err)
	if want := (obfuscation.Params{Jc: 8, Jmin: 110, Jmax: 210}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// The upstream tools bug: awg-quick drops H1/H2. The agent must notice and push them.
func TestAWGRepairsDroppedH1H2(t *testing.T) {
	f := newAWGFixture(t)
	f.dropH12 = true
	var a awgAgent
	if !a.apply("wg0", profile(1, baseParams), f.logger) {
		t.Fatal("expected a recreate")
	}
	cmds := strings.Join(f.take(), "|")
	if !strings.Contains(cmds, "awg-quick up wg0|awg set wg0 jc 5") || !strings.Contains(cmds, "h1 10 h2 20") {
		t.Fatalf("H1/H2 must be repaired with awg set after awg-quick up: %q", cmds)
	}
	if f.running.H1 != "10" || f.running.H2 != "20" {
		t.Fatalf("running params %+v", f.running)
	}
	if r := a.report("wg0"); r.Mode != "awg" || r.Error != "" {
		t.Fatalf("report %+v", r)
	}
}
