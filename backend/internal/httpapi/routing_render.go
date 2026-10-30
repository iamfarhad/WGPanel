package httpapi

import (
	"context"
	"net/netip"
	"strings"

	"wgpanel-api/internal/routing"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// newEncryptedPSK mints a PresharedKey and encrypts it for storage, exactly as the
// account's private key is.
func newEncryptedPSK(hexKey string) (string, error) {
	psk, err := wgkeys.GeneratePresharedKey()
	if err != nil {
		return "", err
	}
	return wgkeys.Encrypt(hexKey, psk)
}

// peerRouting is what renderPeerConfig needs beyond keys and endpoint: the client's
// Address list, its AllowedIPs list and the optional PresharedKey.
type peerRouting struct {
	Address      string // "10.66.0.7/32" or "10.66.0.7/32, fd12:...::7/128"
	AllowedIPs   string
	PresharedKey string // "" = omit the line
}

// effectiveRoutingMode is the mode the account actually runs with: its own, else full.
func effectiveRoutingMode(mode *string) string {
	if mode != nil && routing.ValidMode(*mode) {
		return *mode
	}
	return string(routing.ModeFull)
}

// forceIncludeFor lists the IPv4 ranges that must stay in the tunnel in a split mode:
// the node's tunnel subnet, because the node's DNS forwarder lives on its .1 and the
// private-range exclusion (10.0.0.0/8) would otherwise swallow it - name resolution
// would silently leave the tunnel and the client would resolve through the (possibly
// filtered) local resolver; and any literal IPv4 resolver from the client_dns setting,
// for the same reason (a public resolver inside an excluded range).
func forceIncludeFor(nodeSubnet, dns string) []netip.Prefix {
	var out []netip.Prefix
	if p, err := netip.ParsePrefix(nodeSubnet); err == nil && p.Addr().Is4() {
		out = append(out, p.Masked())
	}
	for _, entry := range strings.Split(dns, ",") {
		if a, err := netip.ParseAddr(strings.TrimSpace(entry)); err == nil && a.Is4() {
			out = append(out, netip.PrefixFrom(a, 32))
		}
	}
	return out
}

// buildAllowedIPs renders the AllowedIPs value for a routing setting. ::/0 is in every
// mode - see the long comment on renderPeerConfig.
func buildAllowedIPs(r store.AccountRouting, nodeSubnet, dns string) (string, error) {
	spec := routing.Spec{
		Mode:         routing.Mode(effectiveRoutingMode(r.Mode)),
		Precision:    routing.PrecisionCompact,
		ForceInclude: forceIncludeFor(nodeSubnet, dns),
	}
	if r.Precision != nil && routing.ValidPrecision(*r.Precision) {
		spec.Precision = routing.Precision(*r.Precision)
	}
	if spec.Mode == routing.ModeCustom {
		ps, err := routing.ParseIPv4CIDRs(r.ExcludedCIDRs)
		if err != nil {
			return "", err
		}
		spec.CustomExcluded = ps
	}
	return strings.Join(routing.AllowedIPs(spec), ", "), nil
}

// peerRoutingConfig gathers the routing, IPv6 and PSK parts of one peer's config.
// Every lookup that fails degrades to today's plain config (full tunnel, IPv4 only, no
// PSK) rather than failing config delivery: a client with a working-but-plainer config
// is better than none.
func (s *Server) peerRoutingConfig(ctx context.Context, accountID string, target store.AccountPeerWithNode, dns string) peerRouting {
	out := peerRouting{
		Address:    target.AssignedIP + "/32",
		AllowedIPs: "0.0.0.0/0, ::/0",
	}

	if r, err := s.Store.GetAccountRouting(ctx, accountID); err != nil {
		s.Logger.Warn("get_account_routing_failed", "error", err)
	} else if allowed, err := buildAllowedIPs(r, target.NodeWGSubnet, dns); err != nil {
		s.Logger.Warn("build_allowed_ips_failed", "account_id", accountID, "error", err)
	} else {
		out.AllowedIPs = allowed
	}

	nr, err := s.Store.GetNodeRouting(ctx, target.NodeID)
	if err != nil {
		s.Logger.Warn("get_node_routing_failed", "error", err)
		return out
	}

	// IPv6 only where the admin enabled it AND the agent reported it working.
	if nr.IPv6Active() {
		if v6, err := routing.PeerV6(*nr.IPv6ULAPrefix, target.NodeWGSubnet, target.AssignedIP); err != nil {
			s.Logger.Warn("derive_peer_v6_failed", "error", err)
		} else {
			out.Address += ", " + v6.String() + "/128"
		}
	}

	// PresharedKey: a node peer may require it only if every config ever served for this
	// (account, node) carried it (store.ServeConfigPSK, migration 0029). So the key is
	// offered only when the node's agent can apply it and the account has one, and the
	// store then refuses it if a PSK-less config was ever served for this peer (always
	// the case for accounts that predate the feature). Every PSK-less serve pins the peer
	// to plain mode for good, which is what keeps already-imported configs working.
	psk := ""
	if nr.PSKSupported {
		if enc, err := s.Store.GetAccountPSK(ctx, accountID); err != nil {
			s.Logger.Warn("get_account_psk_failed", "error", err)
		} else if enc != nil {
			if p, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, *enc); err != nil {
				s.Logger.Warn("decrypt_account_psk_failed", "error", err)
			} else {
				psk = p
			}
		}
	}
	use, err := s.Store.ServeConfigPSK(ctx, accountID, target.NodeID, psk != "")
	if err != nil {
		s.Logger.Warn("serve_config_psk_failed", "error", err)
	} else if use {
		out.PresharedKey = psk
	}
	return out
}
