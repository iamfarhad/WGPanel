// Package obfuscation holds everything the panel and the node agent share about
// AmneziaWG (AWG): the parameter set, validation against the real upstream limits,
// random generation, the built-in presets and the rendering of the config lines.
//
// Why a shared package: the control plane validates and renders client configs, the
// agent renders the server's interface config and diffs profiles to decide between a
// live update and an interface recreate. One definition keeps the key order, the
// "zero means unset" rule and the limits identical on both sides.
//
// Upstream references (pinned in deploy/node.Dockerfile): amneziawg-go v0.2.19
// (device/uapi.go, device/magic-header.go, device/obf*.go, device/receive.go) and
// amneziawg-tools v1.0.20260618 (src/config.c, src/wg-quick/linux.bash).
package obfuscation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Compat levels. awg1 sticks to the 1.x parameters (Jc/Jmin/Jmax, S1/S2, single-value
// H1-H4), which every AWG client app understands. awg2 adds S3/S4, H ranges and the
// I1-I5 custom signature packets, which need a current AmneziaVPN / AmneziaWG app.
const (
	CompatAWG1 = "awg1"
	CompatAWG2 = "awg2"
)

// Upstream-derived limits. Sizes are bounded by the 1280-byte minimum IPv6 MTU: a
// larger junk/signature packet fragments on the wire, which is itself a DPI tell
// (amneziawg-go README: "If Jmax >= system MTU ... looks suspicious").
const (
	MaxJc        = 128
	MaxJunkSize  = 1280
	MaxS1        = 1280 - 148 // a handshake initiation is 148 bytes plus S1 padding
	MaxS2        = 1280 - 92  // a handshake response is 92 bytes plus S2 padding
	MaxS3        = 64         // cookie reply padding; AmneziaVPN's own limit for AWG 2.0
	MaxS4        = 32         // added to EVERY transport packet, so it eats tunnel MTU
	MinHeader    = 5          // 1-4 are the stock WireGuard message types
	MaxHeader    = 1<<32 - 1
	MaxCPSPacket = 1280
	CPSSlots     = 5
)

// Params is one AWG parameter set. A zero value / empty string means "not set": the
// line is left out of the rendered config and AWG falls back to its stock behaviour
// (README: "If there is no value specified (for any param), AWG treats it as 0").
type Params struct {
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	S1   int `json:"s1"`
	S2   int `json:"s2"`
	S3   int `json:"s3"`
	S4   int `json:"s4"`
	// H1-H4 are "N" or, for awg2, "A-B" ranges.
	H1 string `json:"h1"`
	H2 string `json:"h2"`
	H3 string `json:"h3"`
	H4 string `json:"h4"`
	// I1-I5 are CPS strings: a sequence of <b 0x..>, <r N>, <rc N>, <rd N>, <t> tags.
	I1 string `json:"i1"`
	I2 string `json:"i2"`
	I3 string `json:"i3"`
	I4 string `json:"i4"`
	I5 string `json:"i5"`
}

// Headers returns H1-H4 in order.
func (p Params) Headers() [4]string { return [4]string{p.H1, p.H2, p.H3, p.H4} }

// Signatures returns I1-I5 in order.
func (p Params) Signatures() [5]string { return [5]string{p.I1, p.I2, p.I3, p.I4, p.I5} }

// JunkOnly reports a profile that only sets Jc/Jmin/Jmax: no padding, default headers,
// no signatures. See Presets for why such a server stays reachable by stock clients.
func (p Params) JunkOnly() bool {
	return p.Jc > 0 && p.S1 == 0 && p.S2 == 0 && p.S3 == 0 && p.S4 == 0 &&
		p.H1 == "" && p.H2 == "" && p.H3 == "" && p.H4 == "" && p.I1 == "" &&
		p.I2 == "" && p.I3 == "" && p.I4 == "" && p.I5 == ""
}

// ValidCompat reports whether c is a known compat level.
func ValidCompat(c string) bool { return c == CompatAWG1 || c == CompatAWG2 }

// Validate checks p against the constraints of compat. It returns the first problem as
// a human-readable error naming the field. The rules come from amneziawg-go v0.2.19:
//
//   - jc/jmin/jmax must be positive when set (uapi.go); Jmin <= Jmax (send.go draws
//     n in [jmin, jmax]; reversed bounds would panic rand.Int). With Jc = 0 junk is
//     off, so Jmin/Jmax must be 0 too.
//   - S1 and S2 are arbitrary non-negative paddings, but receive.go identifies a
//     message by size AND header; with stock headers an init (148+S1) and a response
//     (92+S2) of equal length are indistinguishable, hence S1+56 != S2. The same
//     argument gives S3 != S1+84 and S3 != S2+28 for the 64-byte cookie reply.
//   - H1-H4 must be pairwise distinct (awg2: non-overlapping ranges, uapi.go
//     mergeWithDevice) and above 4 so they can't be mistaken for stock message types.
//     Either all four are set or none (a half-set quartet would mix custom headers
//     with stock ones, which no client app can express).
//   - CPS strings must parse with the tag set amneziawg-go implements; a malformed
//     tag makes the interface refuse the whole config.
func (p Params) Validate(compat string) error {
	if !ValidCompat(compat) {
		return fmt.Errorf("compat must be %q or %q", CompatAWG1, CompatAWG2)
	}
	if p.Jc < 0 || p.Jc > MaxJc {
		return fmt.Errorf("jc must be between 0 and %d", MaxJc)
	}
	if p.Jc == 0 {
		if p.Jmin != 0 || p.Jmax != 0 {
			return errors.New("jmin and jmax must be 0 when jc is 0 (junk packets off)")
		}
	} else {
		if p.Jmin < 1 || p.Jmax < 1 {
			return errors.New("jmin and jmax must be positive when jc is set")
		}
		if p.Jmin > p.Jmax {
			return errors.New("jmin must not exceed jmax")
		}
		if p.Jmax > MaxJunkSize {
			return fmt.Errorf("jmax must be at most %d (larger packets fragment)", MaxJunkSize)
		}
	}
	if p.S1 < 0 || p.S1 > MaxS1 {
		return fmt.Errorf("s1 must be between 0 and %d", MaxS1)
	}
	if p.S2 < 0 || p.S2 > MaxS2 {
		return fmt.Errorf("s2 must be between 0 and %d", MaxS2)
	}
	if p.S1+56 == p.S2 {
		return errors.New("s1 + 56 must not equal s2 (init and response messages would have the same size)")
	}

	if compat == CompatAWG1 {
		if p.S3 != 0 || p.S4 != 0 {
			return errors.New("s3 and s4 need compat awg2")
		}
		for i, s := range p.Signatures() {
			if s != "" {
				return fmt.Errorf("i%d needs compat awg2", i+1)
			}
		}
	} else {
		if p.S3 < 0 || p.S3 > MaxS3 {
			return fmt.Errorf("s3 must be between 0 and %d", MaxS3)
		}
		if p.S4 < 0 || p.S4 > MaxS4 {
			return fmt.Errorf("s4 must be between 0 and %d", MaxS4)
		}
		if p.S3 == p.S1+84 || p.S3 == p.S2+28 {
			return errors.New("s3 would make the cookie reply the same size as an init or response message")
		}
	}

	if err := p.validateHeaders(compat); err != nil {
		return err
	}
	for i, s := range p.Signatures() {
		if s == "" {
			continue
		}
		if _, err := ParseCPS(s); err != nil {
			return fmt.Errorf("i%d: %w", i+1, err)
		}
	}
	return nil
}

type headerRange struct{ lo, hi uint64 }

func (p Params) validateHeaders(compat string) error {
	set := 0
	var ranges [4]headerRange
	for i, h := range p.Headers() {
		if h == "" {
			continue
		}
		set++
		r, err := parseHeader(h)
		if err != nil {
			return fmt.Errorf("h%d: %w", i+1, err)
		}
		if compat == CompatAWG1 && r.lo != r.hi {
			return fmt.Errorf("h%d: ranges need compat awg2 (use a single value)", i+1)
		}
		if r.lo < MinHeader {
			return fmt.Errorf("h%d: values must be at least %d (1-4 are stock WireGuard message types)", i+1, MinHeader)
		}
		ranges[i] = r
	}
	if set == 0 {
		return nil
	}
	if set != 4 {
		return errors.New("h1-h4 must either all be set or all be empty")
	}
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if ranges[i].lo <= ranges[j].hi && ranges[j].lo <= ranges[i].hi {
				return fmt.Errorf("h%d and h%d must not overlap", i+1, j+1)
			}
		}
	}
	return nil
}

func parseHeader(s string) (headerRange, error) {
	lo, hi, isRange := strings.Cut(s, "-")
	a, err := strconv.ParseUint(lo, 10, 32)
	if err != nil {
		return headerRange{}, fmt.Errorf("%q is not an unsigned 32-bit number or range", s)
	}
	b := a
	if isRange {
		if b, err = strconv.ParseUint(hi, 10, 32); err != nil {
			return headerRange{}, fmt.Errorf("%q is not an unsigned 32-bit number or range", s)
		}
	}
	if b < a {
		return headerRange{}, fmt.Errorf("range %q must be ascending", s)
	}
	return headerRange{a, b}, nil
}

// Normalize trims whitespace so the stored and the rendered form are canonical.
func (p Params) Normalize() Params {
	p.H1, p.H2, p.H3, p.H4 = strings.TrimSpace(p.H1), strings.TrimSpace(p.H2), strings.TrimSpace(p.H3), strings.TrimSpace(p.H4)
	p.I1, p.I2, p.I3, p.I4, p.I5 = strings.TrimSpace(p.I1), strings.TrimSpace(p.I2), strings.TrimSpace(p.I3), strings.TrimSpace(p.I4), strings.TrimSpace(p.I5)
	return p
}
