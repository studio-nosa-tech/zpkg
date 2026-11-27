// Package depspec parses dependency atoms used by recipes.
//
// Supported forms:
//
//	foo                    any version
//	foo@14                 any version in slot 14
//	>=foo-1.2              version constraint (also >, <=, <, =, !=)
//	>=foo@14-1.2           constraint on one slot
//	ssl?openssl            optional dependency: when ssl is enabled
//	!ssl?gnutls            inverse optional dependency
//	foo[ssl]               dependency must have ssl enabled
//	foo[-ssl]              dependency must have ssl disabled
//	foo[ssl?]              if the dependency knows ssl, require it enabled
//	ssl?foo[ssl,-zlib]     condition and USE constraints may be combined
package depspec

import (
	"fmt"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/pkgid"
	"github.com/studio-nosa-tech/zpkg/internal/version"
)

// Operator is a version comparison operator.
type Operator string

const (
	OpAny Operator = ""
	OpEQ  Operator = "="
	OpNE  Operator = "!="
	OpGE  Operator = ">="
	OpGT  Operator = ">"
	OpLE  Operator = "<="
	OpLT  Operator = "<"
)

// UseConstraint is one [flag] token attached to a dependency.
type UseConstraint struct {
	Flag     string
	Enabled  bool // false for -flag / !flag
	Optional bool // true for flag? ("only if the candidate has the flag")
}

// Atom is a parsed dependency or provides entry.
type Atom struct {
	Raw       string
	Name      string
	Slot      string // empty means any slot
	Op        Operator
	Version   string // empty when Op is OpAny
	Use       []UseConstraint
	Condition string // flag guarding an optional dependency
	Negated   bool   // condition is "!flag?"
}

// Parse parses a dependency atom. A version constraint requires the
// operator form (">=pkg-1.2"); a bare "pkg-1.2" stays a package name
// because package names may contain hyphens and digits.
func Parse(raw string) (Atom, error) {
	a := Atom{Raw: strings.TrimSpace(raw)}
	s := a.Raw
	if s == "" {
		return a, fmt.Errorf("empty dependency")
	}

	// Trailing USE constraints: [flag,-flag,flag?]
	if strings.HasSuffix(s, "]") {
		i := strings.LastIndexByte(s, '[')
		if i < 0 {
			return a, fmt.Errorf("%q: unmatched ']'", raw)
		}
		body, list := s[:i], s[i+1:len(s)-1]
		s = body
		for _, tok := range strings.Split(list, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				return a, fmt.Errorf("%q: empty USE constraint", raw)
			}
			uc := UseConstraint{Enabled: true}
			if strings.HasSuffix(tok, "?") {
				uc.Optional = true
				tok = strings.TrimSuffix(tok, "?")
			}
			if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "!") {
				uc.Enabled = false
				tok = tok[1:]
			}
			if !validFlag(tok) {
				return a, fmt.Errorf("%q: invalid USE flag %q", raw, tok)
			}
			uc.Flag = tok
			a.Use = append(a.Use, uc)
		}
	}

	// Optional dependency: [ ! ] flag ? atom
	if i := strings.IndexByte(s, '?'); i >= 0 {
		cond := s[:i]
		s = s[i+1:]
		if strings.HasPrefix(cond, "!") {
			a.Negated = true
			cond = cond[1:]
		}
		if cond == "" || !validFlag(cond) || s == "" {
			return a, fmt.Errorf("%q: malformed conditional dependency", raw)
		}
		a.Condition = cond
	}

	for _, op := range []Operator{OpGE, OpLE, OpNE, OpGT, OpLT, OpEQ} {
		if strings.HasPrefix(s, string(op)) {
			a.Op = op
			s = s[len(op):]
			break
		}
	}
	if s == "" {
		return a, fmt.Errorf("%q: missing package name", raw)
	}
	if a.Op == OpAny {
		name, slot, hasSlot, err := splitSlot(s)
		if err != nil {
			return a, fmt.Errorf("%q: %w", raw, err)
		}
		if hasSlot {
			a.Slot = slot
		}
		if err := checkName(name); err != nil {
			return a, fmt.Errorf("%q: %w", raw, err)
		}
		a.Name = name
		return a, nil
	}
	name, ver, ok := splitVersion(s)
	if !ok {
		return a, fmt.Errorf("%q: version constraint needs <name>-<version>", raw)
	}
	name, slot, hasSlot, err := splitSlot(name)
	if err != nil {
		return a, fmt.Errorf("%q: %w", raw, err)
	}
	if hasSlot {
		a.Slot = slot
	}
	if err := checkName(name); err != nil {
		return a, fmt.Errorf("%q: %w", raw, err)
	}
	if ver == "" {
		return a, fmt.Errorf("%q: empty version", raw)
	}
	a.Name, a.Version = name, ver
	return a, nil
}

// ParseProvided parses a provides entry such as "virtual/foo" or
// "virtual/foo-1.2". Unlike dependencies, a trailing -version is split
// without requiring an operator.
func ParseProvided(raw string) (Atom, error) {
	a, err := Parse(raw)
	if err != nil {
		return a, err
	}
	if a.Op != OpAny || a.Condition != "" || len(a.Use) > 0 || a.Slot != "" {
		return a, fmt.Errorf("%q: provides entries cannot have conditions, slots, USE constraints or operators", raw)
	}
	if name, ver, ok := splitVersion(a.Name); ok {
		a.Name, a.Version = name, ver
	}
	return a, nil
}

func (a Atom) String() string {
	if a.Raw != "" {
		return a.Raw
	}
	var b strings.Builder
	if a.Condition != "" {
		if a.Negated {
			b.WriteByte('!')
		}
		b.WriteString(a.Condition)
		b.WriteByte('?')
	}
	b.WriteString(string(a.Op))
	b.WriteString(a.Name)
	if a.Slot != "" {
		b.WriteByte('@')
		b.WriteString(a.Slot)
	}
	if a.Version != "" {
		b.WriteByte('-')
		b.WriteString(a.Version)
	}
	if len(a.Use) > 0 {
		b.WriteByte('[')
		for i, u := range a.Use {
			if i > 0 {
				b.WriteByte(',')
			}
			if !u.Enabled {
				b.WriteByte('-')
			}
			b.WriteString(u.Flag)
			if u.Optional {
				b.WriteByte('?')
			}
		}
		b.WriteByte(']')
	}
	return b.String()
}

// Matches reports whether version v satisfies the atom's version constraint.
func (a Atom) Matches(v string) bool {
	if a.Op == OpAny {
		return true
	}
	if v == "" {
		return false
	}
	c := version.Compare(v, a.Version)
	switch a.Op {
	case OpEQ:
		return c == 0
	case OpNE:
		return c != 0
	case OpGE:
		return c >= 0
	case OpGT:
		return c > 0
	case OpLE:
		return c <= 0
	case OpLT:
		return c < 0
	}
	return false
}

// splitVersion splits "name-1.2.3-r4" at the last hyphen whose suffix
// starts with a digit. It is only used when a version is expected; names
// without a numeric suffix are not split.
func splitVersion(s string) (name, ver string, ok bool) {
	for i := len(s) - 1; i > 0; i-- {
		if s[i] == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// splitSlot splits an optional "@slot" suffix. A slot is part of the package
// identity, not the version.
func splitSlot(s string) (name, slot string, has bool, err error) {
	i := strings.LastIndexByte(s, '@')
	if i < 0 {
		return s, "", false, nil
	}
	if i == 0 {
		return "", "", true, fmt.Errorf("missing package name before @")
	}
	slot = s[i+1:]
	if !pkgid.ValidSlot(slot) {
		return "", "", true, fmt.Errorf("invalid slot %q", slot)
	}
	return s[:i], slot, true, nil
}

func checkName(name string) error {
	if name == "" {
		return fmt.Errorf("empty package name")
	}
	if strings.ContainsAny(name, " \t[]?@") {
		return fmt.Errorf("invalid package name %q", name)
	}
	return nil
}

func validFlag(flag string) bool {
	if flag == "" {
		return false
	}
	for _, r := range flag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '+':
		default:
			return false
		}
	}
	return true
}
