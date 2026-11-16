// Package options loads build options: USE flags and the CFLAGS/MAKEOPTS
// used for recipe builds.
//
// Configuration lives in the target root:
//
//	/etc/zpkg/use.conf    whitespace-separated USE flags
//	/etc/zpkg/make.conf   CFLAGS=... / MAKEOPTS=... lines
//
// The ZPKG_USE, ZPKG_CFLAGS and ZPKG_MAKEOPTS environment variables
// override the files (handy for one-off builds and tests).
package options

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options is the effective build configuration.
type Options struct {
	Use      []string // enabled flags, sorted
	set      map[string]bool
	CFlags   string
	MakeOpts string
}

func usePath(root string) string  { return filepath.Join(root, "etc", "zpkg", "use.conf") }
func makePath(root string) string { return filepath.Join(root, "etc", "zpkg", "make.conf") }

// Load reads the configuration for root. Missing files mean "no options".
func Load(root string) (Options, error) {
	o := Options{set: map[string]bool{}}

	flags, err := readTokens(usePath(root))
	if err != nil {
		return o, err
	}
	if env := strings.TrimSpace(os.Getenv("ZPKG_USE")); env != "" {
		flags = strings.Fields(env)
	}
	for _, f := range flags {
		o.addFlag(f)
	}
	o.Use = o.flags()

	if v, err := readVar(makePath(root), "CFLAGS"); err != nil {
		return o, err
	} else if v != "" {
		o.CFlags = v
	}
	if env := os.Getenv("ZPKG_CFLAGS"); env != "" {
		o.CFlags = env
	}
	if v, err := readVar(makePath(root), "MAKEOPTS"); err != nil {
		return o, err
	} else if v != "" {
		o.MakeOpts = v
	}
	if env := os.Getenv("ZPKG_MAKEOPTS"); env != "" {
		o.MakeOpts = env
	}
	return o, nil
}

// Enabled reports whether a USE flag is on.
func (o Options) Enabled(flag string) bool { return o.set[flag] }

// Expand resolves conditional dependencies: "flag?pkg" yields pkg when the
// flag is enabled and disappears otherwise. Plain names pass through.
func (o Options) Expand(deps []string) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		i := strings.IndexByte(d, '?')
		if i < 0 {
			out = append(out, d)
			continue
		}
		flag, pkg := d[:i], d[i+1:]
		if pkg != "" && o.set[flag] {
			out = append(out, pkg)
		}
	}
	return out
}

// Hash is a short digest of the options, used in binpkg cache names so
// builds with different flags do not collide. Empty when no options are set.
func (o Options) Hash() string {
	if len(o.Use) == 0 && o.CFlags == "" && o.MakeOpts == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(o.Use, ",") + "|" + o.CFlags + "|" + o.MakeOpts))
	return hex.EncodeToString(sum[:])[:8]
}

// Save writes the USE flag set to root's use.conf.
func Save(root string, flags []string) error {
	sort.Strings(flags)
	dir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# ZIGNOS USE flags (one or more per line)\n")
	for _, f := range flags {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	p := usePath(root)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (o *Options) addFlag(f string) {
	f = strings.TrimSpace(f)
	if f == "" {
		return
	}
	if strings.HasPrefix(f, "-") {
		delete(o.set, strings.TrimPrefix(f, "-"))
		return
	}
	o.set[f] = true
}

func (o Options) flags() []string {
	flags := make([]string, 0, len(o.set))
	for f := range o.set {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	return flags
}

// readTokens returns whitespace-separated tokens, ignoring '#' comments.
func readTokens(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		out = append(out, strings.Fields(line)...)
	}
	return out, nil
}

// readVar returns KEY=value from a make.conf style file.
func readVar(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		return v, nil
	}
	return "", nil
}
