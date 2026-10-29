package httpapi

import (
	"testing"

	"wgpanel-api/internal/store"
)

func TestNormalizePublicEndpoint(t *testing.T) {
	valid := map[string]string{
		"vpn1.example.com:51820":   "vpn1.example.com:51820",
		" 203.0.113.7:51820 ":      "203.0.113.7:51820",
		"[2001:db8::1]:51820":      "[2001:db8::1]:51820",
		"vpn1.example.com:0051820": "vpn1.example.com:51820",
	}
	for in, want := range valid {
		got, err := normalizePublicEndpoint(in)
		if err != nil {
			t.Errorf("normalizePublicEndpoint(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizePublicEndpoint(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"",
		"vpn1.example.com",           // no port: WireGuard apps refuse the config
		"203.0.113.7",                // no port
		"vpn1.example.com:0",         // port out of range
		"vpn1.example.com:70000",     // port out of range
		"vpn1.example.com:udp",       // non-numeric port
		"https://vpn1.example.com:1", // scheme
		"vpn1 example.com:51820",     // space in host
		":51820",                     // no host
	}
	for _, in := range invalid {
		if _, err := normalizePublicEndpoint(in); err == nil {
			t.Errorf("normalizePublicEndpoint(%q): expected an error", in)
		}
	}
}

func TestNormalizeWGSubnet(t *testing.T) {
	valid := map[string]string{
		"10.66.0.0/24":   "10.66.0.0/24",
		"10.66.0.5/24":   "10.66.0.0/24", // host bits stripped
		" 10.90.0.0/22 ": "10.90.0.0/22",
		"10.0.0.0/16":    "10.0.0.0/16",
		"10.0.0.0/30":    "10.0.0.0/30",
	}
	for in, want := range valid {
		got, err := normalizeWGSubnet(in)
		if err != nil {
			t.Errorf("normalizeWGSubnet(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeWGSubnet(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{"", "10.66.0.0", "10.66.0.0/33", "10.0.0.0/8", "10.0.0.0/31", "fd00::/64", "not-a-cidr"}
	for _, in := range invalid {
		if _, err := normalizeWGSubnet(in); err == nil {
			t.Errorf("normalizeWGSubnet(%q): expected an error", in)
		}
	}
}

func TestNodeNetworkFor(t *testing.T) {
	nn := nodeNetworkFor(store.Node{WGSubnet: "10.67.0.0/24", PublicEndpoint: "vpn.example.com:51821"})
	if nn.WGInterfaceAddress != "10.67.0.1/24" {
		t.Errorf("WGInterfaceAddress = %q, want 10.67.0.1/24", nn.WGInterfaceAddress)
	}
	if nn.WGListenPort != 51821 {
		t.Errorf("WGListenPort = %d, want 51821", nn.WGListenPort)
	}

	// A legacy endpoint without a port (accepted before validation existed) must not
	// break registration - the port is just unknown.
	legacy := nodeNetworkFor(store.Node{WGSubnet: "10.67.0.0/24", PublicEndpoint: "vpn.example.com"})
	if legacy.WGListenPort != 0 {
		t.Errorf("legacy WGListenPort = %d, want 0", legacy.WGListenPort)
	}
}

func TestResolveClientDNS(t *testing.T) {
	cases := []struct {
		setting   string
		forwarder bool
		want      string
	}{
		{"node", true, "10.67.0.1"},                                    // the node's own forwarder
		{"node", false, publicClientDNS},                               // older agent: no forwarder to point at
		{"9.9.9.9, 149.112.112.112", true, "9.9.9.9, 149.112.112.112"}, // explicit choice wins
	}
	for _, c := range cases {
		if got := resolveClientDNS(c.setting, "10.67.0.0/24", c.forwarder); got != c.want {
			t.Errorf("resolveClientDNS(%q, forwarder=%v) = %q, want %q", c.setting, c.forwarder, got, c.want)
		}
	}
}

func TestValidateClientDNS(t *testing.T) {
	for _, ok := range []string{"node", "1.1.1.1", "1.1.1.1, 1.0.0.1", "10.0.0.53, corp.example", "2606:4700:4700::1111"} {
		if err := validateClientDNS(ok); err != nil {
			t.Errorf("validateClientDNS(%q): unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", "corp.example", "1.1.1.1; 1.0.0.1", "https://dns.example", "Node"} {
		if err := validateClientDNS(bad); err == nil {
			t.Errorf("validateClientDNS(%q): expected an error", bad)
		}
	}
}
