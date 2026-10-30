package routing

import (
	"net/netip"
	"sync"
)

// Mode is how much of a client's traffic goes through the tunnel.
type Mode string

const (
	// ModeFull sends everything through the tunnel (0.0.0.0/0, ::/0): the historical
	// behaviour and the default.
	ModeFull Mode = "full"
	// ModeBypassIran sends everything except Iranian IPv4 ranges and private/LAN ranges.
	ModeBypassIran Mode = "bypass_iran"
	// ModeCustom sends everything except admin-entered CIDRs.
	ModeCustom Mode = "custom"
)

// ValidMode reports whether s names a routing mode.
func ValidMode(s string) bool {
	switch Mode(s) {
	case ModeFull, ModeBypassIran, ModeCustom:
		return true
	}
	return false
}

// Precision selects how exactly the excluded set is represented.
type Precision string

const (
	// PrecisionCompact (the default) caps the config at about CompactBudget IPv4 entries by
	// flipping the cheapest slivers of address space (see Compact): a few foreign IPs may
	// bypass the tunnel and the smallest excluded prefixes ride it. A deliberate trade
	// for a config WireGuard clients import quickly.
	PrecisionCompact Precision = "compact"
	// PrecisionExact keeps the excluded set exactly; the resulting AllowedIPs list is
	// several times longer.
	PrecisionExact Precision = "exact"
)

// ValidPrecision reports whether s names a precision.
func ValidPrecision(s string) bool {
	return Precision(s) == PrecisionCompact || Precision(s) == PrecisionExact
}

// MaxCustomExcluded bounds the admin-entered list: each entry can split the complement
// further, and a config with tens of thousands of routes is unusable on phones.
const MaxCustomExcluded = 500

// Spec is everything AllowedIPs needs.
type Spec struct {
	Mode Mode
	// CustomExcluded are the admin's CIDRs (ModeCustom only).
	CustomExcluded []netip.Prefix
	Precision      Precision
	// ForceInclude are IPv4 ranges that must stay in the tunnel even when they fall inside
	// an excluded range - the node's own tunnel subnet (the DNS forwarder lives there; the
	// private 10.0.0.0/8 exclusion would otherwise swallow it and break name resolution)
	// and any literal IPv4 resolver from client_dns.
	ForceInclude []netip.Prefix
}

// AllowedIPs returns the entries for the client's AllowedIPs line, "::/0" always last.
// IPv6 is never split: see the comment in renderPeerConfig about the v6 leak.
func AllowedIPs(sp Spec) []string {
	v4 := AllowedIPv4(sp)
	out := make([]string, 0, len(v4)+1)
	for _, p := range v4 {
		out = append(out, p.String())
	}
	return append(out, "::/0")
}

// AllowedIPv4 is the IPv4 part of AllowedIPs.
func AllowedIPv4(sp Spec) []netip.Prefix {
	var excluded []netip.Prefix
	switch sp.Mode {
	case ModeBypassIran:
		excluded = append(append(excluded, IranIPv4()...), PrivateIPv4()...)
	case ModeCustom:
		excluded = sp.CustomExcluded
	default:
		return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	}
	if len(excluded) == 0 {
		return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	}
	var ex []span
	switch {
	case sp.Precision == PrecisionExact:
		ex = mergeSpans(prefixesToSpans(excluded))
	case sp.Mode == ModeBypassIran:
		ex = compactIranSpans()
	default:
		ex = Compact(mergeSpans(prefixesToSpans(excluded)), CompactBudget)
	}
	in := complementSpans(ex)
	in = append(in, prefixesToSpans(sp.ForceInclude)...)
	return spansToPrefixes(mergeSpans(in))
}

// CompactBudget is the IPv4 entry budget of PrecisionCompact. See Compact.
const CompactBudget = 1000

var (
	compactIranOnce sync.Once
	compactIran     []span
)

// compactIranSpans is Compact over the Iran+private set, computed once: that set only
// changes with the embedded file, and the computation takes ~100ms.
func compactIranSpans() []span {
	compactIranOnce.Do(func() {
		ex := append(append([]netip.Prefix{}, IranIPv4()...), PrivateIPv4()...)
		compactIran = Compact(mergeSpans(prefixesToSpans(ex)), CompactBudget)
	})
	return compactIran
}

// segment is one maximal run of the address space that is either excluded or included.
type segment struct {
	span
	excluded bool
}

// coverLen is len(spanPrefixes(span{lo, hi})) without allocating: Compact calls it
// millions of times.
func coverLen(lo, hi uint32) int {
	n := 0
	l, h := uint64(lo), uint64(hi)
	for l <= h {
		size := uint64(1) << 32
		if l != 0 {
			size = l & -l
		}
		for size > h-l+1 {
			size >>= 1
		}
		l += size
		n++
	}
	return n
}

func segmentsOf(ex []span) []segment {
	var segs []segment
	next := uint64(0)
	for _, x := range ex {
		if uint64(x.lo) > next {
			segs = append(segs, segment{span{uint32(next), x.lo - 1}, false})
		}
		segs = append(segs, segment{x, true})
		next = uint64(x.hi) + 1
	}
	if next <= 0xFFFFFFFF {
		segs = append(segs, segment{span{uint32(next), 0xFFFFFFFF}, false})
	}
	return segs
}

func includedEntries(segs []segment) int {
	n := 0
	for _, s := range segs {
		if !s.excluded {
			n += coverLen(s.lo, s.hi)
		}
	}
	return n
}

// Compact rewrites the merged excluded spans so the included space needs at most budget
// CIDR entries (when it can), by flipping the cheapest segments of the address space:
//
//   - closing a gap (a foreign stretch between two excluded ranges) excludes foreign
//     addresses, which then connect directly instead of via the node;
//   - dropping a small excluded span includes it, so those (Iranian/custom) addresses
//     ride the tunnel instead of going direct.
//
// "Cheapest" is addresses misrouted per entry saved. Both kinds of error are small next to
// the space they remove entries for: the thousands of /24-/20 slivers scattered through
// the Iranian allocations cost a few hundred addresses each, while the long foreign
// stretches (millions of addresses for one entry) are never touched.
//
// ex must be merged. The result is merged. Cost is O(n^2) in the number of spans; the
// callers cache the bypass_iran result and cap custom lists.
func Compact(ex []span, budget int) []span {
	if budget <= 0 {
		return ex
	}
	segs := segmentsOf(ex)
	entries := includedEntries(segs)
	for entries > budget {
		best, bestSaved, bestCost := -1, 0, 0.0
		for k := range segs {
			size := float64(uint64(segs[k].hi) - uint64(segs[k].lo) + 1)
			var saved int
			if !segs[k].excluded {
				saved = coverLen(segs[k].lo, segs[k].hi)
			} else if k > 0 && k+1 < len(segs) {
				// Dropping an excluded span fuses the gaps on both sides with it. At the very
				// edges (no gap on one side) there is nothing to fuse, so it is not a candidate.
				a, b := segs[k-1], segs[k+1]
				saved = coverLen(a.lo, a.hi) + coverLen(b.lo, b.hi) - coverLen(a.lo, b.hi)
			}
			if saved <= 0 {
				continue
			}
			cost := size / float64(saved)
			if best < 0 || cost < bestCost {
				best, bestSaved, bestCost = k, saved, cost
			}
		}
		if best < 0 {
			break
		}
		entries -= bestSaved
		segs[best].excluded = !segs[best].excluded
		// Fuse equal-type neighbours back into maximal segments.
		merged := segs[:0:0]
		for _, s := range segs {
			if n := len(merged); n > 0 && merged[n-1].excluded == s.excluded {
				merged[n-1].hi = s.hi
				continue
			}
			merged = append(merged, s)
		}
		segs = merged
	}
	var out []span
	for _, s := range segs {
		if s.excluded {
			out = append(out, s.span)
		}
	}
	return out
}
