package routing

import (
	"math/rand"
	"net/netip"
	"strings"
	"testing"
)

func pfx(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func total(s []span) uint64 {
	var n uint64
	for _, x := range s {
		n += uint64(x.hi) - uint64(x.lo) + 1
	}
	return n
}

func prefixTotal(ps []netip.Prefix) uint64 {
	return total(prefixesToSpans(ps))
}

// assertDisjointSorted fails if ps overlap or are out of order.
func assertDisjointSorted(t *testing.T, ps []netip.Prefix) {
	t.Helper()
	var prevEnd uint64
	for i, p := range ps {
		s := prefixSpan(p)
		if i > 0 && uint64(s.lo) < prevEnd {
			t.Fatalf("%s overlaps or precedes the previous entry (previous ended at %d)", p, prevEnd)
		}
		prevEnd = uint64(s.hi) + 1
	}
}

func TestAggregateMergesAdjacentAndOverlapping(t *testing.T) {
	got := Aggregate(pfx("10.0.0.0/25", "10.0.0.128/25", "10.0.0.64/26", "10.0.1.0/24"))
	want := pfx("10.0.0.0/23")
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Aggregate = %v, want %v", got, want)
	}
}

func TestComplementEdges(t *testing.T) {
	if got := Complement(nil); len(got) != 1 || got[0].String() != "0.0.0.0/0" {
		t.Fatalf("Complement(nothing) = %v, want 0.0.0.0/0", got)
	}
	if got := Complement(pfx("0.0.0.0/0")); len(got) != 0 {
		t.Fatalf("Complement(everything) = %v, want empty", got)
	}
	// Touching both ends of the space must not wrap around.
	got := Complement(pfx("0.0.0.0/8", "255.0.0.0/8"))
	if prefixTotal(got) != 1<<32-2<<24 {
		t.Fatalf("complement of two /8 edge blocks covers %d addresses", prefixTotal(got))
	}
	assertDisjointSorted(t, got)
}

// The invariant that matters: excluded + complement is exactly the whole IPv4 space,
// with no address in both. Checked on random sets, including the real Iranian list.
func TestComplementPartitionsTheSpace(t *testing.T) {
	check := func(name string, ex []netip.Prefix) {
		t.Helper()
		comp := Complement(ex)
		assertDisjointSorted(t, comp)
		agg := Aggregate(ex)
		assertDisjointSorted(t, agg)
		if got := prefixTotal(agg) + prefixTotal(comp); got != 1<<32 {
			t.Fatalf("%s: excluded(%d) + complement(%d) = %d, want 2^32", name, prefixTotal(agg), prefixTotal(comp), got)
		}
		// No overlap between the two halves.
		a, c := prefixesToSpans(agg), prefixesToSpans(comp)
		if n := inter(a, c); n != 0 {
			t.Fatalf("%s: %d addresses are both excluded and included", name, n)
		}
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		var ex []netip.Prefix
		for j := rng.Intn(40); j >= 0; j-- {
			bits := 8 + rng.Intn(25)
			a := [4]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}
			ex = append(ex, netip.PrefixFrom(netip.AddrFrom4(a), bits).Masked())
		}
		check("random", ex)
	}
	check("iran+private", append(append([]netip.Prefix{}, IranIPv4()...), PrivateIPv4()...))
}

func inter(a, b []span) uint64 {
	var n uint64
	for _, x := range a {
		for _, y := range b {
			lo, hi := x.lo, x.hi
			if y.lo > lo {
				lo = y.lo
			}
			if y.hi < hi {
				hi = y.hi
			}
			if lo <= hi {
				n += uint64(hi) - uint64(lo) + 1
			}
		}
	}
	return n
}

func TestRangePrefixesHandlesNonPowerOfTwoCounts(t *testing.T) {
	// RIR files list e.g. 768 addresses = /23 + /24.
	got, err := RangePrefixes(netip.MustParseAddr("5.160.0.0"), 768)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "5.160.0.0/23" || got[1].String() != "5.160.2.0/24" {
		t.Fatalf("RangePrefixes = %v", got)
	}
	if _, err := RangePrefixes(netip.MustParseAddr("255.255.255.0"), 512); err == nil {
		t.Fatal("a range running past 255.255.255.255 must be rejected")
	}
}

func TestEmbeddedIranList(t *testing.T) {
	ir := IranIPv4()
	if len(ir) < 1000 {
		t.Fatalf("embedded Iranian list has only %d prefixes - generator output missing or truncated", len(ir))
	}
	assertDisjointSorted(t, ir)
	hdr := IranListHeader()
	for _, want := range []string{"ripe.net", "as-of date", "Generated"} {
		if !strings.Contains(hdr, want) {
			t.Fatalf("list header lacks %q: %q", want, hdr)
		}
	}
	// Spot checks that need no network: well-known Iranian and foreign addresses.
	contains := func(ps []netip.Prefix, a string) bool {
		ad := netip.MustParseAddr(a)
		for _, p := range ps {
			if p.Contains(ad) {
				return true
			}
		}
		return false
	}
	if contains(ir, "8.8.8.8") || contains(ir, "1.1.1.1") {
		t.Fatal("public foreign resolvers must not be in the Iranian list")
	}
	if !contains(ir, "2.144.0.1") { // TCI mobile space, first lines of the list
		t.Fatal("expected 2.144.0.0/14 in the list")
	}
}

func covers(ps []string, ip string) bool {
	ad := netip.MustParseAddr(ip)
	for _, s := range ps {
		p := netip.MustParsePrefix(s)
		if p.Contains(ad) {
			return true
		}
	}
	return false
}

func TestAllowedIPsModes(t *testing.T) {
	node := pfx("10.66.0.0/24")

	full := AllowedIPs(Spec{Mode: ModeFull})
	if len(full) != 2 || full[0] != "0.0.0.0/0" || full[1] != "::/0" {
		t.Fatalf("full = %v, want exactly the historical 0.0.0.0/0, ::/0", full)
	}
	if got := AllowedIPs(Spec{}); len(got) != 2 {
		t.Fatalf("empty spec must behave as full, got %v", got)
	}

	for _, prec := range []Precision{PrecisionExact, PrecisionCompact} {
		got := AllowedIPs(Spec{Mode: ModeBypassIran, Precision: prec, ForceInclude: node})
		if got[len(got)-1] != "::/0" {
			t.Fatalf("%s: ::/0 must stay in AllowedIPs (IPv6 leak protection), got tail %q", prec, got[len(got)-1])
		}
		// Private ranges are excluded...
		for _, ip := range []string{"192.168.1.10", "172.20.0.5", "169.254.1.1", "10.1.2.3", "127.0.0.1"} {
			if covers(got, ip) {
				t.Fatalf("%s: private address %s must bypass the tunnel", prec, ip)
			}
		}
		// ...except the node's own tunnel subnet: the DNS forwarder lives on its .1.
		for _, ip := range []string{"10.66.0.1", "10.66.0.200"} {
			if !covers(got, ip) {
				t.Fatalf("%s: %s (node subnet / DNS) must stay in the tunnel", prec, ip)
			}
		}
		// Foreign addresses ride the tunnel.
		for _, ip := range []string{"8.8.8.8", "1.1.1.1", "142.250.1.1", "151.101.1.1"} {
			if !covers(got, ip) {
				t.Fatalf("%s: foreign %s must be tunnelled", prec, ip)
			}
		}
		// The big Iranian block 2.144.0.0/14 goes direct in both precisions.
		if covers(got, "2.145.10.10") {
			t.Fatalf("%s: Iranian 2.145.10.10 must bypass the tunnel", prec)
		}
	}

	exact := AllowedIPs(Spec{Mode: ModeBypassIran, Precision: PrecisionExact, ForceInclude: node})
	compact := AllowedIPs(Spec{Mode: ModeBypassIran, Precision: PrecisionCompact, ForceInclude: node})
	t.Logf("bypass_iran AllowedIPs entries: exact=%d compact=%d (incl. ::/0)", len(exact), len(compact))
	if len(compact) > CompactBudget+len(node)*8+16 {
		t.Fatalf("compact has %d entries, budget is %d", len(compact), CompactBudget)
	}
	if len(exact) <= len(compact) {
		t.Fatalf("exact (%d) should be much longer than compact (%d)", len(exact), len(compact))
	}
	if len(compact) < 100 {
		t.Fatalf("compact collapsed to %d entries - the compaction is dropping far too much", len(compact))
	}
}

func TestAllowedIPsExactIsTheExactComplement(t *testing.T) {
	// Exact precision: AllowedIPv4 ∪ (Iran ∪ private − forced) = whole space, disjoint.
	node := pfx("10.66.0.0/24")
	allowed := AllowedIPv4(Spec{Mode: ModeBypassIran, Precision: PrecisionExact, ForceInclude: node})
	assertDisjointSorted(t, allowed)
	ex := mergeSpans(prefixesToSpans(append(append([]netip.Prefix{}, IranIPv4()...), PrivateIPv4()...)))
	// The forced range is carved back out of the excluded side for the arithmetic.
	forced := prefixesToSpans(node)
	if got := prefixTotal(allowed) + total(ex) - inter(ex, forced); got != 1<<32 {
		t.Fatalf("allowed + excluded = %d, want 2^32", got)
	}
}

func TestAllowedIPsCustom(t *testing.T) {
	ex, err := ParseIPv4CIDRs([]string{"203.0.113.0/24", "198.51.100.7"})
	if err != nil {
		t.Fatal(err)
	}
	got := AllowedIPs(Spec{Mode: ModeCustom, CustomExcluded: ex, Precision: PrecisionExact})
	if covers(got, "203.0.113.9") || covers(got, "198.51.100.7") {
		t.Fatalf("custom exclusions must bypass the tunnel: %v", got)
	}
	if !covers(got, "198.51.100.8") || !covers(got, "8.8.8.8") {
		t.Fatalf("everything else must ride the tunnel: %v", got)
	}
	if got[len(got)-1] != "::/0" {
		t.Fatalf("::/0 missing: %v", got)
	}
	// Custom with nothing to exclude degenerates to full.
	if got := AllowedIPs(Spec{Mode: ModeCustom}); len(got) != 2 || got[0] != "0.0.0.0/0" {
		t.Fatalf("custom with no exclusions = %v, want full", got)
	}
	// A small custom list is never altered by compaction.
	small := AllowedIPs(Spec{Mode: ModeCustom, CustomExcluded: ex, Precision: PrecisionCompact})
	if strings.Join(small, ",") != strings.Join(got, ",") {
		t.Fatalf("compaction changed a list already under budget:\n%v\n%v", small, got)
	}
}

func TestCompactNeverExceedsOrLeavesInvalidSpans(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var ex []netip.Prefix
	for i := 0; i < 300; i++ {
		a := [4]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), 0}
		ex = append(ex, netip.PrefixFrom(netip.AddrFrom4(a), 24).Masked())
	}
	m := mergeSpans(prefixesToSpans(ex))
	out := Compact(m, 50)
	entries := len(spansToPrefixes(complementSpans(out)))
	if entries > 50 {
		t.Fatalf("Compact left %d entries, budget 50", entries)
	}
	// Sorted and disjoint.
	for i := 1; i < len(out); i++ {
		if uint64(out[i].lo) <= uint64(out[i-1].hi)+1 {
			t.Fatalf("Compact output not merged at %d", i)
		}
	}
}

func TestParseIPv4CIDRs(t *testing.T) {
	got, err := ParseIPv4CIDRs([]string{" 10.0.0.5/8 ", "192.0.2.1", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "10.0.0.0/8" || got[1].String() != "192.0.2.1/32" {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"nope", "2001:db8::/32", "10.0.0.0/33", "1.2.3"} {
		if _, err := ParseIPv4CIDRs([]string{bad}); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestV6Derivation(t *testing.T) {
	prefix := "fd12:3456:789a:1::/64"
	seen := map[netip.Addr]string{}
	// Every host index of a /24 subnet maps to a unique address inside the /64.
	for i := 2; i < 255; i++ {
		ip := netip.AddrFrom4([4]byte{10, 66, 0, byte(i)}).String()
		a, err := PeerV6(prefix, "10.66.0.0/24", ip)
		if err != nil {
			t.Fatal(err)
		}
		if !netip.MustParsePrefix(prefix).Contains(a) {
			t.Fatalf("%s outside %s", a, prefix)
		}
		if prev, dup := seen[a]; dup {
			t.Fatalf("%s and %s both map to %s", prev, ip, a)
		}
		seen[a] = ip
	}
	a, _ := PeerV6(prefix, "10.66.0.0/24", "10.66.0.7")
	if a.String() != "fd12:3456:789a:1::7" {
		t.Fatalf("10.66.0.7 -> %s", a)
	}
	// A /16 subnet uses the full 16-bit host index.
	b, _ := PeerV6(prefix, "10.68.0.0/16", "10.68.3.4")
	if b.String() != "fd12:3456:789a:1::304" {
		t.Fatalf("10.68.3.4 in /16 -> %s", b)
	}
	// The node itself is index 1, matching its IPv4 .1.
	gw, err := NodeV6Gateway(prefix)
	if err != nil || gw.String() != "fd12:3456:789a:1::1/64" {
		t.Fatalf("gateway = %v, %v", gw, err)
	}
	g4, _ := PeerV6(prefix, "10.66.0.0/24", "10.66.0.1")
	if g4 != gw.Addr() {
		t.Fatalf("the v4 gateway .1 must map to the v6 gateway: %s vs %s", g4, gw.Addr())
	}
	if _, err := PeerV6(prefix, "10.66.0.0/24", "10.67.0.1"); err == nil {
		t.Fatal("an address outside the subnet must be rejected")
	}
}

func TestULAPrefixes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := GenerateULAPrefix()
		if err != nil {
			t.Fatal(err)
		}
		if p.Bits() != 64 || p.Addr().As16()[0] != 0xfd {
			t.Fatalf("not an fd00::/8 /64: %s", p)
		}
		if _, err := NormalizeULAPrefix(p.String()); err != nil {
			t.Fatalf("generated prefix rejected: %v", err)
		}
		if seen[p.String()] {
			t.Fatalf("duplicate generated prefix %s", p)
		}
		seen[p.String()] = true
	}
	for _, bad := range []string{"2001:db8::/64", "fd00::/48", "10.0.0.0/8", "nope", "fd00::1/128"} {
		if _, err := NormalizeULAPrefix(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	if p, err := NormalizeULAPrefix("fd12:3456:789a:1::5/64"); err != nil || p.String() != "fd12:3456:789a:1::/64" {
		t.Fatalf("normalize = %v, %v", p, err)
	}
}
