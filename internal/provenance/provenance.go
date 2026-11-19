// Package provenance records the inputs and outputs of a package build so an
// artifact can be traced back to its recipe, sources, toolchain and
// environment. The JSON file is stored both next to the binary package and in
// the installed package database.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

// Schema is the provenance JSON schema.
const Schema = 1

// DefaultSourceDateEpoch is 2000-01-01T00:00:00Z. It is late enough for
// formats that reject pre-1980 timestamps and early enough to be obviously
// synthetic.
const DefaultSourceDateEpoch int64 = 946684800

// Info is one package's build provenance.
type Info struct {
	Schema       int         `json:"schema"`
	Package      Package     `json:"package"`
	Recipe       Recipe      `json:"recipe"`
	Sources      []Source    `json:"sources,omitempty"`
	Build        Build       `json:"build"`
	Toolchain    []Component `json:"toolchain,omitempty"`
	Dependencies []Component `json:"dependencies,omitempty"`
	BuildDeps    []Component `json:"build_dependencies,omitempty"`
	Environment  Environment `json:"environment"`
	Artifact     Artifact    `json:"artifact"`
	Log          Log         `json:"log,omitempty"`
}

// Package identifies the built package.
type Package struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Revision int    `json:"revision"`
	Slot     string `json:"slot"`
	Arch     string `json:"arch"`
}

// Recipe identifies the recipe that produced the package.
type Recipe struct {
	Repo       string `json:"repo"`
	Dir        string `json:"dir"`
	Channel    string `json:"channel,omitempty"`
	Digest     string `json:"digest"`
	KeyID      string `json:"keyid,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

// Source is one recipe source and its verified checksum.
type Source struct {
	URL    string `json:"url"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Build describes the build configuration and origin.
type Build struct {
	Use               []string `json:"use,omitempty"`
	CFlags            string   `json:"cflags,omitempty"`
	MakeOpts          string   `json:"makeopts,omitempty"`
	Sandbox           string   `json:"sandbox,omitempty"`
	Network           string   `json:"network,omitempty"`
	SourceDateEpoch   int64    `json:"source_date_epoch"`
	Started           string   `json:"started,omitempty"`
	Finished          string   `json:"finished,omitempty"`
	Origin            string   `json:"origin,omitempty"`
	OriginRepo        string   `json:"origin_repo,omitempty"`
	OriginKeyID       string   `json:"origin_keyid,omitempty"`
	OriginGeneration  int64    `json:"origin_generation,omitempty"`
	ZpkgVersion       string   `json:"zpkg_version,omitempty"`
	ReproducibleCheck bool     `json:"reproducible_check,omitempty"`
}

// Component is a named version (dependency or tool).
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Environment is the installed package set visible to the build.
type Environment struct {
	Hash     string   `json:"hash"`
	Count    int      `json:"count"`
	Packages []string `json:"packages"`
}

// Artifact describes the packed binary package.
type Artifact struct {
	Filename       string `json:"filename"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

// Log points at the persisted build log.
type Log struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Write writes info as indented JSON, atomically.
func Write(path string, info *Info) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Read loads a provenance file.
func Read(path string) (*Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if info.Schema != Schema {
		return nil, fmt.Errorf("%s: unsupported provenance schema %d", path, info.Schema)
	}
	return &info, nil
}

// RecipeDigest hashes every regular file (path, content, mode) and symlink
// (path, target) in a recipe directory. The result identifies the exact
// recipe inputs independently of git.
func RecipeDigest(dir string) (string, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			lines = append(lines, rel+"\x00link\x00"+target)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: unsupported file type", rel)
		}
		sum, err := hash.File(p)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s\x00%s\x00%04o", rel, sum, info.Mode().Perm()))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// Toolchain returns the versions of the build toolchain packages that are
// installed.
func Toolchain(db pkgdb.DB) []Component {
	var out []Component
	for _, name := range []string{"gcc", "binutils", "glibc", "make", "bash", "go", "zpkg", "bubblewrap"} {
		rec, err := db.Get(name, "0")
		if err != nil {
			continue
		}
		out = append(out, Component{Name: name, Version: rec.VersionString()})
	}
	return out
}

// InstalledEnvironment returns the sorted installed package list and its digest.
func InstalledEnvironment(db pkgdb.DB) ([]string, string, int) {
	refs, err := db.List()
	if err != nil {
		return nil, "", 0
	}
	lines := make([]string, 0, len(refs))
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			continue
		}
		lines = append(lines, ref.String()+"-"+rec.VersionString())
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return lines, hex.EncodeToString(sum[:]), len(lines)
}

// FromMeta builds a minimal provenance record for packages installed before
// provenance files existed or installed from a binary repository.
func FromMeta(rec *pkgdb.Rec) *Info {
	info := &Info{Schema: Schema}
	if rec.Meta == nil {
		info.Package = Package{Name: rec.Name, Version: rec.Version, Revision: rec.Revision, Slot: rec.Slot}
		return info
	}
	m := rec.Meta
	info.Package = Package{Name: m.Name, Version: m.Version, Revision: m.Revision, Slot: m.Slot, Arch: m.Arch}
	info.Recipe.Channel = m.Channel
	info.Build = Build{
		Use:              append([]string{}, m.UseFlags...),
		Sandbox:          m.BuildSandbox,
		Network:          m.BuildNetwork,
		Origin:           m.Origin,
		OriginRepo:       m.OriginRepo,
		OriginKeyID:      m.OriginKeyID,
		OriginGeneration: m.OriginGeneration,
	}
	if m.OriginPkgSHA256 != "" {
		info.Artifact.SHA256 = m.OriginPkgSHA256
	}
	info.Environment = Environment{Hash: m.BuildEnvHash, Count: m.BuildEnvCount}
	return info
}
