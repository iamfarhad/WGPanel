package obfuscation

import (
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	good := Params{Jc: 5, Jmin: 50, Jmax: 200, S1: 30, S2: 40, H1: "100", H2: "200", H3: "300", H4: "400"}
	if err := good.Validate(CompatAWG1); err != nil {
		t.Fatalf("good awg1: %v", err)
	}
	cases := []struct {
		name   string
		compat string
		mut    func(p *Params)
		want   string
	}{
		{"jc too big", CompatAWG1, func(p *Params) { p.Jc = 129 }, "jc"},
		{"jmin>jmax", CompatAWG1, func(p *Params) { p.Jmin, p.Jmax = 300, 200 }, "jmin"},
		{"jmax over mtu", CompatAWG1, func(p *Params) { p.Jmax = 2000 }, "jmax"},
		{"jc zero with sizes", CompatAWG1, func(p *Params) { p.Jc = 0 }, "jc is 0"},
		{"s1+56==s2", CompatAWG1, func(p *Params) { p.S1, p.S2 = 30, 86 }, "s1 + 56"},
		{"s1 too big", CompatAWG1, func(p *Params) { p.S1 = 1200 }, "s1"},
		{"s3 on awg1", CompatAWG1, func(p *Params) { p.S3 = 5 }, "awg2"},
		{"I on awg1", CompatAWG1, func(p *Params) { p.I1 = "<t>" }, "awg2"},
		{"range on awg1", CompatAWG1, func(p *Params) { p.H1 = "100-150" }, "ranges"},
		{"h small", CompatAWG1, func(p *Params) { p.H1 = "4" }, "at least 5"},
		{"h dup", CompatAWG1, func(p *Params) { p.H2 = "100" }, "overlap"},
		{"h partial", CompatAWG1, func(p *Params) { p.H4 = "" }, "all be set"},
		{"h not number", CompatAWG1, func(p *Params) { p.H1 = "abc" }, "h1"},
		{"h too big", CompatAWG1, func(p *Params) { p.H1 = "4294967296" }, "h1"},
		{"h reversed range", CompatAWG2, func(p *Params) { p.H1 = "200-100" }, "ascending"},
		{"h overlapping ranges", CompatAWG2, func(p *Params) { p.H1, p.H2 = "100-250", "200-300" }, "overlap"},
		{"s4 too big", CompatAWG2, func(p *Params) { p.S4 = 33 }, "s4"},
		{"s3 collides", CompatAWG2, func(p *Params) { p.S2, p.S3 = 20, 48 }, "cookie"},
		{"bad cps", CompatAWG2, func(p *Params) { p.I1 = "<x 1>" }, "i1"},
		{"bad compat", "awg3", func(p *Params) {}, "compat"},
	}
	for _, tc := range cases {
		p := good
		tc.mut(&p)
		err := p.Validate(tc.compat)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	// awg2 accepts ranges, S3/S4 and signatures.
	p2 := Params{Jc: 4, Jmin: 40, Jmax: 90, S1: 20, S2: 30, S3: 10, S4: 8,
		H1: "100-200", H2: "300-400", H3: "500-600", H4: "700-800", I1: "<b 0xc0><r 20><t>"}
	if err := p2.Validate(CompatAWG2); err != nil {
		t.Fatalf("good awg2: %v", err)
	}
	// Junk off and nothing else is a valid (if pointless) profile.
	if err := (Params{}).Validate(CompatAWG1); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

func TestParseCPS(t *testing.T) {
	ok := map[string]int{
		"<t>":                4,
		"<b 0xdeadbeef>":     4,
		"<r 10><rc 5><rd 3>": 18,
		"<b 0xC0> <r 1> <t>": 6,
		"<b 0x00><b 0x0000>": 3,
		"<r 1280>":           1280,
	}
	for in, size := range ok {
		tags, err := ParseCPS(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		got := 0
		for _, tag := range tags {
			got += tag.Size
		}
		if got != size {
			t.Errorf("%q: size %d, want %d", in, got, size)
		}
	}
	bad := []string{
		"", "   ", "hello", "<t> trailing", "<t", "<>", "<b>", "<b 0x>", "<b 0xabc>", "<b 0xzz>", "<b ab>",
		"<r>", "<r 0>", "<r -5>", "<r abc>", "<r 1281>", "<r 700><r 700>", "<t 5>", "<x 1>", "<d 1>", "<r 1 2>",
	}
	for _, in := range bad {
		if _, err := ParseCPS(in); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestGeneratedParamsAlwaysValid(t *testing.T) {
	for i := 0; i < 500; i++ {
		for _, id := range []string{PresetCompatible, PresetAWG1, PresetAWG2QUIC, PresetAWG2DNS} {
			p, compat, err := FromPreset(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Validate(compat); err != nil {
				t.Fatalf("preset %s iteration %d: %v (%+v)", id, i, err, p)
			}
			rot, err := Rotate(compat, p, CPSKeep)
			if err != nil {
				t.Fatalf("rotate %s: %v", id, err)
			}
			if id == PresetCompatible && !rot.JunkOnly() {
				t.Fatalf("rotating the compatible preset must stay junk-only: %+v", rot)
			}
			if id == PresetAWG2QUIC && rot.I1 != p.I1 {
				t.Fatalf("cps keep must keep I1")
			}
		}
		for _, mode := range []string{CPSQUIC, CPSDNS, CPSNone} {
			p, err := Rotate(CompatAWG2, Params{}, mode)
			if err != nil {
				t.Fatalf("rotate awg2 %s: %v", mode, err)
			}
			if (mode == CPSNone) != (p.I1 == "") {
				t.Fatalf("mode %s gave I1=%q", mode, p.I1)
			}
		}
	}
	if _, err := Rotate(CompatAWG2, Params{}, "bogus"); err == nil {
		t.Fatal("unknown cps mode must fail")
	}
}

func TestRotateChangesValuesAndKeepsCompat(t *testing.T) {
	p, _, _ := FromPreset(PresetAWG1)
	r, err := Rotate(CompatAWG1, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.H1 == p.H1 && r.H2 == p.H2 && r.H3 == p.H3 {
		t.Fatalf("rotation must change the headers: %+v -> %+v", p, r)
	}
	if r.S3 != 0 || r.S4 != 0 || r.I1 != "" {
		t.Fatalf("awg1 rotation produced awg2 fields: %+v", r)
	}
}

func TestLinesAndSetArgs(t *testing.T) {
	p := Params{Jc: 5, Jmin: 50, Jmax: 200, S1: 30, H1: "100-200", I1: "<t>"}
	got := strings.Join(p.Lines(), "|")
	want := "Jc = 5|Jmin = 50|Jmax = 200|S1 = 30|H1 = 100-200|I1 = <t>"
	if got != want {
		t.Fatalf("lines %q, want %q", got, want)
	}
	if (Params{}).InterfaceBlock() != "" {
		t.Fatal("empty params must render nothing")
	}
	args := strings.Join(p.SetArgs(), " ")
	if args != "jc 5 jmin 50 jmax 200 s1 30 h1 100-200 i1 <t>" {
		t.Fatalf("set args %q", args)
	}
}

func TestParseLinesRoundTrip(t *testing.T) {
	for i := 0; i < 50; i++ {
		p, _, _ := FromPreset(PresetAWG2QUIC)
		back, err := ParseLines("# wgpanel-profile: x 3\n\n" + p.InterfaceBlock())
		if err != nil || back != p {
			t.Fatalf("round trip failed: %v\n%+v\n%+v", err, p, back)
		}
	}
	for _, bad := range []string{"Jc = x", "nokey", "Foo = 1"} {
		if _, err := ParseLines(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestPlan(t *testing.T) {
	a := Params{Jc: 5, Jmin: 50, Jmax: 200, S1: 30, S2: 40, H1: "10", H2: "20", H3: "30", H4: "40"}
	b := a
	b.Jc, b.H1 = 7, "11"
	cleared := a
	cleared.S1 = 0
	withI := a
	withI.I1 = "<t>"

	cases := []struct {
		name     string
		old, new *Params
		want     Change
	}{
		{"both off", nil, nil, ChangeNone},
		{"enable", nil, &a, ChangeRecreate},
		{"disable", &a, nil, ChangeRecreate},
		{"same", &a, &a, ChangeNone},
		{"overwrite values", &a, &b, ChangeLive},
		{"add a signature", &a, &withI, ChangeLive},
		{"clear a value", &a, &cleared, ChangeRecreate},
		{"drop a signature", &withI, &a, ChangeRecreate},
	}
	for _, tc := range cases {
		if got := Plan(tc.old, tc.new); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPresetsMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if seen[p.ID] {
			t.Fatalf("duplicate preset %s", p.ID)
		}
		seen[p.ID] = true
		gen, compat, err := FromPreset(p.ID)
		if err != nil || compat != p.Compat {
			t.Fatalf("preset %s: compat %q err %v", p.ID, compat, err)
		}
		if p.StockCompatible != gen.JunkOnly() {
			t.Fatalf("preset %s: stock_client_compatible must equal junk-only", p.ID)
		}
	}
	if _, _, err := FromPreset("nope"); err == nil {
		t.Fatal("unknown preset must fail")
	}
}
