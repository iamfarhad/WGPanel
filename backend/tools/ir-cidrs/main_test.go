package main

import (
	"strings"
	"testing"
)

const sample = `2|ripencc|1793314799|6|19700101|20261029|+0100
ripencc|*|ipv4|*|5|summary
ripencc|IR|ipv4|2.144.0.0|262144|20110511|allocated|abc
ripencc|IR|ipv4|5.160.0.0|768|20120101|assigned|def
ripencc|IR|ipv4|5.160.3.0|256|20120101|allocated|ghi
ripencc|IR|ipv6|2001:db8::|32|20120101|allocated|jkl
ripencc|DE|ipv4|3.0.0.0|256|20120101|allocated|mno
ripencc|IR|ipv4|9.9.9.0|256|20120101|reserved|pqr
# comment
`

func TestParseDelegation(t *testing.T) {
	ps, asOf, records, err := parseDelegation(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if asOf != "2026-10-29" {
		t.Fatalf("asOf = %q", asOf)
	}
	// Only IR ipv4 allocated/assigned records: not IPv6, not DE, not "reserved".
	if records != 3 {
		t.Fatalf("records = %d, want 3", records)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.String())
	}
	if strings.Join(got, " ") != "2.144.0.0/14 5.160.0.0/23 5.160.2.0/24 5.160.3.0/24" {
		t.Fatalf("prefixes = %v", got)
	}
}
