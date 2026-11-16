// Binary package repositories. A repository is a directory (local path) or
// a URL base containing, for every built package:
//
//	<name>-<version>[-rN][-<usehash>].pkg.tar.gz   the binary package
//	<...>.manifest                                  the file list sidecar
//	<...>.meta.json                                 name/version/deps sidecar
//	index.json                                      the repository index
//
// zpkg can generate the index from a directory of built packages
// ("zpkg mkindex") and can install from a repository instead of building
// from source (ZPKG_BINREPO / BINREPO= in /etc/zpkg/binrepo.conf). Hosting
// the repository is out of scope: any static file server works.
package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

// BinMeta is the sidecar metadata of one binary package. It lets a
// repository index be built from binary packages alone, without the recipe
// tree.
type BinMeta struct {
	Schema   int      `json:"schema"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Revision int      `json:"revision"`
	UseHash  string   `json:"usehash"` // options hash, "" when no options were set
	Arch     string   `json:"arch"`
	Depends  []string `json:"depends,omitempty"`
}

const binMetaSchema = 1

func binMetaName(base string) string { return base + ".meta.json" }

// writeBinMeta writes the sidecar next to a packed binary package. Recipe
// deps are captured so the index (and therefore consumers) can record them
// without the recipe tree.
func writeBinMeta(dir, base string, r *recipe.Recipe, optsHash string) error {
	m := BinMeta{
		Schema:   binMetaSchema,
		Name:     r.Name,
		Version:  r.Version,
		Revision: r.Revision,
		UseHash:  optsHash,
		Arch:     runtime.GOARCH,
		Depends:  append([]string{}, r.Depends...),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, binMetaName(base)), append(data, '\n'), 0o644)
}

// IndexEntry is one binary package in a repository index.
type IndexEntry struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Revision int      `json:"revision"`
	UseHash  string   `json:"usehash"`
	Arch     string   `json:"arch"`
	Sha256   string   `json:"sha256"`
	Size     int64    `json:"size"`
	Filename string   `json:"filename"`
	Depends  []string `json:"depends,omitempty"`
}

// IndexFile is the repository index itself.
type IndexFile struct {
	Schema    int          `json:"schema"`
	Generated string       `json:"generated"`
	Arch      string       `json:"arch"`
	Packages  []IndexEntry `json:"packages"`
}

const indexSchema = 1

// MakeIndex scans dir for packed binary packages and writes index.json.
// It returns the number of indexed packages. The metadata comes from the
// .meta.json sidecars; packages without one are indexed with empty deps
// and their name/version/revision derived from the file name.
func MakeIndex(dir string) (int, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	idx := IndexFile{Schema: indexSchema, Generated: time.Now().UTC().Format(time.RFC3339), Arch: runtime.GOARCH}
	for _, it := range items {
		if !strings.HasSuffix(it.Name(), ".pkg.tar.gz") {
			continue
		}
		pkgPath := filepath.Join(dir, it.Name())
		sum, err := hash.File(pkgPath)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", it.Name(), err)
		}
		info, err := it.Info()
		if err != nil {
			return 0, err
		}
		entry := IndexEntry{
			Name:     it.Name(),
			Filename: it.Name(),
			Sha256:   sum,
			Size:     info.Size(),
			Arch:     runtime.GOARCH,
		}
		// Prefer the sidecar metadata; fall back to parsing the file name.
		metaPath := filepath.Join(dir, binMetaName(strings.TrimSuffix(it.Name(), ".pkg.tar.gz")))
		var meta BinMeta
		if data, err := os.ReadFile(metaPath); err == nil {
			if json.Unmarshal(data, &meta) == nil && meta.Name != "" {
				entry.Name = meta.Name
				entry.Version = meta.Version
				entry.Revision = meta.Revision
				entry.UseHash = meta.UseHash
				entry.Arch = meta.Arch
				entry.Depends = meta.Depends
			}
		}
		idx.Packages = append(idx.Packages, entry)
	}
	sort.Slice(idx.Packages, func(i, j int) bool { return idx.Packages[i].Name < idx.Packages[j].Name })
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), append(data, '\n'), 0o644); err != nil {
		return 0, err
	}
	return len(idx.Packages), nil
}

// LoadRepoBinpkg returns a Result restored from a binary package
// repository, or nil when there is no repository configured, no matching
// package, or the fetch/verification fails. Matching requires the same
// name, arch, version+revision and options hash — a repository binary that
// does not match the local options is ignored, not installed.
func LoadRepoBinpkg(r *recipe.Recipe, cfg config.Config) *Result {
	if os.Getenv("ZPKG_NO_BINPKG") == "1" {
		return nil
	}
	if cfg.BinRepo == "" {
		return nil
	}
	return loadRepoBinpkgImpl(r, cfg.BinRepo, cfg.Cache, cfg.Options.ForPackage(r.Name).Hash())
}

// fetchRepoFile returns the local path of a repository file, downloading
// it through the source cache when the repository is a URL.
func fetchRepoFile(repo, name, cacheDir string) (string, error) {
	if isURL(repo) {
		return source.Fetch(repo+"/"+name+"#"+name, cacheDir)
	}
	p := filepath.Join(repo, name)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// loadRepoBinpkgImpl finds a matching index entry and restores the package.
func loadRepoBinpkgImpl(r *recipe.Recipe, binRepo, cacheDir string, optsHash string) *Result {
	// Read the index on every run so repository updates are picked up.
	var indexData []byte
	var err error
	if isURL(binRepo) {
		var p string
		p, err = source.Fetch(binRepo+"/index.json", cacheDir)
		if err != nil {
			return nil
		}
		indexData, err = os.ReadFile(p)
	} else {
		indexData, err = os.ReadFile(filepath.Join(binRepo, "index.json"))
	}
	if err != nil {
		return nil
	}
	var idx IndexFile
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return nil
	}
	for _, e := range idx.Packages {
		if e.Name != r.Name || e.Arch != runtime.GOARCH ||
			recipeKey(e.Version, e.Revision) != r.FullVersion() || e.UseHash != optsHash {
			continue
		}
		// Found a match: fetch the package, verify it, unpack it.
		pkgPath, err := fetchRepoFile(binRepo, e.Filename, cacheDir)
		if err != nil {
			return nil
		}
		sum, err := hash.File(pkgPath)
		if err != nil || sum != e.Sha256 {
			return nil // repository and index disagree: build from source
		}
		tmp := filepath.Join(cacheDir, "build", r.Name, "repo")
		if err := os.RemoveAll(tmp); err != nil {
			return nil
		}
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			return nil
		}
		if err := source.Extract(pkgPath, tmp); err != nil {
			return nil
		}
		manPath, err := fetchRepoFile(binRepo, baseOf(e.Filename)+".manifest", cacheDir)
		if err != nil {
			return nil
		}
		entries, err := manifest.Read(manPath)
		if err != nil {
			return nil
		}
		return &Result{Recipe: r, Destroot: tmp, Entries: entries}
	}
	return nil
}

func recipeKey(version string, revision int) string {
	if revision > 0 {
		return fmt.Sprintf("%s-r%d", version, revision)
	}
	return version
}

func baseOf(filename string) string {
	return strings.TrimSuffix(filename, ".pkg.tar.gz")
}
