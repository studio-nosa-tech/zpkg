package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
)

func testSyncConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Cache:         t.TempDir(),
		DB:            t.TempDir(),
		TrustedKeys:   t.TempDir(),
		AllowUnsigned: true,
	}
}

func writeRecipe(t *testing.T, root, cat, name, version string) string {
	t.Helper()
	dir := filepath.Join(root, cat, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFindRecipeTree(t *testing.T) {
	tree := t.TempDir()
	writeRecipe(t, tree, "core", "foo", "1")
	writeRecipe(t, tree, "main", "bar", "2")
	if got, err := findRecipeTree(tree); err != nil || got != tree {
		t.Fatalf("findRecipeTree(tree) = %q, %v", got, err)
	}

	stage3 := t.TempDir()
	inner := filepath.Join(stage3, "usr", "share", "zignos", "recipes")
	writeRecipe(t, inner, "core", "foo", "1")
	writeRecipe(t, inner, "main", "bar", "2")
	if got, err := findRecipeTree(stage3); err != nil || got != inner {
		t.Fatalf("findRecipeTree(stage3) = %q, %v", got, err)
	}

	bad := t.TempDir()
	if _, err := findRecipeTree(bad); err == nil {
		t.Fatal("findRecipeTree(empty) succeeded, want error")
	}
}

func TestScanRecipeTree(t *testing.T) {
	root := t.TempDir()
	writeRecipe(t, root, "core", "foo", "1")
	writeRecipe(t, root, "main", "bar", "2")
	writeRecipe(t, root, "desktop", "baz", "3")
	s, err := scanRecipeTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.core != 1 || s.main != 1 || s.desktop != 1 || s.total() != 3 {
		t.Fatalf("stats = %+v, want 1/1/1", s)
	}
	if err := os.MkdirAll(filepath.Join(root, "desktop", "not-a-recipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err = scanRecipeTree(root)
	if err != nil || s.desktop != 1 || s.total() != 3 {
		t.Fatalf("stats with non-recipe dir = %+v, %v", s, err)
	}
	if _, err := scanRecipeTree(filepath.Join(root, "core")); err == nil {
		t.Fatal("scanRecipeTree(core) succeeded, want error")
	}
}

func TestSyncRecipesDirectory(t *testing.T) {
	src := t.TempDir()
	foo := writeRecipe(t, src, "core", "foo", "1")
	writeRecipe(t, src, "main", "bar", "2")
	dest := t.TempDir()
	writeRecipe(t, dest, "core", "old", "0")

	stats, err := syncRecipes(testSyncConfig(t), src, dest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.core != 1 || stats.main != 1 || stats.total() != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "foo", "version")); err != nil {
		t.Fatalf("new recipe missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "old", "version")); !os.IsNotExist(err) {
		t.Fatalf("old recipe survived: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "core", "foo", "build"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("build mode %v lost executable bit", info.Mode())
	}
	_ = foo

	// A dry run must validate without touching the target.
	before, err := os.ReadFile(filepath.Join(dest, "core", "foo", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncRecipes(testSyncConfig(t), src, dest, false, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dest, "core", "foo", "version"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("dry run changed target: %q, %v", after, err)
	}
}

func TestSyncRecipesInvalidLeavesTarget(t *testing.T) {
	src := t.TempDir()
	writeRecipe(t, src, "core", "foo", "1") // no main
	dest := t.TempDir()
	writeRecipe(t, dest, "core", "old", "0")
	if _, err := syncRecipes(testSyncConfig(t), src, dest, false, false); err == nil {
		t.Fatal("sync of incomplete tree succeeded")
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "old", "version")); err != nil {
		t.Fatalf("target was damaged by failed sync: %v", err)
	}
}

func TestRecipePrefix(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		ok     bool
	}{
		{"core/foo/version", "", true},
		{"./core/foo/version", "", true},
		{"./usr/share/zignos/recipes/core/foo/version", "./usr/share/zignos/recipes", true},
		{"repo-main/main/bar/version", "repo-main", true},
		{"usr/share/zignos/recipes/README", "", false},
	}
	for _, c := range cases {
		prefix, ok := recipePrefix(c.name)
		if prefix != c.prefix || ok != c.ok {
			t.Errorf("recipePrefix(%q) = %q, %v; want %q, %v", c.name, prefix, ok, c.prefix, c.ok)
		}
	}
}

func TestSyncRecipesStage3Archive(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "stage3.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("./usr/share/zignos/recipes/core/foo/version", "1\n", 0o644)
	add("./usr/share/zignos/recipes/core/foo/build", "#!/bin/sh\n", 0o755)
	add("./usr/share/zignos/recipes/main/bar/version", "2\n", 0o644)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	stats, err := syncRecipes(testSyncConfig(t), archive, dest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.core != 1 || stats.main != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "foo", "build")); err != nil {
		t.Fatalf("archive recipe missing: %v", err)
	}
}

func TestSyncRecipesURL(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("./core/foo/version", "1\n", 0o644)
	add("./main/bar/version", "2\n", 0o644)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	dest := t.TempDir()
	stats, err := syncRecipes(testSyncConfig(t), srv.URL+"/recipes.tar.gz", dest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.core != 1 || stats.main != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dest, "main", "bar", "version")); err != nil {
		t.Fatalf("URL recipe missing: %v", err)
	}
}

func signTree(t *testing.T, root string, generation int64, expires time.Time) (trusted, priv string) {
	t.Helper()
	keyDir := t.TempDir()
	priv = filepath.Join(keyDir, "recipes.key")
	pub := filepath.Join(keyDir, "recipes.pub")
	if _, err := sign.WriteKeypair(priv, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := recipesig.Sign(root, priv, generation, expires); err != nil {
		t.Fatal(err)
	}
	trusted = t.TempDir()
	if _, _, err := sign.ImportPublicKey(trusted, pub); err != nil {
		t.Fatal(err)
	}
	return trusted, priv
}

func TestSyncRecipesSignedAndRollback(t *testing.T) {
	src := t.TempDir()
	writeRecipe(t, src, "core", "foo", "1")
	writeRecipe(t, src, "main", "bar", "2")
	trusted, priv := signTree(t, src, 1, time.Now().Add(time.Hour))
	dest := t.TempDir()
	writeRecipe(t, dest, "core", "old", "0")

	cfg := config.Config{
		Cache:       t.TempDir(),
		DB:          t.TempDir(),
		TrustedKeys: trusted,
	}
	if _, err := syncRecipes(cfg, src, dest, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "foo", "build")); err != nil {
		t.Fatalf("signed tree not installed: %v", err)
	}

	// A newer generation is accepted.
	if err := os.WriteFile(filepath.Join(src, "core", "foo", "build"), []byte("#!/bin/sh\necho 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := recipesig.Sign(src, priv, 2, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRecipes(cfg, src, dest, false, false); err != nil {
		t.Fatal(err)
	}

	// Re-signing an older generation must be rejected as a rollback.
	if _, err := recipesig.Sign(src, priv, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRecipes(cfg, src, dest, false, false); err == nil {
		t.Fatal("rollback to generation 1 was accepted")
	}
	// The rejected tree must not have replaced the target.
	data, err := os.ReadFile(filepath.Join(dest, "core", "foo", "build"))
	if err != nil || string(data) != "#!/bin/sh\necho 2\n" {
		t.Fatalf("target changed by rejected sync: %q, %v", data, err)
	}

	// Tampering after signing is rejected before the swap.
	if _, err := recipesig.Sign(src, priv, 3, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "core", "foo", "build"), []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRecipes(cfg, src, dest, false, false); err == nil {
		t.Fatal("tampered recipe tree was accepted")
	}

	// Unsigned trees are rejected unless explicitly allowed, and an invalid
	// signature is never accepted by --allow-unsigned.
	unsigned := t.TempDir()
	writeRecipe(t, unsigned, "core", "foo", "1")
	writeRecipe(t, unsigned, "main", "bar", "2")
	if _, err := syncRecipes(cfg, unsigned, dest, false, false); err == nil {
		t.Fatal("unsigned tree accepted without --allow-unsigned")
	}
	if _, err := syncRecipes(cfg, unsigned, dest, true, true); err != nil {
		t.Fatalf("unsigned tree with --allow-unsigned: %v", err)
	}
	bad := t.TempDir()
	writeRecipe(t, bad, "core", "foo", "1")
	writeRecipe(t, bad, "main", "bar", "2")
	if _, err := recipesig.Sign(bad, priv, 4, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, recipesig.SignatureName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncRecipes(cfg, bad, dest, true, true); err == nil {
		t.Fatal("invalid signature accepted with --allow-unsigned")
	}
}
