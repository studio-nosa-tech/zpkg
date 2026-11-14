// Package build runs recipe build scripts into a destroot.
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

// Result describes one completed build.
type Result struct {
	Recipe   *recipe.Recipe
	Destroot string
	Entries  []manifest.Entry
}

// Run fetches, verifies and extracts sources, then executes the build script
// with ZPKG_DESTROOT pointing at a fresh destroot. On success it returns the
// destroot inventory.
func Run(r *recipe.Recipe, cfg config.Config) (*Result, error) {
	work := filepath.Join(cfg.Cache, "build", r.Name)
	destroot := filepath.Join(work, "destroot")
	src := filepath.Join(work, "src")
	for _, d := range []string{destroot, src} {
		if err := os.RemoveAll(d); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	if len(r.Sources) > 0 {
		var files []string
		for _, u := range r.Sources {
			f, err := source.Fetch(u, cfg.Cache)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
		}
		if err := source.Verify(files, r.Checksums); err != nil {
			return nil, err
		}
		for _, f := range files {
			if err := source.Extract(f, src); err != nil {
				return nil, err
			}
		}
	}

	cmd := exec.Command("bash", r.Build)
	cmd.Dir = src
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"ZPKG_NAME="+r.Name,
		"ZPKG_VERSION="+r.Version,
		"ZPKG_ROOT="+cfg.Root,
		"ZPKG_CACHE="+cfg.Cache,
		"ZPKG_DESTROOT="+destroot,
		"ZPKG_SRC="+src,
		"ZPKG_WORKDIR="+work,
	)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: build script failed: %w", r.Name, err)
	}

	entries, err := manifest.FromRoot(destroot)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: build script produced no files in destroot", r.Name)
	}
	return &Result{Recipe: r, Destroot: destroot, Entries: entries}, nil
}
