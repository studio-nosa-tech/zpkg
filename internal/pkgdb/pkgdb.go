// Package pkgdb tracks installed packages.
package pkgdb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/version"
)

// Schema is the current metadata schema version. Records without a
// meta.json are v1 (name + version + manifest only) and remain readable.
const Schema = 2

// DB is the installed package database rooted at a directory.
type DB struct{ Dir string }

// Meta is the package metadata stored next to the manifest. It exists so
// that upgrades, slots, USE flags and build provenance can be answered
// without rebuilding the recipe world.
type Meta struct {
	Schema    int      `json:"schema"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Revision  int      `json:"revision"`
	Slot      string   `json:"slot,omitempty"`
	Repo      string   `json:"repo,omitempty"`
	Channel   string   `json:"channel,omitempty"` // release channel used at install time
	Arch      string   `json:"arch,omitempty"`
	Installed string   `json:"installed,omitempty"` // RFC3339, UTC
	UseFlags  []string `json:"useflags,omitempty"`
	Depends   []string `json:"depends,omitempty"`      // runtime dependencies
	BuildDeps []string `json:"builddepends,omitempty"` // build-only dependencies
	Provides  []string `json:"provides,omitempty"`     // virtual packages this record satisfies
	Conflicts []string `json:"conflicts,omitempty"`    // packages this record must not coexist with
	BuildID   string   `json:"buildid,omitempty"`

	// Provenance of this installation. Origin is "source", "binpkg" (local
	// build cache) or "binrepo". For binrepo, the key and generation describe
	// the signed index that authorized the package.
	Origin            string `json:"origin,omitempty"`
	OriginRepo        string `json:"origin_repo,omitempty"`
	OriginKeyID       string `json:"origin_keyid,omitempty"`
	OriginGeneration  int64  `json:"origin_generation,omitempty"`
	OriginIndexSHA256 string `json:"origin_index_sha256,omitempty"`
	OriginPkgSHA256   string `json:"origin_pkg_sha256,omitempty"`

	// Build environment provenance for source builds.
	BuildSandbox  string `json:"build_sandbox,omitempty"` // "bwrap" or "none"
	BuildNetwork  string `json:"build_network,omitempty"` // "on" or "off"
	BuildEnvHash  string `json:"build_env_hash,omitempty"`
	BuildEnvCount int    `json:"build_env_count,omitempty"`
}

// Rec is one installed package record.
type Rec struct {
	Name     string
	Slot     string // "0" unless the recipe declares a slot
	Version  string
	Revision int
	Entries  []manifest.Entry
	Meta     *Meta // nil for records written by zpkg v1
}

// VersionString is the canonical "pkgver-rN" form.
func (r *Rec) VersionString() string { return version.String(r.Version, r.Revision) }

// RecKey is the DB directory key for a name/slot pair. Slot "0" (the
// default) keeps the legacy plain-name layout, so records written before
// slots existed need no migration; other slots get "name@slot".
func SlotKey(name, slot string) string {
	if slot == "" || slot == "0" {
		return name
	}
	return name + "@" + slot
}

// Ref identifies one installed record by name and slot.
type Ref struct {
	Name string
	Slot string
}

// String renders name or name@slot (non-zero slots only).
func (r Ref) String() string {
	if r.Slot == "" || r.Slot == "0" {
		return r.Name
	}
	return r.Name + "@" + r.Slot
}

func (db DB) recDir(name, slot string) string {
	return filepath.Join(db.Dir, SlotKey(name, slot))
}

// List returns the installed records as name/slot refs, sorted by key.
func (db DB) List() ([]Ref, error) {
	items, err := os.ReadDir(db.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var refs []Ref
	for _, it := range items {
		if !it.IsDir() {
			continue
		}
		name, slot := SplitKey(it.Name())
		refs = append(refs, Ref{Name: name, Slot: slot})
	}
	sort.Slice(refs, func(i, j int) bool { return SlotKey(refs[i].Name, refs[i].Slot) < SlotKey(refs[j].Name, refs[j].Slot) })
	return refs, nil
}

// SplitKey parses a DB directory key back into name and slot.
func SplitKey(key string) (string, string) {
	if i := strings.LastIndex(key, "@"); i > 0 {
		return key[:i], key[i+1:]
	}
	return key, "0"
}

// Get loads the record for a name/slot pair. A slot of "" means "0".
func (db DB) Get(name, slot string) (*Rec, error) {
	key := SlotKey(name, slot)
	dir := filepath.Join(db.Dir, key)
	vb, err := os.ReadFile(filepath.Join(dir, "version"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: not installed", name)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	entries, err := manifest.Read(filepath.Join(dir, "manifest"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	rec := &Rec{Name: name, Slot: slot, Version: strings.TrimSpace(string(vb)), Entries: entries}
	if mb, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		var m Meta
		if err := json.Unmarshal(mb, &m); err == nil {
			rec.Version = m.Version
			rec.Revision = m.Revision
			rec.Meta = &m
			return rec, nil
		}
	}
	// v1 record: the version file may already include the revision.
	rec.Version, rec.Revision = version.Parse(rec.Version)
	return rec, nil
}

// GetAll returns every slot installed for name, sorted by slot key.
func (db DB) GetAll(name string) ([]*Rec, error) {
	refs, err := db.List()
	if err != nil {
		return nil, err
	}
	var out []*Rec
	for _, ref := range refs {
		if ref.Name != name {
			continue
		}
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Owner returns the installed package owning path as a Ref, or a zero Ref
// when the path is not owned by anyone.
func (db DB) Owner(path string) (Ref, error) {
	refs, err := db.List()
	if err != nil {
		return Ref{}, err
	}
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return Ref{}, err
		}
		for _, e := range rec.Entries {
			if e.Path == path {
				return ref, nil
			}
		}
	}
	return Ref{}, nil
}

// Save writes a record atomically.
func (db DB) Save(rec *Rec) error {
	if rec.Slot == "" {
		rec.Slot = "0"
	}
	dir := db.recDir(rec.Name, rec.Slot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The plain version file stays for compatibility with zpkg v1.
	if err := writeFileAtomic(filepath.Join(dir, "version"), []byte(rec.VersionString()+"\n"), 0o644); err != nil {
		return err
	}
	m := rec.Meta
	if m == nil {
		m = &Meta{}
	}
	m.Schema = Schema
	m.Name = rec.Name
	m.Version = rec.Version
	m.Revision = rec.Revision
	if m.Slot == "" {
		m.Slot = "0"
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "meta.json"), append(mb, '\n'), 0o644); err != nil {
		return err
	}
	if err := manifest.Write(filepath.Join(dir, "manifest"), rec.Entries); err != nil {
		return err
	}
	// The record is the source of truth for remove/owner; make sure it is
	// on stable storage before the caller reports success.
	syncFile(filepath.Join(dir, "manifest"))
	syncDir(dir)
	return nil
}

// Delete removes the record of a name/slot pair.
func (db DB) Delete(name, slot string) error {
	return os.RemoveAll(db.recDir(name, slot))
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func syncFile(path string) {
	if f, err := os.Open(path); err == nil {
		f.Sync()
		f.Close()
	}
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
