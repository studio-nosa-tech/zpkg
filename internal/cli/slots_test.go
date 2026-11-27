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

func testSlotRecipe(t *testing.T, repo, rel, version, file string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"version": version + "\n",
		"build": "#!/bin/sh\nmkdir -p \"$ZPKG_DESTROOT/usr/bin\"\n" +
			"echo " + file + " > \"$ZPKG_DESTROOT/usr/bin/" + file + "\"\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func slotTestEnv(t *testing.T, root, repo string) {
	t.Helper()
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_REPOS", repo)
	t.Setenv("ZPKG_CACHE", filepath.Join(root, "var", "cache", "zpkg"))
	t.Setenv("ZPKG_DB", filepath.Join(root, "var", "lib", "zpkg"))
}

func TestSlotInstallWorldAndRemove(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "tool@1", "1.0", "tool-one", nil)
	testSlotRecipe(t, repo, "tool@14", "2.0", "tool-fourteen", nil)
	slotTestEnv(t, root, repo)

	if err := cmdBuildInstall([]string{"tool@1", "tool@14"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	refs, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].String() != "tool@1" || refs[1].String() != "tool@14" {
		t.Fatalf("installed refs = %v", refs)
	}
	worldData, err := os.ReadFile(filepath.Join(cfg.DB, "world"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worldData), "tool@1") || !strings.Contains(string(worldData), "tool@14") {
		t.Fatalf("world = %q", worldData)
	}
	for _, f := range []string{"tool-one", "tool-fourteen"} {
		if _, err := os.Stat(filepath.Join(root, "usr", "bin", f)); err != nil {
			t.Fatalf("%s not installed: %v", f, err)
		}
	}

	if err := cmdRemove([]string{"tool@14"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("tool", "14"); err == nil {
		t.Fatal("tool@14 record survived removal")
	}
	if _, err := db.Get("tool", "1"); err != nil {
		t.Fatalf("tool@1 was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "tool-one")); err != nil {
		t.Fatalf("slot 1 file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "tool-fourteen")); !os.IsNotExist(err) {
		t.Fatal("slot 14 file survived removal")
	}
	worldData, err = os.ReadFile(filepath.Join(cfg.DB, "world"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worldData), "tool@14") {
		t.Fatalf("world still contains tool@14: %q", worldData)
	}

	// Removing the bare name removes every remaining slot.
	if err := cmdRemove([]string{"tool"}); err != nil {
		t.Fatal(err)
	}
	refs, err = db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs after remove-all = %v", refs)
	}
}

func TestInstallUsesSelectedVersionFromVersionsDir(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "lib/versions/1.0", "1.0", "lib-one", nil)
	testSlotRecipe(t, repo, "lib/versions/2.0", "2.0", "lib-two", nil)
	testSlotRecipe(t, repo, "app", "1.0", "app", map[string]string{"depends": "=lib-1.0\n"})
	slotTestEnv(t, root, repo)

	if err := cmdBuildInstall([]string{"app"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	rec, err := db.Get("lib", "0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.VersionString() != "1.0" {
		t.Fatalf("installed lib = %s, want 1.0 (the version satisfying =lib-1.0)", rec.VersionString())
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "lib-one")); err != nil {
		t.Fatalf("lib 1.0 file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "lib-two")); !os.IsNotExist(err) {
		t.Fatal("install built a different version than the resolver selected")
	}
}

func TestInstallUpgradesDependencyToSatisfyConstraint(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "lib/versions/1.0", "1.0", "lib-one", nil)
	testSlotRecipe(t, repo, "lib/versions/2.0", "2.0", "lib-two", nil)
	slotTestEnv(t, root, repo)

	// Install the old lib explicitly, then require the new one from app.
	if err := cmdBuildInstall([]string{"=lib-1.0"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	rec, err := db.Get("lib", "0")
	if err != nil || rec.VersionString() != "1.0" {
		t.Fatalf("initial lib = %+v, %v", rec, err)
	}

	testSlotRecipe(t, repo, "app", "1.0", "app", map[string]string{"depends": ">=lib-2.0\n"})
	if err := cmdBuildInstall([]string{"app"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	rec, err = db.Get("lib", "0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.VersionString() != "2.0" {
		t.Fatalf("lib = %s, want 2.0 to satisfy app", rec.VersionString())
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "app")); err != nil {
		t.Fatalf("app not installed: %v", err)
	}
}

func TestUpgradeSlotKeepsOtherSlots(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "tool@1/versions/1.0", "1.0", "tool-one", nil)
	testSlotRecipe(t, repo, "tool@14/versions/1.0", "1.0", "tool-fourteen-one", nil)
	testSlotRecipe(t, repo, "tool@14/versions/2.0", "2.0", "tool-fourteen-two", nil)
	slotTestEnv(t, root, repo)

	if err := cmdBuildInstall([]string{"=tool@1-1.0", "=tool@14-1.0"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	if rec, err := db.Get("tool", "14"); err != nil || rec.VersionString() != "1.0" {
		t.Fatalf("initial tool@14 = %+v, %v", rec, err)
	}

	if err := cmdUpgrade([]string{"--yes", "--no-sandbox"}); err != nil {
		t.Fatal(err)
	}
	if rec, err := db.Get("tool", "1"); err != nil || rec.VersionString() != "1.0" {
		t.Fatalf("tool@1 changed: %+v, %v", rec, err)
	}
	if rec, err := db.Get("tool", "14"); err != nil || rec.VersionString() != "2.0" {
		t.Fatalf("tool@14 = %+v, %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "tool-one")); err != nil {
		t.Fatalf("slot 1 file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "tool-fourteen-one")); !os.IsNotExist(err) {
		t.Fatal("old slot 14 file survived the upgrade")
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "tool-fourteen-two")); err != nil {
		t.Fatalf("new slot 14 file missing: %v", err)
	}
}

func TestUpgradeUsesPlannedCandidates(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "lib/versions/1.0", "1.0", "lib-one", nil)
	testSlotRecipe(t, repo, "lib/versions/2.0", "2.0", "lib-two", nil)
	testSlotRecipe(t, repo, "app/versions/1.0", "1.0", "app-one", map[string]string{"depends": "=lib-1.0\n"})
	testSlotRecipe(t, repo, "app/versions/2.0", "2.0", "app-two", map[string]string{"depends": ">=lib-2.0\n"})
	slotTestEnv(t, root, repo)

	if err := cmdBuildInstall([]string{"=app-1.0"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for name, want := range map[string]string{"app": "1.0", "lib": "1.0"} {
		rec, err := db.Get(name, "0")
		if err != nil || rec.VersionString() != want {
			t.Fatalf("initial %s = %+v, %v", name, rec, err)
		}
	}

	if err := cmdUpgrade([]string{"--yes", "--no-sandbox"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"app": "2.0", "lib": "2.0"} {
		rec, err := db.Get(name, "0")
		if err != nil || rec.VersionString() != want {
			t.Fatalf("upgraded %s = %+v, %v", name, rec, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "app-two")); err != nil {
		t.Fatalf("app 2.0 file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "app-one")); !os.IsNotExist(err) {
		t.Fatal("app 1.0 file survived the upgrade")
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "lib-two")); err != nil {
		t.Fatalf("lib 2.0 file missing: %v", err)
	}
}
