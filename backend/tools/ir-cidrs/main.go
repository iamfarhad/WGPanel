// Command ir-cidrs regenerates backend/internal/routing/ir_ipv4.txt, the embedded,
// aggregated list of Iranian IPv4 ranges behind the "bypass_iran" routing mode.
//
// Source: RIPE NCC's extended delegation file (every IR "ipv4" record that is
// allocated or assigned). It is the registry of record for the country's address
// space - no third-party GeoIP database, no licence strings. Run from backend/:
//
//	go run ./tools/ir-cidrs                       # download the current file
//	go run ./tools/ir-cidrs -in delegated.txt     # use a file you already have
//
// The output header records the source URL and the file's own "as of" date, so a
// diff of the generated file shows exactly which snapshot it came from. Re-run it
// every few months; Iranian allocations change slowly.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/routing"
)

const defaultSource = "https://ftp.ripe.net/pub/stats/ripencc/delegated-ripencc-extended-latest"

func main() {
	in := flag.String("in", "", "read this delegation file instead of downloading")
	src := flag.String("source", defaultSource, "URL to download when -in is not given")
	out := flag.String("out", "internal/routing/ir_ipv4.txt", "output path")
	flag.Parse()

	var r io.Reader
	sourceLabel := *src
	if *in != "" {
		f, err := os.Open(*in)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		r = f
		sourceLabel = *src + " (local copy)"
	} else {
		client := &http.Client{Timeout: 15 * time.Minute}
		resp, err := client.Get(*src)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("download %s: %s", *src, resp.Status)
		}
		r = resp.Body
	}

	prefixes, asOf, records, err := parseDelegation(r)
	if err != nil {
		log.Fatal(err)
	}
	agg := routing.Aggregate(prefixes)
	if len(agg) == 0 {
		log.Fatal("no IR ipv4 records found - refusing to write an empty list")
	}

	var total uint64
	for _, p := range agg {
		total += uint64(1) << (32 - p.Bits())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Iranian IPv4 ranges for the bypass_iran routing mode. GENERATED - do not edit.\n")
	fmt.Fprintf(&b, "# Source: %s\n", sourceLabel)
	fmt.Fprintf(&b, "# Registry file as-of date: %s\n", asOf)
	fmt.Fprintf(&b, "# Generated: %s by backend/tools/ir-cidrs\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "# Filter: cc=IR, type=ipv4, status in (allocated, assigned); %d records -> %d aggregated CIDRs, %d addresses\n", records, len(agg), total)
	for _, p := range agg {
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s: %d records -> %d CIDRs (%d addresses), registry as of %s\n", *out, records, len(agg), total, asOf)
}

// parseDelegation reads the RIR "extended" format: a version line
// (version|registry|serial|records|startdate|enddate|UTCoffset), summary lines, then
// registry|cc|type|start|value|date|status|... records. For ipv4, value is an address
// count.
func parseDelegation(r io.Reader) (prefixes []netip.Prefix, asOf string, records int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) >= 7 && f[0][0] >= '0' && f[0][0] <= '9' {
			// Version line: field 5 is the file's end date.
			asOf = f[5]
			if t, perr := time.Parse("20060102", f[5]); perr == nil {
				asOf = t.Format("2006-01-02")
			}
			continue
		}
		if len(f) < 7 || f[1] != "IR" || f[2] != "ipv4" {
			continue
		}
		if f[6] != "allocated" && f[6] != "assigned" {
			continue
		}
		start, perr := netip.ParseAddr(f[3])
		if perr != nil {
			return nil, "", 0, fmt.Errorf("bad start address in %q: %w", line, perr)
		}
		count, perr := strconv.ParseUint(f[4], 10, 64)
		if perr != nil {
			return nil, "", 0, fmt.Errorf("bad count in %q: %w", line, perr)
		}
		ps, perr := routing.RangePrefixes(start, count)
		if perr != nil {
			return nil, "", 0, perr
		}
		prefixes = append(prefixes, ps...)
		records++
	}
	return prefixes, asOf, records, sc.Err()
}
