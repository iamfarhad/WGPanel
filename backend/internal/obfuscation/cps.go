package obfuscation

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// CPSTag is one parsed tag of a custom signature packet.
type CPSTag struct {
	Name string // b, r, rc, rd, t
	Size int    // bytes this tag contributes to the packet
}

// ParseCPS parses a custom-signature-packet string with the grammar amneziawg-go
// v0.2.19 implements (device/obf.go: tags b, t, r, rc, rd; d/ds/dz exist upstream
// for data-carrying packets and are not offered, since I1-I5 carry no data):
//
//	<b 0xHEX>   static bytes, even number of hex digits
//	<r N>       N random bytes
//	<rc N>      N random letters [a-zA-Z]
//	<rd N>      N random digits
//	<t>         4-byte unix timestamp
//
// Upstream silently ignores text outside tags and panics (slice bounds) on a negative
// <r N>; this parser is stricter on purpose - the panel must never push a string that
// makes the node's interface misbehave. Whitespace between tags is allowed.
// It returns the tags and fails if the packet would exceed MaxCPSPacket bytes.
func ParseCPS(s string) ([]CPSTag, error) {
	var tags []CPSTag
	rest := strings.TrimSpace(s)
	if rest == "" {
		return nil, fmt.Errorf("signature is empty")
	}
	total := 0
	for rest != "" {
		if rest[0] != '<' {
			return nil, fmt.Errorf("unexpected text %q outside a tag", clip(rest))
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return nil, fmt.Errorf("missing closing > in %q", clip(rest))
		}
		fields := strings.Fields(rest[1:end])
		rest = strings.TrimSpace(rest[end+1:])
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty tag <>")
		}
		tag := CPSTag{Name: fields[0]}
		switch tag.Name {
		case "t":
			if len(fields) != 1 {
				return nil, fmt.Errorf("<t> takes no argument")
			}
			tag.Size = 4
		case "b":
			if len(fields) != 2 || !strings.HasPrefix(fields[1], "0x") {
				return nil, fmt.Errorf("<b> needs one argument of the form 0xHEX")
			}
			digits := fields[1][2:]
			if digits == "" || len(digits)%2 != 0 {
				return nil, fmt.Errorf("<b %s>: hex must have an even, non-zero number of digits", fields[1])
			}
			if _, err := hex.DecodeString(digits); err != nil {
				return nil, fmt.Errorf("<b %s>: not valid hex", fields[1])
			}
			tag.Size = len(digits) / 2
		case "r", "rc", "rd":
			if len(fields) != 2 {
				return nil, fmt.Errorf("<%s> needs one size argument", tag.Name)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1 || n > MaxCPSPacket {
				return nil, fmt.Errorf("<%s %s>: size must be a number between 1 and %d", tag.Name, fields[1], MaxCPSPacket)
			}
			tag.Size = n
		default:
			return nil, fmt.Errorf("unknown tag <%s>", tag.Name)
		}
		total += tag.Size
		tags = append(tags, tag)
	}
	if total > MaxCPSPacket {
		return nil, fmt.Errorf("packet is %d bytes, at most %d (larger packets fragment)", total, MaxCPSPacket)
	}
	return tags, nil
}

func clip(s string) string {
	if len(s) > 24 {
		return s[:24] + "..."
	}
	return s
}
