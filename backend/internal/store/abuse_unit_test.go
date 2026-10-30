package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeTagsRules(t *testing.T) {
	got, err := NormalizeTags([]string{" VIP ", "vip", "Trial   User", "", "vip2", "ویژه"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"trial-user", "vip", "vip2", "ویژه"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if empty, err := NormalizeTags(nil); err != nil || len(empty) != 0 {
		t.Fatalf("nil input: %v %v", empty, err)
	}

	for name, in := range map[string][]string{
		"bad char":  {"vip!"},
		"comma":     {"a,b"},
		"too long":  {strings.Repeat("x", MaxTagLength+1)},
		"too many":  manyTags(MaxTagsPerAcct + 1),
		"semicolon": {"a;b"},
	} {
		if _, err := NormalizeTags(in); !errors.Is(err, ErrInvalidTag) {
			t.Errorf("%s: err = %v, want ErrInvalidTag", name, err)
		}
	}
	if _, err := NormalizeTags(manyTags(MaxTagsPerAcct)); err != nil {
		t.Errorf("exactly the cap should be fine: %v", err)
	}
	// Duplicates do not count toward the cap.
	if out, err := NormalizeTags(append(manyTags(MaxTagsPerAcct), strings.ToUpper(manyTags(1)[0]), manyTags(2)[1])); err != nil || len(out) != MaxTagsPerAcct {
		t.Errorf("dupes: %d tags, err %v", len(out), err)
	}
}

func manyTags(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "t" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return out
}

func TestNetworkOfEndpoint(t *testing.T) {
	cases := []struct{ in, network, ip string }{
		{"203.0.113.77:51820", "203.0.113.0/24", "203.0.113.77"},
		{"203.0.113.200:1", "203.0.113.0/24", "203.0.113.200"},
		{"[2001:db8:1234:5678::9]:443", "2001:db8:1234::/48", "2001:db8:1234:5678::9"},
		{"[::ffff:198.51.100.9]:5000", "198.51.100.0/24", "198.51.100.9"},
		{"198.51.100.9", "198.51.100.0/24", "198.51.100.9"}, // bare address
	}
	for _, c := range cases {
		n, ip, ok := NetworkOfEndpoint(c.in)
		if !ok || n != c.network || ip != c.ip {
			t.Errorf("%s -> %q %q %v, want %q %q", c.in, n, ip, ok, c.network, c.ip)
		}
	}
	if _, _, ok := NetworkOfEndpoint("not-an-address:1"); ok {
		t.Error("garbage should not parse")
	}
}

func TestGroupSharingNetworks(t *testing.T) {
	now := time.Now()
	nets := GroupSharingNetworks([]SharingDevice{
		{"203.0.113.5:1000", "fra", now.Add(-4 * time.Minute)},
		{"203.0.113.5:2000", "fra", now.Add(-3 * time.Minute)},  // same IP, another port
		{"203.0.113.99:3000", "ams", now.Add(-2 * time.Minute)}, // same /24, another IP
		{"198.51.100.1:1", "fra", now.Add(-1 * time.Minute)},
		{"[2001:db8:aa::1]:9", "fra", now},
		{"bogus", "fra", now},
	})
	if len(nets) != 3 {
		t.Fatalf("want 3 networks (one /24 shared by two IPs), got %+v", nets)
	}
	if nets[0].Network != "2001:db8:aa::/48" || nets[1].Network != "198.51.100.0/24" || nets[2].Network != "203.0.113.0/24" {
		t.Fatalf("not newest-first: %+v", nets)
	}
	last := nets[2]
	if !reflect.DeepEqual(last.IPs, []string{"203.0.113.5", "203.0.113.99"}) || !reflect.DeepEqual(last.Nodes, []string{"ams", "fra"}) {
		t.Fatalf("detail wrong: %+v", last)
	}
}
