package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

func TestPackBinpkgRoundtrip(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("usr/bin/hello", "#!/bin/sh\necho hi\n")
	if err := os.Symlink("/usr/bin/hello", filepath.Join(root, "usr/bin/hello-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	entries, err := manifest.FromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{Name: "hello", Version: "1.0.0"}
	res := &Result{Recipe: r, Destroot: root, Entries: entries}

	cache := t.TempDir()
	pkgPath, err := PackBinpkg(r, res, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "binpkgs", "hello-1.0.0.manifest")); err != nil {
		t.Fatal("sidecar manifest missing:", err)
	}

	tmp := t.TempDir()
	if err := source.Extract(pkgPath, tmp); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "usr", "bin", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("hello content mismatch: %q", data)
	}
	if _, err := os.Lstat(filepath.Join(tmp, "usr", "bin", "hello-link")); err != nil {
		t.Fatal("symlink not restored:", err)
	}
}
