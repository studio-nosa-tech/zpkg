package build

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
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
	pkgPath, err := PackBinpkg(r, res, cache, "", "")
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

func TestPackBinpkgNormalizesTimestamps(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "usr", "share", "hello")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := manifest.FromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{Name: "hello", Version: "1.0.0"}
	res := &Result{Recipe: r, Destroot: root, Entries: entries, SourceDateEpoch: 946684800}

	cache := t.TempDir()
	pkgPath, err := PackBinpkg(r, res, cache, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(946684800, 0).UTC()
	f, err := os.Open(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if !gz.Header.ModTime.Equal(want) {
		t.Fatalf("gzip mtime = %v, want %v", gz.Header.ModTime, want)
	}
	tr := tar.NewReader(gz)
	seen := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen++
		if !hdr.ModTime.Equal(want) {
			t.Errorf("%s: mtime = %v, want %v", hdr.Name, hdr.ModTime, want)
		}
		if !hdr.AccessTime.IsZero() || !hdr.ChangeTime.IsZero() {
			t.Errorf("%s: access/change time not zeroed", hdr.Name)
		}
	}
	if seen == 0 {
		t.Fatal("empty archive")
	}
	if res.ArtifactPath != pkgPath || res.ArtifactSize == 0 || len(res.ArtifactSHA256) != 64 {
		t.Fatalf("artifact provenance = %+v", res)
	}
	sum, err := hash.File(pkgPath)
	if err != nil || sum != res.ArtifactSHA256 {
		t.Fatalf("artifact sha256 = %q, want %q (%v)", res.ArtifactSHA256, sum, err)
	}
	manSum, err := hash.File(filepath.Join(cache, "binpkgs", "hello-1.0.0.manifest"))
	if err != nil || manSum != res.ManifestSHA256 {
		t.Fatalf("manifest sha256 = %q, want %q (%v)", res.ManifestSHA256, manSum, err)
	}
}

func TestLoadBinpkgRejectsOtherChannel(t *testing.T) {
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
	r := &recipe.Recipe{Name: "hello", Version: "1.0.0"}
	res := &Result{Recipe: r, Destroot: root, Entries: entries}

	cache := t.TempDir()
	if _, err := PackBinpkg(r, res, cache, "", "testing"); err != nil {
		t.Fatal(err)
	}
	if got := LoadBinpkg(r, config.Config{Cache: cache, Channel: "testing"}); got == nil {
		t.Fatal("binpkg not restored for its own channel")
	}
	if got := LoadBinpkg(r, config.Config{Cache: cache, Channel: "unstable"}); got != nil {
		t.Fatal("binpkg reused across channels")
	}

	// Entries created before the channel field existed belong to stable.
	cache2 := t.TempDir()
	if _, err := PackBinpkg(r, res, cache2, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := LoadBinpkg(r, config.Config{Cache: cache2, Channel: "stable"}); got == nil {
		t.Fatal("legacy binpkg not restored for stable")
	}
	if got := LoadBinpkg(r, config.Config{Cache: cache2, Channel: "testing"}); got != nil {
		t.Fatal("legacy binpkg reused for a non-stable channel")
	}
}

func TestLoadBinpkgRejectsRecipeDigestMismatch(t *testing.T) {
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
	cfg := config.Config{Cache: cache, Channel: "stable"}
	if got := LoadBinpkg(r, cfg); got == nil {
		t.Fatal("binpkg not restored for matching recipe")
	} else if got.RecipeDigest != digest {
		t.Fatalf("restored digest = %q, want %q", got.RecipeDigest, digest)
	}

	// Same version, different recipe content: the cached binpkg must not be
	// reused (this is the channel-backport case).
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := LoadBinpkg(r, cfg); got != nil {
		t.Fatal("binpkg reused after recipe content changed")
	}
}
