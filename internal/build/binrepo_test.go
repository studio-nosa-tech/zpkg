package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

func packTo(t *testing.T, cache, usehash string) *recipe.Recipe {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "usr/bin/hello")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := manifest.FromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{Name: "hello", Version: "1.0.0", Revision: 2, Depends: []string{"glibc"}}
	res := &Result{Recipe: r, Destroot: root, Entries: entries}
	if _, err := PackBinpkg(r, res, cache, usehash); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMakeIndexAndRepoLoad(t *testing.T) {
	// Pack a binary package (r2, no build options), then turn its cache
	// dir into a repository. Empty options hash to "".
	cache := t.TempDir()
	r := packTo(t, cache, "")
	repo := t.TempDir()
	// Copy the pack output (pkg + manifest + meta) into the repo dir.
	cacheBin := filepath.Join(cache, "binpkgs")
	items, err := os.ReadDir(cacheBin)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("expected pkg/manifest/meta sidecars, got %d files", len(items))
	}
	for _, it := range items {
		data, err := os.ReadFile(filepath.Join(cacheBin, it.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, it.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	n, err := MakeIndex(repo)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("MakeIndex = %d packages, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(repo, "index.json")); err != nil {
		t.Fatal("index.json missing:", err)
	}

	// A fresh cache (no local binpkgs) with the repository configured: the
	// package must be restored from the repository.
	cfg := config.Config{
		Cache:   t.TempDir(),
		BinRepo: repo,
	}
	res := LoadRepoBinpkg(r, cfg)
	if res == nil {
		t.Fatal("repository package not restored")
	}
	found := false
	for _, e := range res.Entries {
		if e.Kind == "file" && e.Path == "usr/bin/hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file entry missing from restored manifest: %+v", res.Entries)
	}
	if _, err := os.Stat(filepath.Join(res.Destroot, "usr", "bin", "hello")); err != nil {
		t.Fatal("destroot not restored:", err)
	}
}

func TestRepoNoMatchFallsBack(t *testing.T) {
	cache := t.TempDir()
	packTo(t, cache, "")
	repo := t.TempDir()
	cacheBin := filepath.Join(cache, "binpkgs")
	items, _ := os.ReadDir(cacheBin)
	for _, it := range items {
		data, _ := os.ReadFile(filepath.Join(cacheBin, it.Name()))
		os.WriteFile(filepath.Join(repo, it.Name()), data, 0o644)
	}
	MakeIndex(repo)

	// Same version but a different options hash: no match is the correct
	// behavior (the repository binary was built with no flags).
	t.Setenv("ZPKG_USE", "demo")
	o, err := options.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Cache: t.TempDir(), BinRepo: repo, Options: o}
	if o.Hash() == "" {
		t.Fatal("options hash should be non-empty with a flag set")
	}
	res := LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0", Revision: 2}, cfg)
	if res != nil {
		t.Fatal("a package with a different options hash must not match")
	}
	// A different version must not match either.
	res = LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "9.9.9", Revision: 2}, cfg)
	if res != nil {
		t.Fatal("a package with a different version must not match")
	}
}

func TestRepoDisabledWithoutConfig(t *testing.T) {
	cfg := config.Config{Cache: t.TempDir(), BinRepo: ""}
	if res := LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0"}, cfg); res != nil {
		t.Fatal("no repository configured: must return nil")
	}
	t.Setenv("ZPKG_NO_BINPKG", "1")
	cfg.BinRepo = "/nonexistent"
	if res := LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0"}, cfg); res != nil {
		t.Fatal("ZPKG_NO_BINPKG=1 must disable the repository too")
	}
}
