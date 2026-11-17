// Package manifest handles package file inventories.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
		return fmt.Sprintf("dir %s", quoteField(e.Path))
	case "link":
		return fmt.Sprintf("link %s %s", quoteField(e.Link), quoteField(e.Path))
	case "file":
		return fmt.Sprintf("file %s %s", e.Hash, quoteField(e.Path))
	default:
		return fmt.Sprintf("%s %s", e.Kind, quoteField(e.Path))
	}
}

// quoteField quotes a path that would break the whitespace-separated
// manifest format. Firmware files legitimately contain spaces
// ("...Intel Corp.-VALLEYVIEW C0 PLATFORM.txt"), and symlink targets can
// contain them too. Unquoted fields stay byte-identical to older manifests.
func quoteField(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"\\") {
		return strconv.Quote(s)
	}
	return s
}

// takeField reads one field: a double-quoted Go string literal or the run
// up to the next space. The remainder (without the separating space) is
// returned as rest.
func takeField(s string) (field, rest string, err error) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", fmt.Errorf("missing field")
	}
	if s[0] == '"' {
		i := 1
		for i < len(s) {
			if s[i] == '\\' {
				i += 2
				continue
			}
			if s[i] == '"' {
				break
			}
			i++
		}
		if i >= len(s) {
			return "", "", fmt.Errorf("unterminated quoted field")
		}
		v, err := strconv.Unquote(s[:i+1])
		if err != nil {
			return "", "", err
		}
		return v, strings.TrimLeft(s[i+1:], " \t"), nil
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimLeft(s[i:], " \t"), nil
	}
	return s, "", nil
}

// takeRest reads the final field, unquoting a quoted value. An unquoted
// value is taken verbatim (so manifests written before quoting existed,
// which may contain spaces in the path, still parse).
func takeRest(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("missing path")
	}
	if s[0] == '"' {
		v, rest, err := takeField(s)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(rest) != "" {
			return "", fmt.Errorf("trailing data after quoted path")
		}
		return v, nil
	}
	return s, nil
}

// ParseLine parses a single manifest line.
func ParseLine(line string) (Entry, error) {
	line = strings.TrimSpace(line)
	kind, rest, err := takeField(line)
	if err != nil {
		return Entry{}, fmt.Errorf("malformed manifest line %q: %w", line, err)
	}
	switch kind {
	case "dir":
		p, err := takeRest(rest)
		if err != nil {
			return Entry{}, fmt.Errorf("malformed dir entry %q", line)
		}
		return Entry{Kind: "dir", Path: p}, nil
	case "link":
		link, rest2, err := takeField(rest)
		if err != nil {
			return Entry{}, fmt.Errorf("malformed link entry %q", line)
		}
		p, err := takeRest(rest2)
		if err != nil {
			return Entry{}, fmt.Errorf("malformed link entry %q", line)
		}
		return Entry{Kind: "link", Link: link, Path: p}, nil
	case "file":
		sum, rest2, err := takeField(rest)
		if err != nil {
			return Entry{}, fmt.Errorf("malformed file entry %q", line)
		}
		p, err := takeRest(rest2)
		if err != nil {
			return Entry{}, fmt.Errorf("malformed file entry %q", line)
		}
		return Entry{Kind: "file", Hash: sum, Path: p}, nil
	default:
		return Entry{}, fmt.Errorf("unknown entry kind %q", kind)
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
