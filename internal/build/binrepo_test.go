package build

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
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
	if _, err := PackBinpkg(r, res, cache, usehash, ""); err != nil {
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
		Cache:         t.TempDir(),
		DB:            t.TempDir(),
		BinRepo:       repo,
		AllowUnsigned: true,
	}
	res, err := LoadRepoBinpkg(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
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
	cfg := config.Config{Cache: t.TempDir(), DB: t.TempDir(), BinRepo: repo, Options: o, AllowUnsigned: true}
	if o.Hash() == "" {
		t.Fatal("options hash should be non-empty with a flag set")
	}
	res, err := LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0", Revision: 2}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatal("a package with a different options hash must not match")
	}
	// A different version must not match either.
	res, err = LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "9.9.9", Revision: 2}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatal("a package with a different version must not match")
	}
}

func TestRepoDisabledWithoutConfig(t *testing.T) {
	cfg := config.Config{Cache: t.TempDir(), BinRepo: ""}
	res, err := LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0"}, cfg)
	if err != nil || res != nil {
		t.Fatalf("no repository configured: %v, %v", res, err)
	}
	t.Setenv("ZPKG_NO_BINPKG", "1")
	cfg.BinRepo = "/nonexistent"
	res, err = LoadRepoBinpkg(&recipe.Recipe{Name: "hello", Version: "1.0.0"}, cfg)
	if err != nil || res != nil {
		t.Fatalf("ZPKG_NO_BINPKG=1 must disable the repository too: %v, %v", res, err)
	}
}

func signedRepo(t *testing.T, repo string, generation int64, expires time.Time) (trusted string) {
	t.Helper()
	keyDir := t.TempDir()
	priv := filepath.Join(keyDir, "repo.key")
	pub := filepath.Join(keyDir, "repo.pub")
	if _, err := sign.WriteKeypair(priv, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := MakeIndexWithOptions(repo, IndexOptions{
		Generation:  generation,
		Expires:     expires,
		SignKeyPath: priv,
	}); err != nil {
		t.Fatal(err)
	}
	trusted = t.TempDir()
	if _, _, err := sign.ImportPublicKey(trusted, pub); err != nil {
		t.Fatal(err)
	}
	return trusted
}

func copyPkgToRepo(t *testing.T, cache, repo string) {
	t.Helper()
	cacheBin := filepath.Join(cache, "binpkgs")
	items, err := os.ReadDir(cacheBin)
	if err != nil {
		t.Fatal(err)
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
}

func TestSignedRepo(t *testing.T) {
	cache := t.TempDir()
	r := packTo(t, cache, "")
	repo := t.TempDir()
	copyPkgToRepo(t, cache, repo)
	trusted := signedRepo(t, repo, 1, time.Now().Add(time.Hour))
	cfg := config.Config{Cache: t.TempDir(), DB: t.TempDir(), BinRepo: repo, TrustedKeys: trusted}
	res, err := LoadRepoBinpkg(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.OriginKind != "binrepo" || res.OriginKeyID == "" || res.OriginGeneration != 1 {
		t.Fatalf("result = %+v", res)
	}

	// A tampered index must be a hard error, not a source-build fallback.
	indexPath := filepath.Join(repo, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(data, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepoBinpkg(r, cfg); err == nil {
		t.Fatal("tampered index was accepted")
	}
}

func TestSignedRepoExpiry(t *testing.T) {
	cache := t.TempDir()
	r := packTo(t, cache, "")
	repo := t.TempDir()
	copyPkgToRepo(t, cache, repo)
	trusted := signedRepo(t, repo, 1, time.Now().Add(-time.Minute))
	cfg := config.Config{Cache: t.TempDir(), DB: t.TempDir(), BinRepo: repo, TrustedKeys: trusted}
	if _, err := LoadRepoBinpkg(r, cfg); err == nil {
		t.Fatal("expired index accepted")
	}
	cfg.AllowExpired = true
	if _, err := LoadRepoBinpkg(r, cfg); err != nil {
		t.Fatalf("allow expired: %v", err)
	}
}

func TestRepoLoadRejectsChannelAndDigestMismatch(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "version"), []byte("1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(recipeDir, "build")
	if err := os.WriteFile(build, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	digest, err := provenance.RecipeDigest(recipeDir)
	if err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{Name: "hello", Version: "1.0.0", Dir: recipeDir}
	res := &Result{Recipe: r, Destroot: root, Entries: entries, RecipeDigest: digest}

	cache := t.TempDir()
	if _, err := PackBinpkg(r, res, cache, "", "stable"); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	cacheBin := filepath.Join(cache, "binpkgs")
	items, err := os.ReadDir(cacheBin)
	if err != nil {
		t.Fatal(err)
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
	if _, err := MakeIndex(repo); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Cache: t.TempDir(), DB: t.TempDir(), BinRepo: repo, AllowUnsigned: true, Channel: "testing"}
	got, err := LoadRepoBinpkg(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("repository package from another channel was accepted")
	}

	cfg.Channel = "stable"
	got, err = LoadRepoBinpkg(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("same-channel repository package not restored")
	}

	// Same version, different recipe content: the index entry must no longer
	// match.
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = LoadRepoBinpkg(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("repository package with a stale recipe digest was accepted")
	}
}
