// Package recipe loads ZIGNOS package recipes (KISS-style directories).
package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
}

// FullVersion is the canonical "pkgver-rN" form.
func (r *Recipe) FullVersion() string {
	return version.String(r.Version, r.Revision)
}

// Load finds the recipe for name across repos (first match wins).
func Load(repos []string, name string) (*Recipe, error) {
	for _, repo := range repos {
		dir := filepath.Join(repo, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			r, err := load(dir, name)
			if err != nil {
				return nil, err
			}
			r.Repo = repo
			return r, nil
		}
	}
	return nil, fmt.Errorf("recipe not found: %s", name)
}

func load(dir, name string) (*Recipe, error) {
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

	if slots, err := readLines(filepath.Join(dir, "slot"), true); err != nil {
		return nil, err
	} else if len(slots) > 1 {
		return nil, fmt.Errorf("%s: slot must be a single line", name)
	} else if len(slots) == 1 {
		r.Slot = slots[0]
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

	r.Build = filepath.Join(dir, "build")
	if _, err := os.Stat(r.Build); err != nil {
		return nil, fmt.Errorf("%s: missing build script", name)
	}
	return r, nil
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
