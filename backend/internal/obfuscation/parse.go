package obfuscation

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseLines is the inverse of Lines: it reads "Key = value" lines (blank lines and
// '#' comments are skipped, keys are case-insensitive, unknown keys are an error).
// The agent persists the applied profile this way so that it, and the container
// entrypoint, can rebuild the interface config after a restart.
func ParseLines(text string) (Params, error) {
	var p Params
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Params{}, fmt.Errorf("malformed line %q", line)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		num := func(dst *int) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: %q is not a number", k, v)
			}
			*dst = n
			return nil
		}
		var err error
		switch k {
		case "jc":
			err = num(&p.Jc)
		case "jmin":
			err = num(&p.Jmin)
		case "jmax":
			err = num(&p.Jmax)
		case "s1":
			err = num(&p.S1)
		case "s2":
			err = num(&p.S2)
		case "s3":
			err = num(&p.S3)
		case "s4":
			err = num(&p.S4)
		case "h1":
			p.H1 = v
		case "h2":
			p.H2 = v
		case "h3":
			p.H3 = v
		case "h4":
			p.H4 = v
		case "i1":
			p.I1 = v
		case "i2":
			p.I2 = v
		case "i3":
			p.I3 = v
		case "i4":
			p.I4 = v
		case "i5":
			p.I5 = v
		default:
			err = fmt.Errorf("unknown key %q", k)
		}
		if err != nil {
			return Params{}, err
		}
	}
	return p, nil
}
