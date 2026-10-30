package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestShapedPeersFromPicksUpV6(t *testing.T) {
	peers := []heartbeatPeer{
		{PublicKey: "a", AllowedIPs: []string{"10.0.0.2/32", "fd12:3456:789a:1::2/128"}, BandwidthLimitMbps: iptr(20)},
		{PublicKey: "b", AllowedIPs: []string{"10.0.0.5/32"}, BandwidthLimitMbps: iptr(50)},
	}
	got := shapedPeersFrom(peers)
	want := []shapedPeer{
		{ip: "10.0.0.2", ip6: "fd12:3456:789a:1::2", mbps: 20},
		{ip: "10.0.0.5", mbps: 50},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shapedPeersFrom = %+v, want %+v", got, want)
	}
}

func joinPlan(cmds [][]string) string {
	lines := make([]string, len(cmds))
	for i, c := range cmds {
		lines[i] = strings.Join(c, " ")
	}
	return strings.Join(lines, "\n")
}

func TestBuildShapingCommandsV6IFB(t *testing.T) {
	_, setup := buildShapingCommands("wg0", "ifb-wg0", []shapedPeer{
		{ip: "10.0.0.2", ip6: "fd12:3456:789a:1::2", mbps: 20},
	}, true)
	plan := joinPlan(setup)
	for _, want := range []string{
		// Download: shape on wg0 egress by v6 destination.
		"tc filter add dev wg0 parent 1: protocol ipv6 prio 2 u32 match ip6 dst fd12:3456:789a:1::2/128 flowid 1:a",
		// Upload: v6 must be mirrored onto the ifb too (a v4-only mirror filter would let
		// IPv6 uploads skip the limit), then matched by v6 source there.
		"tc filter add dev wg0 parent ffff: protocol ipv6 prio 2 u32 match u32 0 0 action mirred egress redirect dev ifb-wg0",
		"tc filter add dev ifb-wg0 parent 1: protocol ipv6 prio 2 u32 match ip6 src fd12:3456:789a:1::2/128 flowid 1:a",
		// The v4 side is unchanged.
		"match ip dst 10.0.0.2/32 flowid 1:a",
		"match ip src 10.0.0.2/32 flowid 1:a",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("missing %q in plan:\n%s", want, plan)
		}
	}
}

func TestBuildShapingCommandsV6Policing(t *testing.T) {
	_, setup := buildShapingCommands("wg0", "ifb-wg0", []shapedPeer{
		{ip: "10.0.0.2", ip6: "fd12:3456:789a:1::2", mbps: 20},
	}, false)
	plan := joinPlan(setup)
	if !strings.Contains(plan, "protocol ipv6 prio 2 u32 match ip6 src fd12:3456:789a:1::2/128 police rate 20mbit") {
		t.Fatalf("v6 upload must be policed in the fallback mode:\n%s", plan)
	}
}

func TestBuildShapingCommandsNoV6WhenPeersHaveNone(t *testing.T) {
	_, setup := buildShapingCommands("wg0", "ifb-wg0", []shapedPeer{{ip: "10.0.0.2", mbps: 20}}, true)
	if strings.Contains(joinPlan(setup), "ipv6") {
		t.Fatalf("a v4-only peer set must not install any IPv6 filter:\n%s", joinPlan(setup))
	}
}
