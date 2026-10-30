package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/obfuscation"
)

// AmneziaWG (AWG) support. When the panel assigns this node an obfuscation profile the
// interface must run as AWG with the profile's parameters; without one it runs as plain
// kernel WireGuard exactly as before.
//
// How peers are synced in AWG mode - the decision this file exists to make:
//
//   - wgctrl cannot set AWG parameters (jc/s1/h1/i1... are not in its Config), so the
//     interface itself is created and configured by awg-quick / `awg set`, not by wgctrl.
//   - Peers, traffic counters and handshakes still go through wgctrl, unchanged.
//     amneziawg-go speaks the standard WireGuard cross-platform UAPI (a unix socket)
//     with extra device keys; wgctrl's userspace client ignores device keys it does not
//     know (internal/wguser/parse.go only switches on private_key/listen_port/fwmark)
//     and the peer keys are the stock ones. So the only thing missing is that wgctrl
//     looks for sockets in /var/run/wireguard while amneziawg-go creates
//     /var/run/amneziawg/<iface>.sock; exposeAWGSocketDir bridges that with a directory
//     symlink. Reusing wgctrl keeps this feature out of reconcilePeers/readDeviceSnapshot,
//     which other features (preshared keys, IPv6 peers) extend: they work in AWG mode
//     for free. The alternative - talking UAPI over the socket by hand - would have
//     forked the peer-sync code path and every future change to it.
//
// Live vs recreate (see obfuscation.Plan): changing or adding parameters is a live
// `awg set` on the running interface (peers and sessions on the node stay up). Clearing
// a parameter, turning AWG on or off, and anything that fails live, recreates the
// interface (down + up), because amneziawg-go v0.2.19's UAPI can overwrite values but
// not unset them, and the kernel module / userspace process switch is not hot-swappable.
// After a recreate the peer signature is reset so peers and tc shaping are re-applied.

// Paths are variables so tests can point them at a temp dir.
var (
	wgConfDir     = "/etc/wireguard"         // wg0.conf (written by node-entrypoint.sh) and wg0.awg (written here)
	awgConfDir    = "/etc/amnezia/amneziawg" // where awg-quick looks for <iface>.conf
	awgSockDir    = "/var/run/amneziawg"     // amneziawg-go's UAPI sockets
	wgctrlSockDir = "/var/run/wireguard"     // where wgctrl's userspace client looks
	tunDevicePath = "/dev/net/tun"           // userspace AWG needs it (compose: devices)
	awgRun        = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() }
	awgLookPath   = exec.LookPath
	awgNow        = time.Now
)

// awgRetryAfter is how long a failed (profile, revision) apply is left alone before the
// next attempt, so a bad profile doesn't tear the interface down every 10s tick.
const awgRetryAfter = 2 * time.Minute

type obfuscationDirective struct {
	Profile *obfuscationProfile `json:"profile"`
}

type obfuscationProfile struct {
	ID       string             `json:"id"`
	Revision int                `json:"revision"`
	Compat   string             `json:"compat"`
	Params   obfuscation.Params `json:"params"`
}

// obfuscationReport is what the agent tells the panel it actually runs.
type obfuscationReport struct {
	Mode      string `json:"mode"` // "wireguard" or "awg"
	ProfileID string `json:"profile_id,omitempty"`
	Revision  int    `json:"revision,omitempty"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// awgAgent is the agent's view of the interface mode. Zero value is usable; init
// detects the real state on first use.
type awgAgent struct {
	inited  bool
	applied *obfuscationProfile // nil = plain WireGuard
	lastErr string
	// failed remembers the last failed apply so it is retried only after awgRetryAfter
	// or once the panel hands out a different profile/revision.
	failedID  string
	failedRev int
	failedAt  time.Time
}

func awgParamsPath(iface string) string { return filepath.Join(wgConfDir, iface+".awg") }

// awgSupport reports why this node cannot run AWG ("" = it can).
func awgSupport() string {
	var missing []string
	for _, bin := range []string{"awg", "awg-quick", "amneziawg-go"} {
		if _, err := awgLookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return "node image has no " + strings.Join(missing, ", ") + " - update the node image"
	}
	if _, err := os.Stat(tunDevicePath); err != nil {
		return tunDevicePath + " is missing - add 'devices: [/dev/net/tun:/dev/net/tun]' to the node's docker-compose.yml and recreate the container"
	}
	return ""
}

// awgRunning is true when an amneziawg-go process serves the interface's UAPI socket.
func awgRunning(iface string) bool {
	fi, err := os.Stat(filepath.Join(awgSockDir, iface+".sock"))
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// detect rebuilds the applied state after an agent (re)start from what is really
// there: an AWG process plus the persisted parameter file written at the last apply.
func (a *awgAgent) detect(iface string) {
	a.inited = true
	a.applied = nil
	if !awgRunning(iface) {
		return
	}
	profile, err := readAWGParamsFile(awgParamsPath(iface))
	if err != nil {
		// AWG runs but we can't tell with what: leave applied nil so the next directive
		// forces a recreate into a known state.
		a.lastErr = "AWG interface is running with unknown parameters: " + err.Error()
		return
	}
	a.applied = profile
	exposeAWGSocketDir()
}

// report builds the heartbeat's obfuscation block.
func (a *awgAgent) report(iface string) *obfuscationReport {
	if !a.inited {
		a.detect(iface)
	}
	r := &obfuscationReport{Mode: "wireguard", Supported: awgSupport() == "", Error: a.lastErr}
	if a.applied != nil {
		r.Mode, r.ProfileID, r.Revision = "awg", a.applied.ID, a.applied.Revision
	}
	return r
}

// apply moves the interface to the panel's desired profile. It returns true when the
// interface was recreated, in which case the caller must re-apply peers and shaping.
// dir == nil means an older control plane that does not speak obfuscation: do nothing.
func (a *awgAgent) apply(iface string, dir *obfuscationDirective, logger *slog.Logger) (recreated bool) {
	if dir == nil {
		return false
	}
	if !a.inited {
		a.detect(iface)
	}
	want := dir.Profile

	if want != nil {
		if reason := awgSupport(); reason != "" {
			// Stay on plain WireGuard and say why; clients are not given AWG configs for
			// this node because the reported mode stays "wireguard".
			a.lastErr = "AmneziaWG unavailable: " + reason
			return false
		}
		if err := want.Params.Validate(want.Compat); err != nil {
			// The panel validates too; a mismatch means a version skew - refuse rather
			// than hand awg-quick something it may reject halfway through.
			a.lastErr = "profile rejected: " + err.Error()
			return false
		}
	}
	if awgSameProfile(a.applied, want) {
		a.lastErr = ""
		return false
	}
	if want != nil && a.failedID == want.ID && a.failedRev == want.Revision && awgNow().Sub(a.failedAt) < awgRetryAfter {
		return false // still backing off; lastErr already says why
	}

	var oldParams, newParams *obfuscation.Params
	if a.applied != nil {
		oldParams = &a.applied.Params
	}
	if want != nil {
		newParams = &want.Params
	}
	plan := obfuscation.Plan(oldParams, newParams)

	switch plan {
	case obfuscation.ChangeNone:
		// Same parameters under a new revision/id: only the bookkeeping changes.
		if err := a.record(iface, want); err != nil {
			logger.Warn("awg_state_save_failed", "error", err)
		}
		a.lastErr = ""
		return false
	case obfuscation.ChangeLive:
		args := append([]string{"set", iface}, want.Params.SetArgs()...)
		if out, err := awgRun("awg", args...); err != nil {
			logger.Warn("awg_live_update_failed_recreating", "error", err, "output", strings.TrimSpace(string(out)))
		} else {
			if err := a.record(iface, want); err != nil {
				logger.Warn("awg_state_save_failed", "error", err)
			}
			a.lastErr = ""
			logger.Info("awg_params_updated_live", "profile", want.ID, "revision", want.Revision)
			return false
		}
	}

	return a.recreate(iface, want, logger)
}

// recreate tears the interface down and brings it up as AWG (want != nil) or plain
// WireGuard (want == nil). Any failure falls back to plain WireGuard so a node with a
// bad profile keeps serving existing clients instead of going dark.
func (a *awgAgent) recreate(iface string, want *obfuscationProfile, logger *slog.Logger) bool {
	logger.Info("awg_recreating_interface", "to_awg", want != nil)
	a.downAny(iface, logger)

	if want != nil {
		err := a.bringUpAWG(iface, want)
		if err == nil {
			a.applied, a.lastErr = want, ""
			a.failedID = ""
			logger.Info("awg_interface_up", "profile", want.ID, "revision", want.Revision)
			return true
		}
		logger.Error("awg_up_failed_falling_back", "error", err)
		a.lastErr = "AmneziaWG failed to start: " + err.Error()
		a.failedID, a.failedRev, a.failedAt = want.ID, want.Revision, awgNow()
		_, _ = awgRun("awg-quick", "down", iface) // drop a half-created interface
		_ = os.Remove(awgParamsPath(iface))
	} else {
		_ = os.Remove(awgParamsPath(iface))
	}

	a.applied = nil
	if out, err := awgRun("wg-quick", "up", iface); err != nil {
		logger.Error("wg_quick_up_failed", "error", err, "output", strings.TrimSpace(string(out)))
		if a.lastErr == "" {
			a.lastErr = "wg-quick up failed: " + strings.TrimSpace(string(out))
		}
	}
	return true
}

// downAny removes whichever flavour of the interface exists. Both downs are tolerant:
// the one that doesn't apply fails harmlessly.
func (a *awgAgent) downAny(iface string, logger *slog.Logger) {
	if awgRunning(iface) {
		if out, err := awgRun("awg-quick", "down", iface); err != nil {
			logger.Warn("awg_quick_down_failed", "error", err, "output", strings.TrimSpace(string(out)))
		}
	}
	if out, err := awgRun("wg-quick", "down", iface); err != nil && !strings.Contains(string(out), "is not a WireGuard interface") {
		logger.Debug("wg_quick_down", "error", err, "output", strings.TrimSpace(string(out)))
	}
}

func (a *awgAgent) bringUpAWG(iface string, want *obfuscationProfile) error {
	if err := a.record(iface, want); err != nil {
		return err
	}
	if err := writeAWGConf(iface, want.Params); err != nil {
		return err
	}
	if out, err := awgRun("awg-quick", "up", iface); err != nil {
		return fmt.Errorf("awg-quick up: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := exposeAWGSocketDir(); err != nil {
		return err
	}
	return nil
}

// record persists the applied profile (also read by node-entrypoint.sh at boot) and
// updates the in-memory state.
func (a *awgAgent) record(iface string, want *obfuscationProfile) error {
	a.applied = want
	if want == nil {
		if err := os.Remove(awgParamsPath(iface)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	body := fmt.Sprintf("# wgpanel-profile: %s %d %s\n%s", want.ID, want.Revision, want.Compat, want.Params.InterfaceBlock())
	return awgWriteFileAtomic(awgParamsPath(iface), []byte(body), 0o600)
}

func awgSameProfile(a, b *obfuscationProfile) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ID == b.ID && a.Revision == b.Revision && a.Params == b.Params
}

// readAWGParamsFile loads what record wrote.
func readAWGParamsFile(path string) (*obfuscationProfile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	first, _, _ := strings.Cut(text, "\n")
	fields := strings.Fields(strings.TrimPrefix(first, "# wgpanel-profile:"))
	if !strings.HasPrefix(first, "# wgpanel-profile:") || len(fields) < 3 {
		return nil, errors.New("missing profile header")
	}
	rev, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, fmt.Errorf("bad revision %q", fields[1])
	}
	params, err := obfuscation.ParseLines(text)
	if err != nil {
		return nil, err
	}
	return &obfuscationProfile{ID: fields[0], Revision: rev, Compat: fields[2], Params: params}, nil
}

// buildAWGConf splices the AWG lines into the plain wg-quick config: right after the
// [Interface] header, so everything node-entrypoint.sh generated (keys, address, port,
// PostUp/PostDown iptables rules) is carried over unchanged. node-entrypoint.sh builds
// the same file at boot with sed; keep the two in step.
func buildAWGConf(plain string, p obfuscation.Params) (string, error) {
	lines := strings.Split(plain, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "[Interface]" {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, strings.Split(strings.TrimRight(p.InterfaceBlock(), "\n"), "\n")...)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	return "", errors.New("no [Interface] section in the wg-quick config")
}

func writeAWGConf(iface string, p obfuscation.Params) error {
	plain, err := os.ReadFile(filepath.Join(wgConfDir, iface+".conf"))
	if err != nil {
		return err
	}
	conf, err := buildAWGConf(string(plain), p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(awgConfDir, 0o700); err != nil {
		return err
	}
	return awgWriteFileAtomic(filepath.Join(awgConfDir, iface+".conf"), []byte(conf), 0o600)
}

// exposeAWGSocketDir makes wgctrl (which only scans /var/run/wireguard for userspace
// sockets) see amneziawg-go's sockets, by symlinking that directory to
// /var/run/amneziawg. A real directory in the way is left alone with an error, since
// replacing it could hide sockets of a different userspace WireGuard.
func exposeAWGSocketDir() error {
	fi, err := os.Lstat(wgctrlSockDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(wgctrlSockDir), 0o755); err != nil {
			return err
		}
		return os.Symlink(awgSockDir, wgctrlSockDir)
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		if target, err := os.Readlink(wgctrlSockDir); err == nil && target == awgSockDir {
			return nil
		}
		return fmt.Errorf("%s is a symlink to somewhere else", wgctrlSockDir)
	default:
		return fmt.Errorf("%s exists as a directory; cannot expose the AmneziaWG socket to wgctrl", wgctrlSockDir)
	}
}

func awgWriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
