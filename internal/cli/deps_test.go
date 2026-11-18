package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func testRecipe(t *testing.T, repo, name, depends string) {
	t.Helper()
	dir := filepath.Join(repo, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(file, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("version", "1.0\n", 0o644)
	if depends != "" {
		write("depends", depends+"\n", 0o644)
	}
	write("build", "#!/bin/sh\nmkdir -p \"$ZPKG_DESTROOT/usr/bin\"\necho "+name+" > \"$ZPKG_DESTROOT/usr/bin/"+name+"\"\n", 0o755)
}

func TestReverseDependentsAndRemoveGuard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testRecipe(t, repo, "lib", "")
	testRecipe(t, repo, "app", "lib")
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_REPOS", repo)
	t.Setenv("ZPKG_CACHE", filepath.Join(root, "var", "cache", "zpkg"))
	t.Setenv("ZPKG_DB", filepath.Join(root, "var", "lib", "zpkg"))

	if err := cmdBuildInstall([]string{"app"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	deps, err := reverseDependents(cfg, db, []string{"lib"})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0] != "app" {
		t.Fatalf("reverse dependents = %v, want [app]", deps)
	}
	if err := cmdRemove([]string{"lib"}); err == nil {
		t.Fatal("remove of a required package succeeded")
	}
}

func TestVirtualTargetInstall(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testRecipe(t, repo, "provider", "")
	if err := os.WriteFile(filepath.Join(repo, "provider", "provides"), []byte("virtual/tool-1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_REPOS", repo)
	t.Setenv("ZPKG_CACHE", filepath.Join(root, "var", "cache", "zpkg"))
	t.Setenv("ZPKG_DB", filepath.Join(root, "var", "lib", "zpkg"))

	if err := cmdBuildInstall([]string{"virtual/tool"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "provider")); err != nil {
		t.Fatalf("provider not installed: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	world, err := os.ReadFile(filepath.Join(cfg.DB, "world"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(world), "\nprovider\n") {
		t.Fatalf("world = %q, want provider", world)
	}
}
