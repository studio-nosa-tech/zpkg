// Package pkgdb tracks installed packages.
package pkgdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
)

// DB is the installed package database rooted at a directory.
type DB struct{ Dir string }

// Rec is one installed package record.
type Rec struct {
	Name    string
	Version string
	Entries []manifest.Entry
}

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
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	entries, err := manifest.Read(filepath.Join(dir, "manifest"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &Rec{Name: name, Version: strings.TrimSpace(string(vb)), Entries: entries}, nil
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

// Save writes a record.
func (db DB) Save(rec *Rec) error {
	dir := db.recDir(rec.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte(rec.Version+"\n"), 0o644); err != nil {
		return err
	}
	return manifest.Write(filepath.Join(dir, "manifest"), rec.Entries)
}

// Delete removes the record of name.
func (db DB) Delete(name string) error {
	return os.RemoveAll(db.recDir(name))
}
