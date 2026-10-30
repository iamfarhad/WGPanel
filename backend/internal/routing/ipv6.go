package routing

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
)

// Per-node IPv6: each node owns a ULA /64 and every peer's v6 address is derived from
// its IPv4 address - prefix | host index - so there is no second allocator to keep in
// sync (and nothing to store per peer): the index is the IPv4 address's offset from the
// node subnet's network address. The node's own interface gets index 1, exactly as its
// IPv4 address is the subnet's .1 (see ipalloc.Gateway), and ipalloc reserves that
// index so no peer collides with it.

// GenerateULAPrefix returns a random RFC 4193 /64: fd + 40 random bits (global ID) + 16
// random bits (subnet ID). 56 random bits make a collision between two nodes' prefixes
// negligible without any coordination.
func GenerateULAPrefix() (netip.Prefix, error) {
	var b [16]byte
	if _, err := rand.Read(b[1:8]); err != nil {
		return netip.Prefix{}, err
	}
	b[0] = 0xfd
	return netip.PrefixFrom(netip.AddrFrom16(b), 64), nil
}

// NormalizeULAPrefix validates an admin-provided node prefix: it must be an IPv6 /64
// inside fc00::/7 (unique local), and is returned masked.
func NormalizeULAPrefix(raw string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(raw)
	if err != nil || !p.Addr().Is6() || p.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("ipv6_ula_prefix must be an IPv6 /64 such as fd12:3456:789a:1::/64")
	}
	if p.Bits() != 64 {
		return netip.Prefix{}, errors.New("ipv6_ula_prefix must be a /64")
	}
	if !netip.MustParsePrefix("fc00::/7").Contains(p.Addr()) {
		return netip.Prefix{}, errors.New("ipv6_ula_prefix must be a unique-local prefix (fc00::/7): the node NATs it to its own global address")
	}
	return p.Masked(), nil
}

// HostIndex is ip's offset from the network address of subnet (an IPv4 CIDR).
func HostIndex(subnet, ip string) (uint64, error) {
	sp, err := netip.ParsePrefix(subnet)
	if err != nil || !sp.Addr().Is4() {
		return 0, fmt.Errorf("invalid IPv4 subnet %q", subnet)
	}
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || !sp.Contains(a) {
		return 0, fmt.Errorf("address %q is not inside %s", ip, subnet)
	}
	return uint64(addrToU32(a)) - uint64(addrToU32(sp.Masked().Addr())), nil
}

func addrToU32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// PeerV6 derives a peer's IPv6 address: the node's /64 with the IPv4 host index in the
// low 32 bits.
func PeerV6(ulaPrefix, wgSubnet, assignedIPv4 string) (netip.Addr, error) {
	pfx, err := netip.ParsePrefix(ulaPrefix)
	if err != nil || !pfx.Addr().Is6() || pfx.Bits() != 64 {
		return netip.Addr{}, fmt.Errorf("invalid node IPv6 prefix %q", ulaPrefix)
	}
	idx, err := HostIndex(wgSubnet, assignedIPv4)
	if err != nil {
		return netip.Addr{}, err
	}
	b := pfx.Masked().Addr().As16()
	b[12], b[13], b[14], b[15] = byte(idx>>24), byte(idx>>16), byte(idx>>8), byte(idx)
	return netip.AddrFrom16(b), nil
}

// NodeV6Gateway is the node's own address on its tunnel interface: index 1 of the
// prefix, with the /64 length.
func NodeV6Gateway(ulaPrefix string) (netip.Prefix, error) {
	pfx, err := netip.ParsePrefix(ulaPrefix)
	if err != nil || !pfx.Addr().Is6() || pfx.Bits() != 64 {
		return netip.Prefix{}, fmt.Errorf("invalid node IPv6 prefix %q", ulaPrefix)
	}
	b := pfx.Masked().Addr().As16()
	b[15] = 1
	return netip.PrefixFrom(netip.AddrFrom16(b), 64), nil
}
