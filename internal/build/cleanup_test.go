package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

func TestCleanOldWork(t *testing.T) {
	cache := t.TempDir()
	cfg := config.Config{Cache: cache}
	r := &recipe.Recipe{Name: "foo", Version: "2.0"}

	for _, name := range []string{"foo-1.0", "foo-2.0", "bar-1.0"} {
		dir := filepath.Join(cache, "build", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, workPackageMarker), []byte(name[:3]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanOldWork(r, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "build", "foo-1.0")); !os.IsNotExist(err) {
		t.Fatal("old foo workdir was not removed")
	}
	for _, name := range []string{"foo-2.0", "bar-1.0"} {
		if _, err := os.Stat(filepath.Join(cache, "build", name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}

func TestCleanOldWorkIgnoresUnknownDirs(t *testing.T) {
	cache := t.TempDir()
	cfg := config.Config{Cache: cache}
	r := &recipe.Recipe{Name: "foo", Version: "2.0"}

	// A work dir from an older zpkg has no marker file. It cannot be
	// attributed to a package safely, so it must be left alone.
	old := filepath.Join(cache, "build", "foo-1.0")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CleanOldWork(r, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("unmarked workdir was removed: %v", err)
	}
}

func TestCleanOldBinpkgs(t *testing.T) {
	cache := t.TempDir()
	cfg := config.Config{Cache: cache}
	r := &recipe.Recipe{Name: "foo", Version: "2.0"}
	dir := filepath.Join(cache, "binpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"foo-1.0", "foo-2.0", "bar-1.0"} {
		if err := os.WriteFile(filepath.Join(dir, base+".meta.json"), []byte(`{"name":"`+base[:3]+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{".pkg.tar.gz", ".manifest", ".build.json", ".spdx.json"} {
			if err := os.WriteFile(filepath.Join(dir, base+suffix), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := CleanOldBinpkgs(r, cfg); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".pkg.tar.gz", ".manifest", ".build.json", ".spdx.json", ".meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, "foo-1.0"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("old foo binpkg %s was not removed", suffix)
		}
	}
	for _, base := range []string{"foo-2.0", "bar-1.0"} {
		if _, err := os.Stat(filepath.Join(dir, base+".pkg.tar.gz")); err != nil {
			t.Fatalf("%s was removed: %v", base, err)
		}
	}
}

func TestRemoveSourceArchivesKeepsPatches(t *testing.T) {
	cache := t.TempDir()
	for _, name := range []string{"foo-1.0.tar.xz", "fix.patch"} {
		if err := os.WriteFile(filepath.Join(cache, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := &Result{Sources: []provenance.Source{
		{File: "foo-1.0.tar.xz"},
		{File: "fix.patch"},
	}}
	if err := RemoveSourceArchives(res, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "foo-1.0.tar.xz")); !os.IsNotExist(err) {
		t.Fatal("source archive was not removed")
	}
	if _, err := os.Stat(filepath.Join(cache, "fix.patch")); err != nil {
		t.Fatal("patch was removed")
	}
}
