// Package recipe loads ZIGNOS package recipes (KISS-style directories).
package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Recipe is a loaded recipe directory.
type Recipe struct {
	Name      string
	Dir       string   // recipe directory
	Version   string
	Sources   []string
	Checksums []string // aligned with Sources by index
	Depends   []string
	Build     string   // path of the build script
}

// Load finds the recipe for name across repos (first match wins).
func Load(repos []string, name string) (*Recipe, error) {
	for _, repo := range repos {
		dir := filepath.Join(repo, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return load(dir, name)
		}
	}
	return nil, fmt.Errorf("recipe not found: %s", name)
}

func load(dir, name string) (*Recipe, error) {
	r := &Recipe{Dir: dir, Name: name}

	versions, err := readLines(filepath.Join(dir, "version"), false)
	if err != nil {
		return nil, err
	}
	if len(versions) != 1 {
		return nil, fmt.Errorf("%s: version must be a single line", name)
	}
	r.Version = versions[0]

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
