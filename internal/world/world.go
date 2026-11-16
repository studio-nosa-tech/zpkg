// Package world tracks the packages the user asked for explicitly, as
// opposed to dependencies pulled in for them. It lives next to the package
// database as a plain sorted text file.
package world

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func path(dir string) string { return filepath.Join(dir, "world") }

// Read returns the world set, sorted.
func Read(dir string) ([]string, error) {
	data, err := os.ReadFile(path(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	sort.Strings(names)
	return names, nil
}

// Add inserts name unless it is already present.
func Add(dir, name string) error {
	names, err := Read(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return write(dir, append(names, name))
}

// Remove drops name from the world set.
func Remove(dir, name string) error {
	names, err := Read(dir)
	if err != nil {
		return err
	}
	var kept []string
	for _, n := range names {
		if n != name {
			kept = append(kept, n)
		}
	}
	return write(dir, kept)
}

func write(dir string, names []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# zpkg world: packages installed explicitly (do not edit while zpkg runs)\n")
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	p := path(dir)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
