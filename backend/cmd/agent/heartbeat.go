package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	heartbeatInterval      = 10 * time.Second
	controlPlaneServerName = "wgpanel-control-plane" // matches nodeca.ServerName - not real DNS, see STORY-04

	// metricsEveryNTicks controls node CPU/RAM reporting cadence - deliberately
	// lower-frequency than the 10s peer-reconciliation tick (docs/STORY-10-monitoring-
	// and-domain-management.md): a dashboard health chart doesn't need 10s resolution,
	// and this cuts node_metrics' row volume 4x for free without touching the timing
	// suspend/quota enforcement latency actually depends on.
	metricsEveryNTicks = 4
)

type heartbeatPeerTraffic struct {
	PublicKey     string     `json:"public_key"`
	ReceiveBytes  int64      `json:"rx_bytes"`
	TransmitBytes int64      `json:"tx_bytes"`
	LastHandshake *time.Time `json:"last_handshake,omitempty"`
	// Endpoint is the peer's current client source "ip:port" from the same kernel
	// snapshot as the counters - the control plane's device-tracking input
	// (PRD-account-management.md §6.4). Omitted when the peer has never connected.
	Endpoint string `json:"endpoint,omitempty"`
}

type heartbeatRequest struct {
	PeerCount     int                    `json:"peer_count"`
	LoadAvg       float64                `json:"load_avg"`
	Traffic       []heartbeatPeerTraffic `json:"traffic,omitempty"`
	CPUPercent    *float32               `json:"cpu_percent,omitempty"`
	MemUsedBytes  *int64                 `json:"mem_used_bytes,omitempty"`
	MemTotalBytes *int64                 `json:"mem_total_bytes,omitempty"`
	// DNSForwarder tells the panel it may point this node's clients at the node's
	// tunnel address for DNS (see dnsForwarder).
	DNSForwarder bool `json:"dns_forwarder"`
	// TorrentHits: matched BitTorrent-signature packets per tunnel IP not yet
	// acknowledged by the panel (see torrent.go); omitted when there are none.
	TorrentHits []torrentHitReport `json:"torrent_hits,omitempty"`
	// PSKSupported: this agent sets PresharedKey on peers when the panel supplies one.
	// The panel only hands out PSKs to nodes that say so - an old agent would drop the
	// key and every PSK client's handshake would fail.
	PSKSupported bool `json:"psk_supported"`
	// IPv6Global / IPv6Ready: the host has a global IPv6 route / the node is set up for
	// IPv6 egress (see ipv6.go).
	IPv6Global bool `json:"ipv6_global"`
	IPv6Ready  bool `json:"ipv6_ready"`
	// Obfuscation is the interface mode the agent really runs (see awg.go).
	Obfuscation *obfuscationReport `json:"obfuscation,omitempty"`
}

type heartbeatPeer struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	// BandwidthLimitMbps: per-account rate limit the control plane wants enforced on
	// this peer via tc (see tc.go); nil = unshaped.
	BandwidthLimitMbps *int `json:"bandwidth_limit_mbps"`
	// PresharedKey (base64) is set by the control plane only for peers whose client was
	// handed it; "" = none.
	PresharedKey string `json:"preshared_key"`
}

type heartbeatResponse struct {
	Status string          `json:"status"`
	Peers  []heartbeatPeer `json:"peers"`
	// Network is absent from older control planes - see checkNodeNetwork.
	Network *nodeNetwork `json:"network"`
	// ClientDNS is the DNS = value client configs carry - see checkClientDNS.
	ClientDNS string `json:"client_dns"`
	// TorrentBlock is whether this node should drop torrent-signature packets; nil from
	// a control plane that predates the feature (see torrentState.sync).
	TorrentBlock *bool `json:"torrent_block"`
	// IPv6 is the node's IPv6 egress instruction; nil from an older control plane.
	IPv6 *heartbeatIPv6 `json:"ipv6"`
	// Obfuscation is the AmneziaWG profile to run; absent from older control planes.
	Obfuscation *obfuscationDirective `json:"obfuscation"`
}

// runHeartbeatLoop returns only on shutdown (nil) or on a fatal node-network
// mismatch reported by the control plane (see heartbeatState.checkNetwork).
func runHeartbeatLoop(ctx context.Context, cfg config, id *identity, dnsForwarder bool, logger *slog.Logger) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{id.TLSCert},
				RootCAs:      id.CACertPool,
				ServerName:   controlPlaneServerName,
			},
		},
		Timeout: 15 * time.Second,
	}

	state := &heartbeatState{checkedNetwork: id.Network, dnsForwarder: dnsForwarder, torrent: newTorrentState()}
	// don't wait a full interval before the first one
	if err := state.tick(ctx, cfg, client, logger); err != nil {
		return err
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := state.tick(ctx, cfg, client, logger); err != nil {
				return err
			}
		}
	}
}

// heartbeatState carries the previous tick's CPU sample (for computing a percentage
// from the delta) and a tick counter (for the lower-frequency metrics cadence) across
// heartbeat calls - both are meaningless on a single isolated reading.
//
// lastAppliedPeerSignature is the other piece of cross-tick state: it's what makes
// reconcilePeers idempotent-in-practice, not just idempotent-in-theory. See
// peerSignature's doc comment for why this exists - a real, previously-undetected
// bug (found only once a real client kept a session open across multiple heartbeat
// ticks to actually verify it, which nothing before this ever did).
//
// checkedNetwork is the node network last verified against the real interface (at
// startup from the saved registration, or from a heartbeat), so the check only
// reruns when the control plane reports something different; checkedDNS is the same
// for the client resolvers probed by checkClientDNS.
type heartbeatState struct {
	tickCount                int
	prevCPU                  cpuSample
	havePrevCPU              bool
	lastAppliedPeerSignature string
	checkedNetwork           nodeNetwork
	checkedDNS               string
	dnsForwarder             bool
	torrent                  torrentState
	ipv6                     ipv6State
	awg                      awgAgent
}

// tick returns an error only for a fatal node-network mismatch; every transient
// failure is logged and retried on the next tick.
func (s *heartbeatState) tick(ctx context.Context, cfg config, client *http.Client, logger *slog.Logger) error {
	s.tickCount++

	snapshot, snapshotOK := readDeviceSnapshot(cfg.WGInterface)
	peerCount := 0
	var traffic []heartbeatPeerTraffic
	if snapshotOK {
		peerCount = len(snapshot.peers)
		for _, p := range snapshot.peers {
			t := heartbeatPeerTraffic{
				PublicKey:     p.PublicKey.String(),
				ReceiveBytes:  p.ReceiveBytes,
				TransmitBytes: p.TransmitBytes,
			}
			if !p.LastHandshakeTime.IsZero() {
				hs := p.LastHandshakeTime
				t.LastHandshake = &hs
			}
			if p.Endpoint != nil {
				t.Endpoint = p.Endpoint.String()
			}
			traffic = append(traffic, t)
		}
	}

	req := heartbeatRequest{PeerCount: peerCount, LoadAvg: 0, Traffic: traffic, DNSForwarder: s.dnsForwarder,
		PSKSupported: true, IPv6Global: s.ipv6.reportedGlobal, IPv6Ready: s.ipv6.ready}
	req.Obfuscation = s.awg.report(cfg.WGInterface)
	if s.tickCount%metricsEveryNTicks == 1 {
		req.CPUPercent = s.readCPUPercent()
		if used, total, ok := readMemInfo(); ok {
			req.MemUsedBytes = &used
			req.MemTotalBytes = &total
		}
	}

	req.TorrentHits = s.torrent.collect()

	resp, ok := sendHeartbeat(ctx, cfg, client, logger, req)
	if !ok {
		return nil
	}
	s.torrent.acked(req.TorrentHits)
	s.torrent.sync(cfg.WGInterface, resp.TorrentBlock, resp.Peers, logger)
	// Checked before heartbeat_ok is logged: the installers read that line as success,
	// and report any client_dns_unreachable warning logged before it.
	if err := s.checkNetwork(cfg, resp.Network, logger); err != nil {
		return err
	}
	s.ipv6.sync(cfg.WGInterface, resp.IPv6, logger)
	if resp.ClientDNS != "" && resp.ClientDNS != s.checkedDNS {
		checkClientDNS(resp.ClientDNS, logger)
		s.checkedDNS = resp.ClientDNS
	}
	logger.Info("heartbeat_ok", "peer_count", peerCount, "desired_peers", len(resp.Peers))

	// AmneziaWG mode switch / parameter change. A recreated interface starts without
	// peers or tc shaping, so forget the applied signature to re-sync both below. It
	// also loses the IPv6 gateway address ipv6.sync put on it above; forgetting that
	// state re-adds it now (addr replace and the -C-guarded rules are idempotent). The
	// torrent rules match the interface by name, so they survive the recreate as-is.
	if s.awg.apply(cfg.WGInterface, resp.Obfuscation, logger) {
		s.lastAppliedPeerSignature = ""
		s.ipv6.applied = heartbeatIPv6{}
		s.ipv6.sync(cfg.WGInterface, resp.IPv6, logger)
	}

	// Only actually touch the interface when the desired peer set changed since the
	// last tick we successfully applied - see peerSignature's doc comment for why
	// reapplying an unchanged set on every ~10s tick is not the harmless no-op it
	// looks like.
	sig := peerSignature(resp.Peers)
	if sig == s.lastAppliedPeerSignature {
		return nil
	}
	if err := reconcilePeers(cfg.WGInterface, resp.Peers); err != nil {
		logger.Warn("reconcile_peers_failed", "error", err)
		return nil
	}
	// Shaping failure (e.g. no tc binary on an old bare-metal install) must not hold
	// the signature back: that would force reconcilePeers to reapply ReplacePeers
	// every 10s - exactly the handshake-resetting behavior peerSignature exists to
	// prevent. Peers stay correct, just without rate enforcement, and the warn says so.
	if err := applyShaping(cfg.WGInterface, resp.Peers, logger); err != nil {
		logger.Warn("apply_shaping_failed", "error", err)
	}
	s.lastAppliedPeerSignature = sig
	return nil
}

// checkNetwork verifies the interface against the node network the control plane
// reported, whenever that differs from what was last verified - which is what covers
// nodes registered before /agent/register returned it: they have no saved network,
// so this first heartbeat is their first check. The result is saved so the next
// start checks before the agent reports itself registered.
func (s *heartbeatState) checkNetwork(cfg config, network *nodeNetwork, logger *slog.Logger) error {
	if network == nil || *network == s.checkedNetwork {
		return nil
	}
	if err := checkNodeNetwork(cfg, *network, logger); err != nil {
		return err
	}
	s.checkedNetwork = *network
	if err := saveNodeNetwork(statePaths(cfg.StateDir), *network); err != nil {
		logger.Warn("save_node_network_failed", "error", err)
	}
	return nil
}

// peerSignature builds a stable (sorted, so response-ordering differences don't
// matter) string identity of a desired-peer list, used to skip reapplying
// ConfigureDevice when nothing has actually changed since the last tick.
//
// This exists because of a real bug found verifying this project's very first
// sustained real client connection (everything before this was either a
// single-shot curl test or a client that disconnected within one heartbeat tick):
// reconcilePeers's own doc comment claimed reapplying an unchanged peer list via
// ConfigureDevice(ReplacePeers: true) every ~10s tick was "the same semantics as
// `wg syncconf` ... unchanged peers aren't touched/re-handshaked" - that assumption
// turned out to be wrong in practice (observed directly: a peer's handshake time
// and learned endpoint reset to zero/none within a live session, tracking the
// heartbeat cadence, even though its public key and allowed-ips never changed
// tick to tick). Skipping the reapply entirely when the desired set is unchanged
// sidesteps whatever layer (this kernel's wireguard module, or wgctrl's netlink
// encoding of an unchanged ReplacePeers call) is actually causing that, rather than
// depending on a "should be a no-op" assumption that measurably wasn't one.
func peerSignature(peers []heartbeatPeer) string {
	entries := make([]string, 0, len(peers))
	for _, p := range peers {
		allowedIPs := append([]string(nil), p.AllowedIPs...)
		sort.Strings(allowedIPs)
		// The bandwidth limit is part of the signature so a limit change alone (same
		// peers, new rate) still triggers a reconcile pass - tc shaping is applied in
		// the same signature-gated step as ConfigureDevice.
		limit := "-"
		if p.BandwidthLimitMbps != nil {
			limit = strconv.Itoa(*p.BandwidthLimitMbps)
		}
		// A PSK change must trigger a reconcile too. The signature only lives in memory.
		entries = append(entries, p.PublicKey+"|"+strings.Join(allowedIPs, ",")+"|"+limit+"|"+p.PresharedKey)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// readCPUPercent returns nil on the very first call (no prior sample to diff
// against) or if /proc/stat is unreadable - never a fabricated value.
func (s *heartbeatState) readCPUPercent() *float32 {
	cur, ok := readCPUSample()
	if !ok {
		s.havePrevCPU = false
		return nil
	}
	defer func() { s.prevCPU, s.havePrevCPU = cur, true }()

	if !s.havePrevCPU {
		return nil
	}
	return cpuPercentBetween(s.prevCPU, cur)
}

func sendHeartbeat(ctx context.Context, cfg config, client *http.Client, logger *slog.Logger, req heartbeatRequest) (heartbeatResponse, bool) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+cfg.PanelAddr+"/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		logger.Error("build_heartbeat_request_failed", "error", err)
		return heartbeatResponse{}, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Warn("heartbeat_failed", "error", err)
		return heartbeatResponse{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("heartbeat_rejected", "status", resp.StatusCode)
		return heartbeatResponse{}, false
	}

	var respBody heartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		logger.Warn("heartbeat_response_decode_failed", "error", err)
		return heartbeatResponse{}, false
	}
	return respBody, true
}

type deviceSnapshot struct {
	peers []wgtypes.Peer
}

// readDeviceSnapshot is the single wgctrl read per heartbeat tick - peer count,
// per-peer traffic counters, and last-handshake times all come from this one
// client.Device(iface) call, so they're mutually consistent within one tick rather
// than three independent snapshots (consolidated from Story 9's separate
// peer-count-only read per design review feedback). Returns ok=false (not an error)
// if the interface doesn't exist yet - true both after a host reboot before
// wg-quick@wg0 has run, and in any dev environment without a real interface at all.
func readDeviceSnapshot(iface string) (deviceSnapshot, bool) {
	client, err := wgctrl.New()
	if err != nil {
		return deviceSnapshot{}, false
	}
	defer client.Close()

	device, err := client.Device(iface)
	if err != nil {
		return deviceSnapshot{}, false
	}
	return deviceSnapshot{peers: device.Peers}, true
}

// reconcilePeers applies the control plane's desired peer list to the real WireGuard
// interface via wgctrl.ConfigureDevice with ReplacePeers: true (the desired list IS
// the complete membership; peers not in it are dropped). Only called when
// peerSignature has actually changed since the last successful call - see its doc
// comment for why calling this on every heartbeat tick regardless of whether
// anything changed is NOT the harmless no-op it looks like.
func reconcilePeers(iface string, desired []heartbeatPeer) error {
	client, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer client.Close()

	peerConfigs := make([]wgtypes.PeerConfig, 0, len(desired))
	for _, p := range desired {
		if pc, ok := peerConfigFor(p); ok {
			peerConfigs = append(peerConfigs, pc)
		}
	}

	return client.ConfigureDevice(iface, wgtypes.Config{
		ReplacePeers: true,
		Peers:        peerConfigs,
	})
}

// peerConfigFor turns one desired peer into its wgctrl config. ok=false for a malformed
// public key: that must not abort every other peer. Malformed AllowedIPs entries and a
// malformed PSK are skipped individually; a peer whose PSK cannot be parsed is applied
// without one (the handshake then fails for that client alone, visibly, rather than
// the whole interface failing to sync).
func peerConfigFor(p heartbeatPeer) (wgtypes.PeerConfig, bool) {
	pubKey, err := wgtypes.ParseKey(p.PublicKey)
	if err != nil {
		return wgtypes.PeerConfig{}, false
	}
	allowedIPs := make([]net.IPNet, 0, len(p.AllowedIPs))
	for _, cidr := range p.AllowedIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		allowedIPs = append(allowedIPs, *ipNet)
	}
	pc := wgtypes.PeerConfig{PublicKey: pubKey, AllowedIPs: allowedIPs}
	if p.PresharedKey != "" {
		if psk, err := wgtypes.ParseKey(p.PresharedKey); err == nil {
			pc.PresharedKey = &psk
		}
	}
	return pc, true
}
