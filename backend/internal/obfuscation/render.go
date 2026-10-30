package obfuscation

import (
	"fmt"
	"strings"
)

// Lines renders the set parameters as "Key = value" lines in the order awg-quick and
// the AWG apps document them. Unset parameters are omitted (see Params). The same
// lines go into a client's [Interface] and into the node's own [Interface].
func (p Params) Lines() []string {
	var out []string
	num := func(k string, v int) {
		if v != 0 {
			out = append(out, fmt.Sprintf("%s = %d", k, v))
		}
	}
	str := func(k, v string) {
		if v != "" {
			out = append(out, k+" = "+v)
		}
	}
	num("Jc", p.Jc)
	num("Jmin", p.Jmin)
	num("Jmax", p.Jmax)
	num("S1", p.S1)
	num("S2", p.S2)
	num("S3", p.S3)
	num("S4", p.S4)
	for i, h := range p.Headers() {
		str(fmt.Sprintf("H%d", i+1), h)
	}
	for i, s := range p.Signatures() {
		str(fmt.Sprintf("I%d", i+1), s)
	}
	return out
}

// InterfaceBlock is Lines joined for splicing into a [Interface] section ("" if none).
func (p Params) InterfaceBlock() string {
	lines := p.Lines()
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// Change is what the agent must do to move a running interface from one parameter set
// to another.
type Change int

const (
	// ChangeNone: nothing to do.
	ChangeNone Change = iota
	// ChangeLive: `awg set` can apply it to the running interface, peers and sessions
	// stay up on the node side.
	ChangeLive
	// ChangeRecreate: the interface must be torn down and brought up again.
	ChangeRecreate
)

// Plan decides between a live update and a recreate when going from old to new (nil =
// plain WireGuard, no AWG). Live is only possible when every parameter that is set in
// old is still set in new: amneziawg-go v0.2.19's UAPI can overwrite a value but can
// neither unset a signature packet nor accept jc/jmin/jmax <= 0 (device/uapi.go), and
// `awg set` only sends keys it is given, so clearing a value would silently keep the
// old one running. Switching AWG on or off always recreates (different userspace
// process vs kernel module).
func Plan(old, new *Params) Change {
	switch {
	case old == nil && new == nil:
		return ChangeNone
	case old == nil || new == nil:
		return ChangeRecreate
	case *old == *new:
		return ChangeNone
	}
	o, n := old.Lines(), new.Lines()
	have := map[string]bool{}
	for _, l := range n {
		have[strings.SplitN(l, " = ", 2)[0]] = true
	}
	for _, l := range o {
		if !have[strings.SplitN(l, " = ", 2)[0]] {
			return ChangeRecreate
		}
	}
	return ChangeLive
}

// SetArgs renders p as `awg set <iface>` arguments (lower-case key, value pairs).
func (p Params) SetArgs() []string {
	var args []string
	for _, l := range p.Lines() {
		k, v, _ := strings.Cut(l, " = ")
		args = append(args, strings.ToLower(k), v)
	}
	return args
}
