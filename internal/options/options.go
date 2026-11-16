// Package options loads build options: USE flags and the CFLAGS/MAKEOPTS
// used for recipe builds.
//
// Configuration lives in the target root:
//
//	/etc/zpkg/use.conf      whitespace-separated global USE flags
//	/etc/zpkg/package.use   one line per package: "<pkg> flag -flag ..."
//	/etc/zpkg/make.conf     CFLAGS=... / MAKEOPTS=... lines
//
// The ZPKG_USE, ZPKG_CFLAGS and ZPKG_MAKEOPTS environment variables
// override the files (handy for one-off builds and tests); an explicitly
// empty value clears the file setting.
package options

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options is the effective build configuration. The per-package tokens are
// kept raw so ForPackage can apply them on top of the global set.
type Options struct {
	Use      []string // enabled flags, sorted
	set      map[string]bool
	CFlags   string
	MakeOpts string
	pkgUse   map[string][]string // package -> raw "+flag"/"-flag" tokens
}

func usePath(root string) string  { return filepath.Join(root, "etc", "zpkg", "use.conf") }
func makePath(root string) string { return filepath.Join(root, "etc", "zpkg", "make.conf") }
func pkgUsePath(root string) string {
	return filepath.Join(root, "etc", "zpkg", "package.use")
}

// Load reads the configuration for root. Missing files mean "no options".
func Load(root string) (Options, error) {
	o := Options{set: map[string]bool{}, pkgUse: map[string][]string{}}

	flags, err := readTokens(usePath(root))
	if err != nil {
		return o, err
	}
	// An explicitly empty variable means "no flags" and must override the
	// file, so use LookupEnv rather than treating "" as unset.
	if env, ok := os.LookupEnv("ZPKG_USE"); ok {
		flags = strings.Fields(env)
	}
	for _, f := range flags {
		o.addFlag(f)
	}
	o.Use = o.flags()

	if o.pkgUse, err = readPackageUse(pkgUsePath(root)); err != nil {
		return o, err
	}

	if v, err := readVar(makePath(root), "CFLAGS"); err != nil {
		return o, err
	} else if v != "" {
		o.CFlags = v
	}
	if env, ok := os.LookupEnv("ZPKG_CFLAGS"); ok {
		o.CFlags = env
	}
	if v, err := readVar(makePath(root), "MAKEOPTS"); err != nil {
		return o, err
	} else if v != "" {
		o.MakeOpts = v
	}
	if env, ok := os.LookupEnv("ZPKG_MAKEOPTS"); ok {
		o.MakeOpts = env
	}
	return o, nil
}

// ForPackage returns the options that apply to one package: the global set
// with package.use tokens applied on top.
func (o Options) ForPackage(name string) Options {
	tokens := o.pkgUse[name]
	if len(tokens) == 0 {
		return o
	}
	p := Options{
		set:      make(map[string]bool, len(o.set)+len(tokens)),
		CFlags:   o.CFlags,
		MakeOpts: o.MakeOpts,
		pkgUse:   o.pkgUse,
	}
	for f := range o.set {
		p.set[f] = true
	}
	for _, t := range tokens {
		p.addFlag(t)
	}
	p.Use = p.flags()
	return p
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

// Hash is a short digest of the effective options, used in binpkg cache
// names so builds with different flags do not collide. Empty when no
// options are set.
func (o Options) Hash() string {
	if len(o.Use) == 0 && o.CFlags == "" && o.MakeOpts == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(o.Use, ",") + "|" + o.CFlags + "|" + o.MakeOpts))
	return hex.EncodeToString(sum[:])[:8]
}

// Save writes the global USE flag set to root's use.conf.
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
	return writeAtomic(usePath(root), []byte(b.String()))
}

// PackageTokens returns the raw per-package tokens for name.
func (o Options) PackageTokens(name string) []string {
	return append([]string{}, o.pkgUse[name]...)
}

// SavePackageUse rewrites name's line in package.use. An empty token list
// removes the line.
func SavePackageUse(root, name string, tokens []string) error {
	all, err := readPackageUse(pkgUsePath(root))
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		delete(all, name)
	} else {
		all[name] = append([]string{}, tokens...)
	}
	return writePackageUse(pkgUsePath(root), all)
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

// readPackageUse parses lines of the form "<pkg> flag -flag ...".
func readPackageUse(path string) (map[string][]string, error) {
	fields, err := readTokens(path)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, line := range strings.Split(strings.Join(fields, "\n"), "\n") {
		_ = line // readTokens already flattened; re-read per line below
	}
	// Re-read preserving line structure (readTokens flattens lines).
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		out[parts[0]] = append(out[parts[0]], parts[1:]...)
	}
	return out, nil
}

func writePackageUse(path string, m map[string][]string) error {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# ZIGNOS per-package USE flags: <package> flag -flag ...\n")
	for _, n := range names {
		b.WriteString(n)
		for _, t := range m[n] {
			b.WriteByte(' ')
			b.WriteString(t)
		}
		b.WriteByte('\n')
	}
	return writeAtomic(path, []byte(b.String()))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
