package httpapi

import (
	"context"

	"wgpanel-api/internal/routing"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// routingAgentReport is what an agent says about itself on a heartbeat (embedded in
// agentHeartbeatRequest). All three are false from an agent that predates the feature,
// which is exactly what keeps such a node on plain configs: no PSK, no IPv6.
type routingAgentReport struct {
	// PSKSupported: the agent sets PresharedKey on the peers it is given.
	PSKSupported bool `json:"psk_supported"`
	// IPv6Global: the host has a working global IPv6 route.
	IPv6Global bool `json:"ipv6_global"`
	// IPv6Ready: forwarding, NAT66 and the tunnel's v6 address are applied.
	IPv6Ready bool `json:"ipv6_ready"`
}

// agentIPv6 tells the agent whether (and with what prefix) to prepare the node for IPv6
// egress. Always present from a panel that knows the feature, with enabled=false when
// off, so the agent can tear down what it set up; absent from older panels, in which
// case the agent leaves IPv6 alone.
type agentIPv6 struct {
	Enabled bool `json:"enabled"`
	// Prefix is the node's ULA /64; Gateway the node's own address in it (index 1) with
	// the prefix length, to put on the tunnel interface.
	Prefix  string `json:"prefix,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

// recordAgentRoutingState stores the agent's self-reported capabilities.
func (s *Server) recordAgentRoutingState(ctx context.Context, nodeID string, rep routingAgentReport) {
	if err := s.Store.RecordNodeRoutingState(ctx, nodeID, rep.PSKSupported, rep.IPv6Global, rep.IPv6Ready); err != nil {
		s.Logger.Warn("record_node_routing_state_failed", "node_id", nodeID, "error", err)
	}
}

// augmentHeartbeatRouting adds the PSK and IPv6 parts to the desired-peer list (peers is
// index-aligned with desired) and the IPv6 instruction to the response. Best effort,
// like the rest of the heartbeat's side channels: a failure leaves the plain peer list,
// which is still correct for a node that never gets the new features.
//
// PresharedKey goes only to a node whose agent reported psk support on THIS heartbeat,
// and only for peers whose key has been issued to a client (store.MarkPeerPSKIssued) -
// both sides of the handshake must agree, so a peer never requires a key its client was
// not handed. The /128 goes to the peer only while IPv6 is active for the node, the same
// condition under which client configs carry the address.
func (s *Server) augmentHeartbeatRouting(ctx context.Context, node store.Node, rep routingAgentReport, desired []store.DesiredPeer, peers []agentHeartbeatPeer, resp *agentHeartbeatResponse) {
	nr, err := s.Store.GetNodeRouting(ctx, node.ID)
	if err != nil {
		s.Logger.Warn("get_node_routing_for_heartbeat_failed", "error", err)
		return
	}

	if nr.IPv6Enabled && nr.IPv6ULAPrefix != nil {
		v6 := &agentIPv6{Enabled: true, Prefix: *nr.IPv6ULAPrefix}
		if gw, err := routing.NodeV6Gateway(*nr.IPv6ULAPrefix); err == nil {
			v6.Gateway = gw.String()
		}
		resp.IPv6 = v6
	} else {
		resp.IPv6 = &agentIPv6{}
	}

	if nr.IPv6Active() {
		for i, d := range desired {
			if v6, err := routing.PeerV6(*nr.IPv6ULAPrefix, node.WGSubnet, d.AssignedIP); err == nil {
				peers[i].AllowedIPs = append(peers[i].AllowedIPs, v6.String()+"/128")
			}
		}
	}

	if rep.PSKSupported {
		issued, err := s.Store.ListIssuedPSKsForNode(ctx, node.ID)
		if err != nil {
			s.Logger.Warn("list_issued_psks_failed", "error", err)
			return
		}
		for i, d := range desired {
			enc, ok := issued[d.PublicKey]
			if !ok {
				continue
			}
			psk, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, enc)
			if err != nil {
				s.Logger.Warn("decrypt_peer_psk_failed", "error", err)
				continue
			}
			peers[i].PresharedKey = psk
		}
	}
}
