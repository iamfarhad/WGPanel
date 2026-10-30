// Package routing computes the client-side AllowedIPs for the split-tunnel modes
// (full, bypass_iran, custom) and holds the embedded list of Iranian IPv4 ranges.
//
// Everything works on IPv4 address *ranges* (uint32 start/end) rather than on
// prefixes: merging, subtracting and complementing are trivial on sorted ranges, and
// the minimal CIDR cover of the result is produced in one last step. net/netip is the
// parse/format layer; it never does the set arithmetic itself.
package routing

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// span is an inclusive IPv4 address range.
type span struct{ lo, hi uint32 }

func prefixSpan(p netip.Prefix) span {
	a := p.Masked().Addr().As4()
	lo := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	size := uint64(1) << (32 - p.Bits())
	return span{lo, uint32(uint64(lo) + size - 1)}
}

// mergeSpans sorts and coalesces overlapping or adjacent spans.
func mergeSpans(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	s := append([]span(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].lo < s[j].lo })
	out := []span{s[0]}
	for _, x := range s[1:] {
		last := &out[len(out)-1]
		// uint64 so hi+1 cannot wrap at 255.255.255.255.
		if uint64(x.lo) <= uint64(last.hi)+1 {
			if x.hi > last.hi {
				last.hi = x.hi
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// complementSpans returns the gaps of merged (sorted, disjoint) spans within the
// whole IPv4 space.
func complementSpans(merged []span) []span {
	var out []span
	next := uint64(0)
	for _, x := range merged {
		if uint64(x.lo) > next {
			out = append(out, span{uint32(next), x.lo - 1})
		}
		next = uint64(x.hi) + 1
	}
	if next <= 0xFFFFFFFF {
		out = append(out, span{uint32(next), 0xFFFFFFFF})
	}
	return out
}

// spanPrefixes is the minimal CIDR cover of one span: repeatedly take the largest
// aligned block that starts at lo and still fits.
func spanPrefixes(x span) []netip.Prefix {
	var out []netip.Prefix
	lo := uint64(x.lo)
	hi := uint64(x.hi)
	for lo <= hi {
		// Largest block permitted by lo's alignment (lo == 0 is aligned to anything)...
		size := uint64(1) << 32
		if lo != 0 {
			size = lo & -lo
		}
		// ...and by the remaining length.
		for size > hi-lo+1 {
			size >>= 1
		}
		bits := 32
		for s := size; s > 1; s >>= 1 {
			bits--
		}
		a := [4]byte{byte(lo >> 24), byte(lo >> 16), byte(lo >> 8), byte(lo)}
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(a), bits))
		lo += size
	}
	return out
}

func spansToPrefixes(spans []span) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range spans {
		out = append(out, spanPrefixes(x)...)
	}
	return out
}

func prefixesToSpans(ps []netip.Prefix) []span {
	out := make([]span, 0, len(ps))
	for _, p := range ps {
		out = append(out, prefixSpan(p))
	}
	return out
}

// Aggregate returns the minimal list of CIDRs covering exactly the union of ps
// (sorted, non-overlapping). IPv4 only.
func Aggregate(ps []netip.Prefix) []netip.Prefix {
	return spansToPrefixes(mergeSpans(prefixesToSpans(ps)))
}

// Complement returns the minimal list of CIDRs covering every IPv4 address NOT in ps.
func Complement(ps []netip.Prefix) []netip.Prefix {
	return spansToPrefixes(complementSpans(mergeSpans(prefixesToSpans(ps))))
}

// ParseIPv4CIDRs parses user/embedded CIDR text. Host bits are masked off (an admin
// pasting "10.0.0.5/8" means 10.0.0.0/8), a bare address is a /32, and IPv6 is
// rejected: the excluded set is IPv4-only because ::/0 stays in AllowedIPs in every
// mode.
func ParseIPv4CIDRs(in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			a, aerr := netip.ParseAddr(raw)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not a valid CIDR", raw)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if !p.Addr().Is4() {
			return nil, fmt.Errorf("%q: only IPv4 CIDRs can be excluded", raw)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// RangePrefixes converts a "start address + number of addresses" block, as RIR
// delegation files list them (the count need not be a power of two), to its minimal
// CIDR cover.
func RangePrefixes(start netip.Addr, count uint64) ([]netip.Prefix, error) {
	if !start.Is4() || count == 0 {
		return nil, fmt.Errorf("invalid range %s+%d", start, count)
	}
	a := start.As4()
	lo := uint64(a[0])<<24 | uint64(a[1])<<16 | uint64(a[2])<<8 | uint64(a[3])
	if lo+count > 1<<32 {
		return nil, fmt.Errorf("range %s+%d overflows the IPv4 space", start, count)
	}
	return spanPrefixes(span{uint32(lo), uint32(lo + count - 1)}), nil
}

func formatPrefixes(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}
