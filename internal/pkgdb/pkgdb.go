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
	Arch      string   `json:"arch,omitempty"`
	Installed string   `json:"installed,omitempty"` // RFC3339, UTC
	UseFlags  []string `json:"useflags,omitempty"`
	Depends   []string `json:"depends,omitempty"`      // runtime dependencies
	BuildDeps []string `json:"builddepends,omitempty"` // build-only dependencies
	BuildID   string   `json:"buildid,omitempty"`
}

// Rec is one installed package record.
type Rec struct {
	Name     string
	Version  string
	Revision int
	Entries  []manifest.Entry
	Meta     *Meta // nil for records written by zpkg v1
}

// VersionString is the canonical "pkgver-rN" form.
func (r *Rec) VersionString() string { return version.String(r.Version, r.Revision) }

func (db DB) recDir(name string) string {
	return filepath.Join(db.Dir, name)
}

// List returns installed package names, sorted.
func (db DB) List() ([]string, error) {
	items, err := os.ReadDir(db.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, it := range items {
		if it.IsDir() {
			names = append(names, it.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Get loads the record for name.
func (db DB) Get(name string) (*Rec, error) {
	dir := db.recDir(name)
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
	rec := &Rec{Name: name, Version: strings.TrimSpace(string(vb)), Entries: entries}
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

// Owner returns the name of the installed package owning path, or "" when
// the path is not owned by anyone.
func (db DB) Owner(path string) (string, error) {
	names, err := db.List()
	if err != nil {
		return "", err
	}
	for _, n := range names {
		rec, err := db.Get(n)
		if err != nil {
			return "", err
		}
		for _, e := range rec.Entries {
			if e.Path == path {
				return n, nil
			}
		}
	}
	return "", nil
}

// Save writes a record atomically.
func (db DB) Save(rec *Rec) error {
	dir := db.recDir(rec.Name)
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

// Delete removes the record of name.
func (db DB) Delete(name string) error {
	return os.RemoveAll(db.recDir(name))
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
