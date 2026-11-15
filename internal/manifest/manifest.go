// Package manifest handles package file inventories.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
)

// Entry is a single record of a package's files.
type Entry struct {
	Kind string // "file", "dir" or "link"
	Hash string // sha256 hex (files only)
	Link string // symlink target (links only)
	Path string // path relative to the root, slash-separated
}

// Ignored reports paths that are generated indexes rather than package
// content: install-info maintains usr/share/info/dir, so every
// info-bearing package would collide on it. It is excluded from
// inventories and ownership checks.
func Ignored(path string) bool {
	return path == "usr/share/info/dir"
}

// String renders the manifest line format.
func (e Entry) String() string {
	switch e.Kind {
	case "dir":
		return fmt.Sprintf("dir %s", e.Path)
	case "link":
		return fmt.Sprintf("link %s %s", e.Link, e.Path)
	case "file":
		return fmt.Sprintf("file %s %s", e.Hash, e.Path)
	default:
		return fmt.Sprintf("%s %s", e.Kind, e.Path)
	}
}

// ParseLine parses a single manifest line.
func ParseLine(line string) (Entry, error) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return Entry{}, fmt.Errorf("empty manifest line")
	}
	switch f[0] {
	case "dir":
		if len(f) != 2 {
			return Entry{}, fmt.Errorf("malformed dir entry %q", line)
		}
		return Entry{Kind: "dir", Path: f[1]}, nil
	case "link":
		if len(f) != 3 {
			return Entry{}, fmt.Errorf("malformed link entry %q", line)
		}
		return Entry{Kind: "link", Link: f[1], Path: f[2]}, nil
	case "file":
		if len(f) != 3 {
			return Entry{}, fmt.Errorf("malformed file entry %q", line)
		}
		return Entry{Kind: "file", Hash: f[1], Path: f[2]}, nil
	default:
		return Entry{}, fmt.Errorf("unknown entry kind %q", f[0])
	}
}

// FromRoot inventories the tree at root (e.g. a destroot), sorted by path.
func FromRoot(root string) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if Ignored(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Kind: "link", Link: target, Path: rel})
		case info.IsDir():
			entries = append(entries, Entry{Kind: "dir", Path: rel})
		case info.Mode().IsRegular():
			sum, err := hash.File(p)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Kind: "file", Hash: sum, Path: rel})
		default:
			return fmt.Errorf("%s: unsupported file type", p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Write renders entries to a manifest file.
func Write(path string, entries []Entry) error {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Read parses a manifest file.
func Read(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
