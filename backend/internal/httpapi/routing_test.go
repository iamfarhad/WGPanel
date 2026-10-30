package httpapi

import (
	"net/netip"
	"strings"
	"testing"

	"wgpanel-api/internal/store"
)

func rtStr(s string) *string { return &s }

func TestResolveRoutingForCreate(t *testing.T) {
	tmpl := &store.PlanTemplate{RoutingMode: rtStr("bypass_iran"), RoutingPrecision: rtStr("exact")}
	customTmpl := &store.PlanTemplate{RoutingMode: rtStr("custom"), RoutingExcludedCIDRs: []string{"203.0.113.0/24"}}
	cidrs := func(c ...string) *[]string { return &c }

	cases := []struct {
		name     string
		req      routingRequest
		tmpl     *store.PlanTemplate
		wantMode string // "" = nil
		wantCIDR string
		wantPrec string
		wantErr  string
	}{
		{name: "nothing, no template", req: routingRequest{}},
		{name: "template inherited", tmpl: tmpl, wantMode: "bypass_iran", wantPrec: "exact"},
		{name: "explicit overrides template", req: routingRequest{RoutingMode: rtStr("full")}, tmpl: tmpl, wantMode: "full", wantPrec: "exact"},
		{name: "explicit precision overrides", req: routingRequest{RoutingPrecision: rtStr("compact")}, tmpl: tmpl, wantMode: "bypass_iran", wantPrec: "compact"},
		{name: "custom with cidrs", req: routingRequest{RoutingMode: rtStr("custom"), RoutingExcludedCIDRs: cidrs("198.51.100.9/24", "198.51.100.0/24")}, wantMode: "custom", wantCIDR: "198.51.100.0/24"},
		{name: "custom template list comes along", tmpl: customTmpl, wantMode: "custom", wantCIDR: "203.0.113.0/24"},
		{name: "custom without cidrs", req: routingRequest{RoutingMode: rtStr("custom")}, wantErr: "needs routing_excluded_cidrs"},
		{name: "cidrs without custom", req: routingRequest{RoutingMode: rtStr("bypass_iran"), RoutingExcludedCIDRs: cidrs("203.0.113.0/24")}, wantErr: "only used with routing_mode custom"},
		{name: "template custom list is not reused for another mode", req: routingRequest{RoutingMode: rtStr("bypass_iran")}, tmpl: customTmpl, wantMode: "bypass_iran"},
		{name: "bad mode", req: routingRequest{RoutingMode: rtStr("split")}, wantErr: "routing_mode must be"},
		{name: "bad precision", req: routingRequest{RoutingPrecision: rtStr("fast")}, wantErr: "routing_precision must be"},
		{name: "bad cidr", req: routingRequest{RoutingMode: rtStr("custom"), RoutingExcludedCIDRs: cidrs("not-a-cidr")}, wantErr: "not a valid CIDR"},
		{name: "ipv6 cidr", req: routingRequest{RoutingMode: rtStr("custom"), RoutingExcludedCIDRs: cidrs("2001:db8::/32")}, wantErr: "only IPv4"},
		{name: "exclude everything", req: routingRequest{RoutingMode: rtStr("custom"), RoutingExcludedCIDRs: cidrs("0.0.0.0/0")}, wantErr: "0.0.0.0/0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, aerr := resolveRoutingForCreate(c.req, c.tmpl)
			if c.wantErr != "" {
				if aerr == nil || !strings.Contains(aerr.Message, c.wantErr) {
					t.Fatalf("error = %v, want containing %q", aerr, c.wantErr)
				}
				return
			}
			if aerr != nil {
				t.Fatalf("unexpected error: %v", aerr)
			}
			if rtDeref(got.Mode) != c.wantMode || rtDeref(got.Precision) != c.wantPrec {
				t.Fatalf("mode/precision = %q/%q, want %q/%q", rtDeref(got.Mode), rtDeref(got.Precision), c.wantMode, c.wantPrec)
			}
			if strings.Join(got.CIDRs, ",") != c.wantCIDR {
				t.Fatalf("cidrs = %v, want %q", got.CIDRs, c.wantCIDR)
			}
		})
	}
}

func rtDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestNormalizeExcludedCIDRsBounds(t *testing.T) {
	var many []string
	for i := 0; i < 501; i++ {
		many = append(many, netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 0}).String()+"/24")
	}
	if _, aerr := normalizeExcludedCIDRs(many); aerr == nil || !strings.Contains(aerr.Message, "at most") {
		t.Fatalf("501 entries must be refused, got %v", aerr)
	}
}

func TestBuildAllowedIPs(t *testing.T) {
	// Default account: today's exact line.
	got, err := buildAllowedIPs(store.AccountRouting{}, "10.66.0.0/24", "10.66.0.1")
	if err != nil || got != "0.0.0.0/0, ::/0" {
		t.Fatalf("default = %q, %v - must be byte-identical to the pre-routing config", got, err)
	}
	// bypass_iran: no 0.0.0.0/0, ::/0 last, node subnet (DNS) kept in.
	got, err = buildAllowedIPs(store.AccountRouting{Mode: rtStr("bypass_iran")}, "10.66.0.0/24", "10.66.0.1")
	if err != nil {
		t.Fatal(err)
	}
	entries := strings.Split(got, ", ")
	if strings.Contains(got, "0.0.0.0/0") || entries[len(entries)-1] != "::/0" {
		t.Fatalf("bypass_iran = %.120s ...", got)
	}
	if !rtCovered(entries, "10.66.0.1") {
		t.Fatal("the node's DNS address must be in the tunnel in bypass mode")
	}
	if rtCovered(entries, "192.168.0.10") {
		t.Fatal("LAN must bypass the tunnel")
	}
	// A literal client_dns inside an excluded range is forced into the tunnel too.
	got, _ = buildAllowedIPs(store.AccountRouting{Mode: rtStr("bypass_iran")}, "10.66.0.0/24", "192.168.77.77, 1.1.1.1")
	if !rtCovered(strings.Split(got, ", "), "192.168.77.77") {
		t.Fatal("a literal IPv4 resolver must stay in the tunnel")
	}
	// custom
	got, err = buildAllowedIPs(store.AccountRouting{Mode: rtStr("custom"), ExcludedCIDRs: []string{"203.0.113.0/24"}, Precision: rtStr("exact")}, "10.66.0.0/24", "node")
	if err != nil || rtCovered(strings.Split(got, ", "), "203.0.113.5") || !rtCovered(strings.Split(got, ", "), "203.0.114.5") {
		t.Fatalf("custom = %q, %v", got, err)
	}
	// A corrupted stored list is an error the caller degrades from, never a panic.
	if _, err := buildAllowedIPs(store.AccountRouting{Mode: rtStr("custom"), ExcludedCIDRs: []string{"garbage"}}, "10.66.0.0/24", ""); err == nil {
		t.Fatal("expected an error for a malformed stored CIDR")
	}
}

func rtCovered(entries []string, ip string) bool {
	a := netip.MustParseAddr(ip)
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

func TestForceIncludeFor(t *testing.T) {
	got := forceIncludeFor("10.66.0.0/24", "10.66.0.1, 1.1.1.1, example.org")
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	if strings.Join(s, " ") != "10.66.0.0/24 10.66.0.1/32 1.1.1.1/32" {
		t.Fatalf("forceIncludeFor = %v", s)
	}
}

func TestEffectiveRoutingMode(t *testing.T) {
	if effectiveRoutingMode(nil) != "full" || effectiveRoutingMode(rtStr("bypass_iran")) != "bypass_iran" || effectiveRoutingMode(rtStr("junk")) != "full" {
		t.Fatal("effectiveRoutingMode must default to full for nil/unknown")
	}
}
