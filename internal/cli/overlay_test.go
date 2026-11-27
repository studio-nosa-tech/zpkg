package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/overlay"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
)

func requireBuildShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func writeOverlayMeta(t *testing.T, dir, name string, categories ...string) {
	t.Helper()
	body := "NAME=" + name + "\nDESCRIPTION=Test overlay\n"
	if len(categories) > 0 {
		body += "CATEGORIES=" + strings.Join(categories, " ") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, overlay.MetaFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOverlayLocalDirLifecycle(t *testing.T) {
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "base", "1.0", "base", nil)
	slotTestEnv(t, root, repo)

	src := t.TempDir()
	writeOverlayMeta(t, src, "Test Menu", "apps", "tools")
	testSlotRecipe(t, src, "apps/hello", "1.0", "hello", nil)
	testSlotRecipe(t, src, "tools/base", "2.0", "base-overlay", nil)

	if err := cmdOverlay([]string{"add", "testmenu", src, "--allow-unsigned", "--priority", "5"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != 1 {
		t.Fatalf("overlays = %d, want 1", len(cfg.Overlays))
	}
	ov := cfg.Overlays[0]
	if !ov.Active || !ov.Present || ov.Err != "" {
		t.Fatalf("overlay state = %+v", ov)
	}
	if ov.Priority != 5 {
		t.Fatalf("priority = %d, want 5", ov.Priority)
	}
	if strings.Join(ov.Meta.Categories, ",") != "apps,tools" {
		t.Fatalf("categories = %v", ov.Meta.Categories)
	}
	wantRepos := []string{
		filepath.Join(ov.Dir, "apps"),
		filepath.Join(ov.Dir, "tools"),
		repo,
	}
	if len(cfg.Repos) != len(wantRepos) {
		t.Fatalf("repos = %v, want %v", cfg.Repos, wantRepos)
	}
	for i := range wantRepos {
		if cfg.Repos[i] != wantRepos[i] {
			t.Fatalf("repos[%d] = %q, want %q", i, cfg.Repos[i], wantRepos[i])
		}
	}
	if got := cfg.RepoLabel(cfg.Repos[0]); got != "testmenu/apps" {
		t.Fatalf("overlay label = %q, want testmenu/apps", got)
	}
	if got := cfg.RepoLabel(repo); got != filepath.Base(repo) {
		t.Fatalf("base label = %q, want %q", got, filepath.Base(repo))
	}
	if got := cfg.OverlayName(cfg.Repos[1]); got != "testmenu" {
		t.Fatalf("OverlayName = %q", got)
	}
	if got := cfg.OverlayName(repo); got != "" {
		t.Fatalf("base repo reported overlay %q", got)
	}

	// A sync by name refreshes the tree without touching the config.
	if err := cmdOverlay([]string{"sync", "testmenu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ov.Dir, "apps", "hello", "version")); err != nil {
		t.Fatalf("synced tree missing: %v", err)
	}
	if err := cmdOverlay([]string{"list"}); err != nil {
		t.Fatal(err)
	}

	// Removing it drops the config and the tree, and the base repos remain.
	if err := cmdOverlay([]string{"remove", "testmenu"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != 0 {
		t.Fatalf("overlays after remove = %d", len(cfg.Overlays))
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != repo {
		t.Fatalf("repos after remove = %v", cfg.Repos)
	}
	if _, err := os.Stat(ov.Dir); !os.IsNotExist(err) {
		t.Fatal("overlay tree survived removal")
	}

	// --no-sync records a spec whose tree is absent; config.Load must still
	// succeed and mark it inactive instead of failing every command.
	if err := cmdOverlay([]string{"add", "testmenu", src, "--allow-unsigned", "--no-sync"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != 1 || cfg.Overlays[0].Active || cfg.Overlays[0].Err == "" {
		t.Fatalf("not-synced overlay state = %+v", cfg.Overlays)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("inactive overlay leaked into repos: %v", cfg.Repos)
	}
	if err := cmdOverlay([]string{"remove", "testmenu"}); err != nil {
		t.Fatal(err)
	}
}

func TestOverlayOverrideAndInstallMetadata(t *testing.T) {
	requireBuildShell(t)
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "base", "1.0", "base-one", nil)
	slotTestEnv(t, root, repo)

	src := t.TempDir()
	writeOverlayMeta(t, src, "Test Menu", "tools")
	testSlotRecipe(t, src, "tools/base", "2.0", "base-two", nil)
	if err := cmdOverlay([]string{"add", "testmenu", src, "--allow-unsigned"}); err != nil {
		t.Fatal(err)
	}

	if err := cmdBuildInstall([]string{"base"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	rec, err := db.Get("base", "0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.VersionString() != "2.0" {
		t.Fatalf("installed %s, want the overlay's 2.0", rec.VersionString())
	}
	if rec.Meta == nil || rec.Meta.Overlay != "testmenu" {
		t.Fatalf("meta.Overlay = %+v", rec.Meta)
	}
	if got := installedRepoLabel(cfg, rec.Meta); got != "testmenu/tools" {
		t.Fatalf("installed repo label = %q", got)
	}
	info, err := provenance.Read(filepath.Join(cfg.DB, "base", "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Recipe.Overlay != "testmenu" {
		t.Fatalf("provenance overlay = %q", info.Recipe.Overlay)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "base-two")); err != nil {
		t.Fatalf("overlay build output missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "base-one")); !os.IsNotExist(err) {
		t.Fatal("base recipe was built instead of the overlay override")
	}

	// While the overlay is active the package is up to date.
	if err := cmdUpgrade([]string{"--yes", "--no-sandbox"}); err != nil {
		t.Fatal(err)
	}
	if rec, err := db.Get("base", "0"); err != nil || rec.VersionString() != "2.0" {
		t.Fatalf("after upgrade = %+v, %v", rec, err)
	}
}

func TestOverlayRemovedPackageSurvivesUpgrade(t *testing.T) {
	requireBuildShell(t)
	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "base", "1.0", "base", nil)
	slotTestEnv(t, root, repo)

	src := t.TempDir()
	writeOverlayMeta(t, src, "Test Menu", "apps")
	testSlotRecipe(t, src, "apps/hello", "1.0", "hello", nil)
	if err := cmdOverlay([]string{"add", "testmenu", src, "--allow-unsigned"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdBuildInstall([]string{"hello"}, true, installOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db := pkgdb.DB{Dir: cfg.DB}
	before, err := db.Get("hello", "0")
	if err != nil {
		t.Fatal(err)
	}

	if err := cmdOverlay([]string{"remove", "testmenu"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// The removed overlay no longer labels its files, but the record still
	// renders its origin.
	if got := installedRepoLabel(cfg, before.Meta); got != "testmenu/apps" {
		t.Fatalf("label after removal = %q", got)
	}
	// The installed-only candidate keeps the package in the closure.
	if err := cmdUpgrade([]string{"--yes", "--no-sandbox"}); err != nil {
		t.Fatal(err)
	}
	after, err := db.Get("hello", "0")
	if err != nil {
		t.Fatalf("hello was removed by the upgrade: %v", err)
	}
	if after.VersionString() != before.VersionString() {
		t.Fatalf("hello changed: %s -> %s", before.VersionString(), after.VersionString())
	}
	if _, err := os.Stat(filepath.Join(root, "usr", "bin", "hello")); err != nil {
		t.Fatalf("hello file missing: %v", err)
	}
}

func TestOverlaySignatureChecks(t *testing.T) {
	src := t.TempDir()
	writeOverlayMeta(t, src, "Signed Menu", "apps")
	testSlotRecipe(t, src, "apps/signed", "1.0", "signed-one", nil)
	trusted, priv := signTree(t, src, 1, time.Now().Add(time.Hour))

	root, repo := t.TempDir(), t.TempDir()
	testSlotRecipe(t, repo, "base", "1.0", "base", nil)
	slotTestEnv(t, root, repo)
	t.Setenv("ZPKG_TRUSTED_KEYS", trusted)

	if err := cmdOverlay([]string{"add", "signedmenu", src}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != 1 || !cfg.Overlays[0].Active {
		t.Fatalf("signed overlay state = %+v", cfg.Overlays)
	}
	if signed, generation, _ := overlaySignature(cfg.Overlays[0].Dir); !signed || generation != 1 {
		t.Fatalf("synced signature = %v gen %d", signed, generation)
	}

	// A newer generation replaces the tree.
	build := filepath.Join(src, "apps", "signed", "build")
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := recipesig.Sign(src, priv, 2, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cmdOverlay([]string{"sync", "signedmenu"}); err != nil {
		t.Fatal(err)
	}
	destBuild := filepath.Join(cfg.Overlays[0].Dir, "apps", "signed", "build")
	if data, err := os.ReadFile(destBuild); err != nil || string(data) != "#!/bin/sh\necho two\n" {
		t.Fatalf("synced build = %q, %v", data, err)
	}

	// Rolling back to an older generation is rejected and changes nothing.
	if _, err := recipesig.Sign(src, priv, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cmdOverlay([]string{"sync", "signedmenu"}); err == nil {
		t.Fatal("generation rollback was accepted")
	}
	if data, err := os.ReadFile(destBuild); err != nil || string(data) != "#!/bin/sh\necho two\n" {
		t.Fatalf("rejected rollback changed the tree: %q, %v", data, err)
	}

	// Tampering after signing is rejected before the swap.
	if _, err := recipesig.Sign(src, priv, 3, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(build, []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cmdOverlay([]string{"sync", "signedmenu"}); err == nil {
		t.Fatal("tampered overlay was accepted")
	}
	if data, err := os.ReadFile(destBuild); err != nil || string(data) != "#!/bin/sh\necho two\n" {
		t.Fatalf("rejected tamper changed the tree: %q, %v", data, err)
	}

	// Unsigned and invalid signatures are refused unless allowed, and
	// --allow-unsigned never accepts a bad signature.
	unsigned := t.TempDir()
	writeOverlayMeta(t, unsigned, "Unsigned", "apps")
	testSlotRecipe(t, unsigned, "apps/unsigned", "1.0", "unsigned", nil)
	if err := cmdOverlay([]string{"add", "unsignedmenu", unsigned}); err == nil {
		t.Fatal("unsigned overlay was accepted without --allow-unsigned")
	}
	if cfg, err := config.Load(); err != nil || len(cfg.Overlays) != 1 {
		t.Fatalf("failed add left config behind: %+v, %v", cfg.Overlays, err)
	}

	bad := t.TempDir()
	writeOverlayMeta(t, bad, "Bad", "apps")
	testSlotRecipe(t, bad, "apps/bad", "1.0", "bad", nil)
	if _, err := recipesig.Sign(bad, priv, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, recipesig.SignatureName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdOverlay([]string{"add", "badmenu", bad, "--allow-unsigned"}); err == nil {
		t.Fatal("invalid signature accepted with --allow-unsigned")
	}
}
