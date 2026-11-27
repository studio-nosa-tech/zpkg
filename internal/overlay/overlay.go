// Package overlay manages additional recipe repositories layered on top of
// the official ZIGNOS tree.
//
// An overlay is a normal recipe repository with an optional overlay.conf at
// its root. It is synced to $ZPKG_ROOT/var/lib/zpkg/overlays/<name> and
// enabled by /etc/zpkg/overlays.d/<name>.conf. Active overlays are searched
// before the base tree, so they can add packages or override official ones.
package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/options"
)

// OfficialName is the official ZIGNOS MENU overlay.
const OfficialName = "zignos-menu"

// OfficialURL is the default source for the official overlay.
const OfficialURL = "https://github.com/studio-nosa-tech/zignos-menu.git"

// MetaFile is the optional metadata file at an overlay root.
const MetaFile = "overlay.conf"

// Spec is the user configuration of one overlay.
type Spec struct {
	Name          string
	URL           string
	Branch        string
	Priority      int
	AllowUnsigned bool
	Enabled       bool
}

// Meta is the signed metadata shipped inside an overlay tree.
type Meta struct {
	Name        string
	Description string
	Homepage    string
	Categories  []string
	Channels    []string
}

// Overlay is a configured overlay plus its local state.
type Overlay struct {
	Spec
	Dir     string
	Meta    Meta
	Present bool
	Active  bool
	Err     string
}

// ValidName reports whether name is safe as an overlay name and config file
// name.
func ValidName(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ConfigDir is the overlay configuration directory under a target root.
func ConfigDir(root string) string {
	return filepath.Join(root, "etc", "zpkg", "overlays.d")
}

// TreeDir is the synced overlay tree for name.
func TreeDir(root, name string) string {
	return filepath.Join(root, "var", "lib", "zpkg", "overlays", name)
}

// Load reads /etc/zpkg/overlays.d/*.conf and the metadata of each synced
// tree. A broken overlay is returned with Err set instead of failing every
// zpkg operation; Active is left for Activate.
func Load(root string) ([]Overlay, error) {
	dir := ConfigDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".conf"))
	}
	sort.Strings(names)

	var out []Overlay
	for _, name := range names {
		if !ValidName(name) {
			out = append(out, Overlay{Spec: Spec{Name: name}, Err: "invalid overlay name"})
			continue
		}
		sp, err := readSpec(root, name)
		ov := Overlay{Spec: sp, Dir: TreeDir(root, name)}
		if err != nil {
			ov.Err = err.Error()
			out = append(out, ov)
			continue
		}
		if !isDir(ov.Dir) {
			ov.Err = "not synced"
			out = append(out, ov)
			continue
		}
		meta, err := ReadMeta(ov.Dir)
		if err != nil {
			ov.Err = err.Error()
			out = append(out, ov)
			continue
		}
		ov.Meta = meta
		ov.Present = true
		if err := validateMeta(ov); err != nil {
			ov.Err = err.Error()
		}
		out = append(out, ov)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func readSpec(root, name string) (Spec, error) {
	rel := filepath.Join("etc", "zpkg", "overlays.d", name+".conf")
	url, err := options.ReadVar(root, rel, "URL")
	if err != nil {
		return Spec{Name: name}, err
	}
	if strings.TrimSpace(url) == "" {
		return Spec{Name: name}, fmt.Errorf("overlay %s: URL is missing", name)
	}
	sp := Spec{Name: name, URL: strings.TrimSpace(url), Branch: "main", Priority: 10, Enabled: true}
	if v, err := options.ReadVar(root, rel, "BRANCH"); err != nil {
		return sp, err
	} else if v = strings.TrimSpace(v); v != "" {
		sp.Branch = v
	}
	if v, err := options.ReadVar(root, rel, "PRIORITY"); err != nil {
		return sp, err
	} else if v = strings.TrimSpace(v); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return sp, fmt.Errorf("overlay %s: invalid PRIORITY %q", name, v)
		}
		sp.Priority = n
	}
	if sp.AllowUnsigned, err = readBool(root, rel, "ALLOW_UNSIGNED", false); err != nil {
		return sp, err
	}
	if sp.Enabled, err = readBool(root, rel, "ENABLED", true); err != nil {
		return sp, err
	}
	return sp, nil
}

func readBool(root, rel, key string, def bool) (bool, error) {
	v, err := options.ReadVar(root, rel, key)
	if err != nil {
		return def, err
	}
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return def, fmt.Errorf("%s: %s must be 0 or 1, not %q", rel, key, v)
	}
}

// WriteSpec writes the overlay configuration atomically, creating the config
// directory when needed.
func WriteSpec(root string, sp Spec) error {
	if !ValidName(sp.Name) {
		return fmt.Errorf("invalid overlay name %q", sp.Name)
	}
	if strings.TrimSpace(sp.URL) == "" {
		return fmt.Errorf("overlay %s: URL is empty", sp.Name)
	}
	branch := sp.Branch
	if branch == "" {
		branch = "main"
	}
	dir := ConfigDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# ZIGNOS overlay: %s\nURL=%s\nBRANCH=%s\nPRIORITY=%d\nALLOW_UNSIGNED=%d\nENABLED=%d\n",
		sp.Name, sp.URL, branch, sp.Priority, boolInt(sp.AllowUnsigned), boolInt(sp.Enabled))
	tmp, err := os.CreateTemp(dir, "."+sp.Name+".conf.tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, sp.Name+".conf")); err != nil {
		return err
	}
	ok = true
	return nil
}

// RemoveSpec deletes the overlay configuration. A missing file is not an
// error.
func RemoveSpec(root, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid overlay name %q", name)
	}
	err := os.Remove(filepath.Join(ConfigDir(root), name+".conf"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadMeta reads overlay.conf from a synced tree. A missing file is valid
// for a flat overlay.
func ReadMeta(dir string) (Meta, error) {
	read := func(key string) (string, error) {
		return options.ReadVar(dir, MetaFile, key)
	}
	var m Meta
	var err error
	if m.Name, err = read("NAME"); err != nil {
		return m, err
	}
	if m.Description, err = read("DESCRIPTION"); err != nil {
		return m, err
	}
	if m.Homepage, err = read("HOMEPAGE"); err != nil {
		return m, err
	}
	var cats, chans string
	if cats, err = read("CATEGORIES"); err != nil {
		return m, err
	}
	if chans, err = read("CHANNELS"); err != nil {
		return m, err
	}
	m.Categories = SplitList(cats)
	m.Channels = SplitList(chans)
	return m, nil
}

// SplitList splits a comma/whitespace separated metadata list.
func SplitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}

func validateMeta(ov Overlay) error {
	for _, cat := range ov.Meta.Categories {
		if !validCategory(cat) {
			return fmt.Errorf("overlay %s: invalid category %q", ov.Name, cat)
		}
		if !isDir(filepath.Join(ov.Dir, cat)) {
			return fmt.Errorf("overlay %s: category %q is missing", ov.Name, cat)
		}
	}
	return nil
}

func validCategory(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '+', r == '-':
		default:
			return false
		}
	}
	return true
}

// Activate marks the overlays that are usable for the selected channel.
func Activate(ovs []Overlay, channel string) {
	for i := range ovs {
		ov := &ovs[i]
		ov.Active = ov.Enabled && ov.Present && ov.Err == "" && channelAllowed(ov.Meta.Channels, channel)
	}
}

func channelAllowed(channels []string, channel string) bool {
	if len(channels) == 0 {
		return true
	}
	for _, c := range channels {
		if c == channel {
			return true
		}
	}
	return false
}

// Repos returns the recipe repo directories of one active overlay. A
// categorized overlay contributes one repo per category; a flat overlay
// contributes its root.
func (ov Overlay) Repos() []string {
	if len(ov.Meta.Categories) == 0 {
		return []string{ov.Dir}
	}
	out := make([]string, 0, len(ov.Meta.Categories))
	for _, cat := range ov.Meta.Categories {
		out = append(out, filepath.Join(ov.Dir, cat))
	}
	return out
}

// ActiveRepos flattens the repos of active overlays in priority order.
func ActiveRepos(ovs []Overlay) []string {
	var out []string
	for _, ov := range ovs {
		if !ov.Active {
			continue
		}
		out = append(out, ov.Repos()...)
	}
	return out
}

// ByName returns the overlay with name.
func ByName(ovs []Overlay, name string) *Overlay {
	for i := range ovs {
		if ovs[i].Name == name {
			return &ovs[i]
		}
	}
	return nil
}

// ForRepo returns the overlay that owns repo, or nil.
func ForRepo(ovs []Overlay, repo string) *Overlay {
	for i := range ovs {
		if within(repo, ovs[i].Dir) {
			return &ovs[i]
		}
	}
	return nil
}

// NameForRepo returns the overlay name that owns repo, or "".
func NameForRepo(ovs []Overlay, repo string) string {
	if ov := ForRepo(ovs, repo); ov != nil {
		return ov.Name
	}
	return ""
}

// LabelForRepo renders repo as the base category name or overlay/category.
func LabelForRepo(ovs []Overlay, repo string) string {
	ov := ForRepo(ovs, repo)
	if ov == nil {
		return filepath.Base(repo)
	}
	rel, err := filepath.Rel(ov.Dir, repo)
	if err != nil || rel == "." || rel == "" {
		return ov.Name
	}
	return ov.Name + "/" + filepath.ToSlash(rel)
}

func within(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
