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
	"strings"
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
	c := s.channels["legacy"]
	if c.core != 1 || c.main != 1 || c.desktop != 1 || s.total() != 3 || !s.legacy {
		t.Fatalf("stats = %+v, want legacy 1/1/1", s)
	}
	if err := os.MkdirAll(filepath.Join(root, "desktop", "not-a-recipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err = scanRecipeTree(root)
	if err != nil || s.channels["legacy"].desktop != 1 || s.total() != 3 {
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
	if c := stats.channels["legacy"]; c.core != 1 || c.main != 1 || stats.total() != 2 {
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
		{"./usr/share/zignos/recipes/stable/core/foo/version", "./usr/share/zignos/recipes", true},
		{"./usr/share/zignos/recipes/unstable/main/bar/version", "./usr/share/zignos/recipes", true},
		{"testing/core/foo/version", "", true},
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
	if c := stats.channels["legacy"]; c.core != 1 || c.main != 1 {
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
	if c := stats.channels["legacy"]; c.core != 1 || c.main != 1 {
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

func writeChannelRecipe(t *testing.T, root, channel, cat, name, version string) string {
	t.Helper()
	return writeRecipe(t, root, filepath.Join(channel, cat), name, version)
}

func TestScanRecipeTreeChannels(t *testing.T) {
	root := t.TempDir()
	writeChannelRecipe(t, root, "stable", "core", "foo", "1")
	writeChannelRecipe(t, root, "stable", "main", "bar", "2")
	writeChannelRecipe(t, root, "testing", "core", "foo", "2")
	writeChannelRecipe(t, root, "testing", "main", "bar", "2")
	s, err := scanRecipeTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.legacy || s.total() != 4 || s.channels["stable"].core != 1 || s.channels["testing"].core != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if !strings.Contains(s.summary(), "stable 1/1") || !strings.Contains(s.summary(), "testing 1/1") {
		t.Fatalf("summary = %q", s.summary())
	}

	// A channel missing main/ is invalid.
	bad := t.TempDir()
	writeChannelRecipe(t, bad, "stable", "core", "foo", "1")
	if _, err := scanRecipeTree(bad); err == nil {
		t.Fatal("incomplete channel accepted")
	}

	// Mixing a flat tree and channel dirs is ambiguous.
	mixed := t.TempDir()
	writeRecipe(t, mixed, "core", "foo", "1")
	writeRecipe(t, mixed, "main", "bar", "1")
	writeChannelRecipe(t, mixed, "stable", "core", "baz", "1")
	writeChannelRecipe(t, mixed, "stable", "main", "qux", "1")
	if _, err := scanRecipeTree(mixed); err == nil {
		t.Fatal("mixed layout accepted")
	}
}

func TestSyncRecipesChannels(t *testing.T) {
	src := t.TempDir()
	writeChannelRecipe(t, src, "stable", "core", "foo", "1")
	writeChannelRecipe(t, src, "stable", "main", "bar", "2")
	writeChannelRecipe(t, src, "testing", "core", "foo", "3")
	writeChannelRecipe(t, src, "testing", "main", "bar", "2")
	dest := t.TempDir()
	writeRecipe(t, dest, "core", "old", "0")
	writeRecipe(t, dest, "main", "old2", "0")

	stats, err := syncRecipes(testSyncConfig(t), src, dest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.legacy || len(stats.channels) != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dest, "stable", "core", "foo", "version")); err != nil {
		t.Fatalf("stable recipe missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "testing", "core", "foo", "version")); err != nil {
		t.Fatalf("testing recipe missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "old", "version")); !os.IsNotExist(err) {
		t.Fatalf("flat tree survived: %v", err)
	}
}

func TestSyncRecipesChannelArchive(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "recipes.tar.gz")
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
	add("./usr/share/zignos/recipes/stable/core/foo/version", "1\n", 0o644)
	add("./usr/share/zignos/recipes/stable/core/foo/build", "#!/bin/sh\n", 0o755)
	add("./usr/share/zignos/recipes/stable/main/bar/version", "2\n", 0o644)
	add("./usr/share/zignos/recipes/testing/core/foo/version", "3\n", 0o644)
	add("./usr/share/zignos/recipes/testing/main/bar/version", "2\n", 0o644)
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
	if stats.legacy || len(stats.channels) != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dest, "stable", "core", "foo", "build")); err != nil {
		t.Fatalf("channel archive recipe missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "testing", "core", "foo", "version")); err != nil {
		t.Fatalf("testing archive recipe missing: %v", err)
	}
}

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return git
}

func initGitRepo(t *testing.T, git, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "zpkg test"},
	} {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func commitAll(t *testing.T, git, dir, msg string) string {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	cmd := exec.Command(git, "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestSyncGitRecipesCompareAndUpdate(t *testing.T) {
	git := requireGit(t)
	repo := t.TempDir()
	initGitRepo(t, git, repo)
	writeRecipe(t, repo, "core", "foo", "1")
	writeRecipe(t, repo, "main", "bar", "2")
	first := commitAll(t, git, repo, "first")

	dest := t.TempDir()
	cfg := testSyncConfig(t)
	stats, err := syncGitRecipes(cfg, repo, "main", dest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.unchanged || stats.commit != first {
		t.Fatalf("stats = %+v, want changed commit %s", stats, first)
	}
	stamp, err := readGitStamp(dest)
	if err != nil || stamp.Commit != first || stamp.Branch != "main" || stamp.Remote != repo {
		t.Fatalf("stamp = %+v, %v", stamp, err)
	}

	// Same remote commit: no clone, no replacement.
	stats, err = syncGitRecipes(cfg, repo, "main", dest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.unchanged || stats.commit != first {
		t.Fatalf("second stats = %+v, want unchanged %s", stats, first)
	}

	// A new commit is fetched and replaces the tree.
	writeRecipe(t, repo, "core", "newpkg", "3")
	second := commitAll(t, git, repo, "second")
	stats, err = syncGitRecipes(cfg, repo, "main", dest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.unchanged || stats.commit != second {
		t.Fatalf("third stats = %+v, want changed commit %s", stats, second)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "newpkg", "version")); err != nil {
		t.Fatalf("new recipe missing after update: %v", err)
	}
	if got, err := readGitStamp(dest); err != nil || got.Commit != second {
		t.Fatalf("updated stamp = %+v, %v", got, err)
	}
}

func TestSyncGitRecipesDryRunDoesNotTouchTree(t *testing.T) {
	git := requireGit(t)
	repo := t.TempDir()
	initGitRepo(t, git, repo)
	writeRecipe(t, repo, "core", "foo", "1")
	writeRecipe(t, repo, "main", "bar", "2")
	commitAll(t, git, repo, "first")

	dest := t.TempDir()
	writeRecipe(t, dest, "core", "old", "0")
	if _, err := syncGitRecipes(testSyncConfig(t), repo, "main", dest, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "core", "old", "version")); err != nil {
		t.Fatalf("dry run replaced the tree: %v", err)
	}
}

func TestParseGitSyncArgs(t *testing.T) {
	o, err := parseSyncArgs([]string{"--git", "--branch", "dev", "--from", "https://example.invalid/r", "--allow-unsigned"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.git || o.branch != "dev" || o.from != "https://example.invalid/r" || !o.allowUnsigned {
		t.Fatalf("options = %+v", o)
	}
	if !isGitSource("https://example.invalid/r.git") || !isGitSource("git@example.invalid:r") || isGitSource("https://example.invalid/r.tar.xz") {
		t.Fatal("isGitSource detection is wrong")
	}
}
