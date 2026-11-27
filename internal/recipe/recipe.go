// Package recipe loads ZIGNOS package recipes (KISS-style directories).
//
// A recipe directory describes one package candidate:
//
//	<repo>/<name>/...                         legacy layout, slot from "slot" (default 0)
//	<repo>/<name>@<slot>/...                  slot from the directory suffix
//
// Multiple versions of one package slot are kept in an explicit versions/
// directory:
//
//	<repo>/<name>/versions/<variant>/...
//	<repo>/<name>@<slot>/versions/<variant>/...
//
// The variant directory names are arbitrary; each variant is a normal recipe
// (version, revision, slot, build, ...). A package directory must not mix a
// top-level "version" file with a versions/ directory: the top-level file
// wins and versions/ is ignored.
package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/pkgid"
	"github.com/studio-nosa-tech/zpkg/internal/version"
)

// Recipe is a loaded recipe directory.
//
// Only version, sources/checksums and build are required. Everything else
// is optional so v0 recipes keep working unchanged.
type Recipe struct {
	Name        string
	Dir         string // recipe directory
	Repo        string // repo directory this recipe was found in
	Version     string
	Revision    int      // recipe revision (-rN), 0 when unset
	Slot        string   // slot ("0" when unset)
	IUse        []string // USE flags this recipe understands
	Provides    []string // virtual packages this recipe satisfies
	Conflicts   []string // packages that must not be installed alongside
	Sources     []string
	Checksums   []string // aligned with Sources by index
	Depends     []string // runtime dependencies
	MakeDepends []string // build-only dependencies
	Build       string   // path of the build script

	// BuildNetwork overrides the global build network policy for this
	// recipe. nil means "inherit"; false means "deny even if the global
	// policy allows it".
	BuildNetwork *bool

	// UpgradeWarning is an optional message shown by "zpkg upgrade" before
	// this package is replaced.
	UpgradeWarning string
}

// FullVersion is the canonical "pkgver-rN" form.
func (r *Recipe) FullVersion() string {
	return version.String(r.Version, r.Revision)
}

// Key is the package identity (name or name@slot).
func (r *Recipe) Key() string { return pkgid.Key(r.Name, r.Slot) }

// UsesVideoCards reports whether the recipe consumes ZPKG_VIDEO_CARDS.
// Such recipes carry a "video_cards" file; their binpkg key includes the
// selection so changing it does not reuse a package built for another GPU.
func (r *Recipe) UsesVideoCards() bool {
	if r.Dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(r.Dir, "video_cards"))
	return err == nil
}

// Load finds the default recipe for name across repos (first match wins).
//
// A legacy flat directory is preferred when present. Otherwise the highest
// version among the matching slot directories is returned. Callers that need
// every version or slot should use LoadCandidates.
func Load(repos []string, name string) (*Recipe, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	for _, repo := range repos {
		rs, err := scanRepoName(repo, name)
		if err != nil {
			return nil, err
		}
		if len(rs) == 0 {
			continue
		}
		flat := filepath.Join(repo, name)
		for _, r := range rs {
			if r.Dir == flat {
				return r, nil
			}
		}
		return bestVersion(rs), nil
	}
	return nil, fmt.Errorf("recipe not found: %s", name)
}

// LoadCandidate loads the highest version of one package key (name or
// name@slot). The first repository that provides that key wins.
func LoadCandidate(repos []string, key string) (*Recipe, error) {
	name, slot := pkgid.Split(key)
	if err := validName(name); err != nil {
		return nil, err
	}
	if !pkgid.ValidSlot(slot) {
		return nil, fmt.Errorf("invalid slot %q in package key %q", slot, key)
	}
	for _, repo := range repos {
		rs, err := scanRepoName(repo, name)
		if err != nil {
			return nil, err
		}
		var matched []*Recipe
		for _, r := range rs {
			if r.Slot == slot {
				matched = append(matched, r)
			}
		}
		if len(matched) == 0 {
			continue
		}
		return bestVersion(matched), nil
	}
	return nil, fmt.Errorf("recipe not found: %s", key)
}

// LoadCandidates returns every candidate for name across repos. All versions
// of a key are taken from the first repository that provides that key; later
// repositories may still provide other slots.
func LoadCandidates(repos []string, name string) ([]*Recipe, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []*Recipe
	for _, repo := range repos {
		rs, err := scanRepoName(repo, name)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if seen[r.Key()] {
				continue
			}
			out = append(out, r)
		}
		for _, r := range rs {
			seen[r.Key()] = true
		}
	}
	sortRecipes(out)
	return out, nil
}

// ScanRepos loads every candidate in repos. The first repository that
// provides a package key wins for that key (all of its versions are kept);
// later repositories are ignored for that key.
func ScanRepos(repos []string) ([]*Recipe, error) {
	seen := map[string]bool{}
	var out []*Recipe
	for _, repo := range repos {
		rs, err := ScanRepo(repo)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if seen[r.Key()] {
				continue
			}
			out = append(out, r)
		}
		for _, r := range rs {
			seen[r.Key()] = true
		}
	}
	sortRecipes(out)
	return out, nil
}

// ScanRepo loads every candidate in one repository directory.
func ScanRepo(repo string) ([]*Recipe, error) {
	entries, err := os.ReadDir(repo)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var out []*Recipe
	seen := map[string]bool{}
	for _, dirName := range names {
		name, dirSlot, hasSlot := splitPackageDir(dirName)
		if err := validName(name); err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		if hasSlot && !pkgid.ValidSlot(dirSlot) {
			return nil, fmt.Errorf("%s: invalid package directory %q", repo, dirName)
		}
		rs, err := loadPackageDir(filepath.Join(repo, dirName), name, dirSlot, hasSlot)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			dupKey := r.Key() + "\x00" + r.FullVersion()
			if seen[dupKey] {
				return nil, fmt.Errorf("%s: duplicate candidate %s %s", repo, r.Key(), r.FullVersion())
			}
			seen[dupKey] = true
			r.Repo = repo
		}
		out = append(out, rs...)
	}
	sortRecipes(out)
	return out, nil
}

// scanRepoName loads only the package directories matching name (or
// name@slot). It preserves the "first matching directory in a repository"
// behavior of Load.
func scanRepoName(repo, name string) ([]*Recipe, error) {
	entries, err := os.ReadDir(repo)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Name() == name || strings.HasPrefix(e.Name(), name+"@") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var out []*Recipe
	for _, dirName := range names {
		n, dirSlot, hasSlot := splitPackageDir(dirName)
		if err := validName(n); err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		if hasSlot && !pkgid.ValidSlot(dirSlot) {
			return nil, fmt.Errorf("%s: invalid package directory %q", repo, dirName)
		}
		rs, err := loadPackageDir(filepath.Join(repo, dirName), n, dirSlot, hasSlot)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			r.Repo = repo
		}
		out = append(out, rs...)
	}
	return out, nil
}

func splitPackageDir(dirName string) (name, slot string, hasSlot bool) {
	if strings.ContainsRune(dirName, '@') {
		name, slot = pkgid.Split(dirName)
		return name, slot, true
	}
	return dirName, "", false
}

func loadPackageDir(dir, name, dirSlot string, hasSlot bool) ([]*Recipe, error) {
	if info, err := os.Stat(filepath.Join(dir, "version")); err == nil && !info.IsDir() {
		r, err := load(dir, name, dirSlot, hasSlot)
		if err != nil {
			return nil, err
		}
		return []*Recipe{r}, nil
	}
	versionsDir := filepath.Join(dir, "versions")
	if info, err := os.Stat(versionsDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(versionsDir)
		if err != nil {
			return nil, err
		}
		var out []*Recipe
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			sub := filepath.Join(versionsDir, e.Name())
			if info, err := os.Stat(filepath.Join(sub, "version")); err != nil || info.IsDir() {
				return nil, fmt.Errorf("%s: %s: missing version file", name, sub)
			}
			r, err := load(sub, name, dirSlot, hasSlot)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s: no version candidates in %s", name, versionsDir)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: missing version file", dir)
}

func load(dir, name, dirSlot string, hasSlot bool) (*Recipe, error) {
	r := &Recipe{Dir: dir, Name: name, Slot: "0"}

	versions, err := readLines(filepath.Join(dir, "version"), false)
	if err != nil {
		return nil, err
	}
	if len(versions) != 1 {
		return nil, fmt.Errorf("%s: version must be a single line", name)
	}
	r.Version = versions[0]

	if revs, err := readLines(filepath.Join(dir, "revision"), true); err != nil {
		return nil, err
	} else if len(revs) > 1 {
		return nil, fmt.Errorf("%s: revision must be a single line", name)
	} else if len(revs) == 1 {
		n, err := strconv.Atoi(revs[0])
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: revision %q is not a non-negative integer", name, revs[0])
		}
		r.Revision = n
	}

	fileSlot := ""
	if slots, err := readLines(filepath.Join(dir, "slot"), true); err != nil {
		return nil, err
	} else if len(slots) > 1 {
		return nil, fmt.Errorf("%s: slot must be a single line", name)
	} else if len(slots) == 1 {
		if !pkgid.ValidSlot(slots[0]) {
			return nil, fmt.Errorf("%s: invalid slot %q", name, slots[0])
		}
		fileSlot = slots[0]
	}
	if hasSlot {
		if fileSlot != "" && fileSlot != dirSlot {
			return nil, fmt.Errorf("%s: slot file %q disagrees with directory slot %q", name, fileSlot, dirSlot)
		}
		r.Slot = dirSlot
	} else if fileSlot != "" {
		r.Slot = fileSlot
	}

	if r.Sources, err = readLines(filepath.Join(dir, "sources"), true); err != nil {
		return nil, err
	}
	if r.Checksums, err = readLines(filepath.Join(dir, "checksums"), true); err != nil {
		return nil, err
	}
	if len(r.Checksums) != len(r.Sources) {
		return nil, fmt.Errorf("%s: %d checksums for %d sources", name, len(r.Checksums), len(r.Sources))
	}
	if r.Depends, err = readLines(filepath.Join(dir, "depends"), true); err != nil {
		return nil, err
	}
	if r.MakeDepends, err = readLines(filepath.Join(dir, "makedepends"), true); err != nil {
		return nil, err
	}
	if r.IUse, err = readLines(filepath.Join(dir, "iuse"), true); err != nil {
		return nil, err
	}
	if r.Provides, err = readLines(filepath.Join(dir, "provides"), true); err != nil {
		return nil, err
	}
	if r.Conflicts, err = readLines(filepath.Join(dir, "conflicts"), true); err != nil {
		return nil, err
	}
	if vals, err := readLines(filepath.Join(dir, "build_network"), true); err != nil {
		return nil, err
	} else if len(vals) > 1 {
		return nil, fmt.Errorf("%s: build_network must be a single line", name)
	} else if len(vals) == 1 {
		switch strings.ToLower(vals[0]) {
		case "1", "yes", "true", "on":
			v := true
			r.BuildNetwork = &v
		case "0", "no", "false", "off":
			v := false
			r.BuildNetwork = &v
		default:
			return nil, fmt.Errorf("%s: build_network %q is not 0 or 1", name, vals[0])
		}
	}

	if vals, err := readLines(filepath.Join(dir, "upgrade_warning"), true); err != nil {
		return nil, err
	} else if len(vals) > 0 {
		r.UpgradeWarning = strings.Join(vals, " ")
	}

	r.Build = filepath.Join(dir, "build")
	if _, err := os.Stat(r.Build); err != nil {
		return nil, fmt.Errorf("%s: missing build script", name)
	}
	return r, nil
}

func validName(name string) error {
	if name == "" {
		return fmt.Errorf("empty package name")
	}
	if strings.ContainsAny(name, "@ \t[]?") {
		return fmt.Errorf("invalid package name %q", name)
	}
	return nil
}

func bestVersion(rs []*Recipe) *Recipe {
	best := rs[0]
	for _, r := range rs[1:] {
		if c := version.Compare(r.FullVersion(), best.FullVersion()); c > 0 {
			best = r
			continue
		} else if c == 0 && r.Key() < best.Key() {
			best = r
		}
	}
	return best
}

func sortRecipes(rs []*Recipe) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Name != rs[j].Name {
			return rs[i].Name < rs[j].Name
		}
		if rs[i].Key() != rs[j].Key() {
			return rs[i].Key() < rs[j].Key()
		}
		if c := version.Compare(rs[i].FullVersion(), rs[j].FullVersion()); c != 0 {
			return c > 0
		}
		return rs[i].Dir < rs[j].Dir
	})
}

// readLines reads a recipe file, skipping blank lines and '#' comments.
func readLines(path string, allowMissing bool) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if allowMissing && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}
