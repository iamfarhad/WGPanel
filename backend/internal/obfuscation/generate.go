package obfuscation

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Preset ids. They are templates, not rows: creating a profile from one generates fresh
// random values (so two profiles from the same preset never share H values or
// padding sizes - shared constants are exactly what a censor would fingerprint).
const (
	PresetCompatible = "compatible"
	PresetAWG1       = "awg1"
	PresetAWG2QUIC   = "awg2-quic"
	PresetAWG2DNS    = "awg2-dns"
)

// Preset describes a built-in starting point.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Compat      string `json:"compat"`
	// StockCompatible: a node running this profile still accepts a plain WireGuard
	// client. Verified against amneziawg-go v0.2.19: junk packets are sent only by the
	// side that initiates a handshake (send.go SendHandshakeInitiation), and with S1=S2=0
	// and stock headers receive.go classifies packets exactly like stock WireGuard, so
	// the server neither needs junk from its peers nor changes anything they see.
	StockCompatible bool `json:"stock_client_compatible"`
}

// Presets lists the built-ins in display order.
func Presets() []Preset {
	return []Preset{
		{PresetCompatible, "Compatible (junk only)",
			"Random junk packets before each handshake only. Stock WireGuard clients keep working; AWG apps add the junk.",
			CompatAWG1, true},
		{PresetAWG1, "AWG 1.x (full)",
			"Junk, padding and custom message headers. Every AWG app supports it; stock WireGuard clients cannot connect.",
			CompatAWG1, false},
		{PresetAWG2QUIC, "AWG 2.0 (QUIC-like)",
			"AWG 1.x plus transport padding, header ranges and a QUIC Initial lookalike signature packet. Needs a current AWG app.",
			CompatAWG2, false},
		{PresetAWG2DNS, "AWG 2.0 (DNS-like)",
			"AWG 1.x plus transport padding, header ranges and a DNS query lookalike signature packet. Needs a current AWG app.",
			CompatAWG2, false},
	}
}

// PresetByID returns the preset with that id.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// FromPreset generates a fresh parameter set for a preset.
func FromPreset(id string) (Params, string, error) {
	pr, ok := PresetByID(id)
	if !ok {
		return Params{}, "", fmt.Errorf("unknown preset %q", id)
	}
	var p Params
	switch id {
	case PresetCompatible:
		p = randomJunk(Params{})
	case PresetAWG1:
		p = randomAWG1()
	case PresetAWG2QUIC:
		p = randomAWG2()
		p.I1 = RandomQUICSignature()
	case PresetAWG2DNS:
		p = randomAWG2()
		p.I1 = RandomDNSSignature()
	}
	return p, pr.Compat, nil
}

// CPSMode selects what Rotate does with the I1-I5 signatures.
const (
	CPSKeep = "keep"
	CPSQUIC = "quic"
	CPSDNS  = "dns"
	CPSNone = "none"
)

// Rotate returns fresh random parameters for a profile, keeping its compat level and
// its shape: a junk-only profile stays junk-only (so it stays stock-compatible), and
// for awg2 the I1-I5 signatures are kept, replaced by a new QUIC/DNS-like I1, or
// cleared according to cps. The result always passes Validate(compat).
func Rotate(compat string, cur Params, cps string) (Params, error) {
	var p Params
	switch {
	case compat == CompatAWG1 && cur.JunkOnly():
		p = randomJunk(Params{})
	case compat == CompatAWG1:
		p = randomAWG1()
	case compat == CompatAWG2:
		p = randomAWG2()
		switch cps {
		case CPSQUIC:
			p.I1 = RandomQUICSignature()
		case CPSDNS:
			p.I1 = RandomDNSSignature()
		case CPSNone:
		case CPSKeep, "":
			p.I1, p.I2, p.I3, p.I4, p.I5 = cur.I1, cur.I2, cur.I3, cur.I4, cur.I5
		default:
			return Params{}, fmt.Errorf("cps must be keep, quic, dns or none")
		}
	default:
		return Params{}, fmt.Errorf("unknown compat %q", compat)
	}
	if err := p.Validate(compat); err != nil {
		// Unreachable by construction; fail loudly rather than push junk to nodes.
		return Params{}, fmt.Errorf("generated parameters invalid: %w", err)
	}
	return p, nil
}

// randInt returns a uniform integer in [lo, hi].
func randInt(lo, hi int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return lo + int(n.Int64())
}

// randomJunk sets Jc/Jmin/Jmax: Jc in the upstream-recommended 4-12, sizes well under
// the MTU so nothing fragments.
func randomJunk(p Params) Params {
	p.Jc = randInt(4, 12)
	p.Jmin = randInt(40, 120)
	p.Jmax = randInt(p.Jmin+40, 900)
	return p
}

// randomPaddings picks S1/S2 in 15-150 (small enough to stay far below the MTU, big
// enough to move the handshake sizes away from the stock 148/92) avoiding S1+56 == S2.
func randomPaddings(p Params) Params {
	for {
		p.S1, p.S2 = randInt(15, 150), randInt(15, 150)
		if p.S1+56 != p.S2 {
			return p
		}
	}
}

func randomAWG1() Params {
	p := randomPaddings(randomJunk(Params{}))
	// Four distinct single values in [5, 2^31); the 2^31 cap keeps them inside the signed
	// 32-bit range some client apps still parse headers with.
	seen := map[int]bool{}
	var h [4]int
	for i := range h {
		for {
			v := randInt(MinHeader, 1<<31-1)
			if !seen[v] {
				seen[v] = true
				h[i] = v
				break
			}
		}
	}
	p.H1, p.H2, p.H3, p.H4 = itoa(h[0]), itoa(h[1]), itoa(h[2]), itoa(h[3])
	return p
}

func randomAWG2() Params {
	p := randomPaddings(randomJunk(Params{}))
	for {
		p.S3, p.S4 = randInt(8, 48), randInt(4, 24)
		if p.S3 != p.S1+84 && p.S3 != p.S2+28 {
			break
		}
	}
	// Split [5, 2^32) into four equal buckets and draw one range inside each, so the
	// ranges can never overlap; then shuffle which message type gets which bucket so
	// the ordering H1<H2<H3<H4 isn't a tell either.
	const space = uint64(MaxHeader) - MinHeader + 1
	const bucket = space / 4
	var ranges [4]string
	for i := range ranges {
		base := uint64(MinHeader) + uint64(i)*bucket
		width := uint64(randInt(1<<16, 1<<26))
		start := base + uint64(randInt(0, int(bucket/2)-1))
		ranges[i] = fmt.Sprintf("%d-%d", start, start+width)
	}
	for i := len(ranges) - 1; i > 0; i-- {
		j := randInt(0, i)
		ranges[i], ranges[j] = ranges[j], ranges[i]
	}
	p.H1, p.H2, p.H3, p.H4 = ranges[0], ranges[1], ranges[2], ranges[3]
	return p
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// RandomQUICSignature builds an I1 resembling a QUIC v1 Initial: long-header flags,
// version 1, an 8-byte random connection id, empty SCID and token, a 2-byte length
// varint and a random payload. It cannot be a valid Initial (a DPI that decrypts the
// CRYPTO frame will see noise); it only has to survive first-byte/length heuristics.
// Total size stays within 1100-1200 bytes like a real client's padded Initial.
func RandomQUICSignature() string {
	payload := randInt(1000, 1150)
	var first [1]byte
	_, _ = rand.Read(first[:])
	flags := 0xc0 | first[0]&0x0f // long header, fixed bit, random type/reserved/pn-length bits
	lenVarint := 0x4000 | uint16(payload+4)
	var lv [2]byte
	binary.BigEndian.PutUint16(lv[:], lenVarint)
	return fmt.Sprintf("<b 0x%02x00000001><b 0x08><r 8><b 0x0000><b 0x%s><r %d>", flags, hex.EncodeToString(lv[:]), payload)
}

var dnsNames = []string{
	"wikipedia.org", "cloudflare.com", "microsoft.com", "apple.com", "mozilla.org",
	"ubuntu.com", "debian.org", "github.com", "openstreetmap.org", "archive.org",
}

// RandomDNSSignature builds an I1 resembling a plain DNS A query: random transaction
// id, recursion-desired header, one question for a common domain, type A, class IN.
func RandomDNSSignature() string {
	name := dnsNames[randInt(0, len(dnsNames)-1)]
	var q strings.Builder
	for _, label := range strings.Split(name, ".") {
		q.WriteString(fmt.Sprintf("%02x%s", len(label), hex.EncodeToString([]byte(label))))
	}
	q.WriteString("00")
	// 0100 0001 0000 0000 0000: flags (RD), QDCOUNT 1, no answer/authority/additional.
	return fmt.Sprintf("<r 2><b 0x01000001000000000000%s><b 0x00010001>", q.String())
}
