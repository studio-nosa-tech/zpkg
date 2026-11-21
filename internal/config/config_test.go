package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/provenance"
)

func TestSourceDateEpochAndBuildSettings(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "build.conf"),
		[]byte("SOURCE_DATE_EPOCH=1234567890\nBUILD_LOG=0\nSBOM=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "")
	t.Setenv("SOURCE_DATE_EPOCH", "")
	t.Setenv("ZPKG_BUILD_LOG", "")
	t.Setenv("ZPKG_SBOM", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != 1234567890 {
		t.Fatalf("SourceDateEpoch = %d", cfg.SourceDateEpoch)
	}
	if cfg.BuildLog || cfg.SBOM {
		t.Fatalf("BuildLog=%v SBOM=%v, want both false", cfg.BuildLog, cfg.SBOM)
	}

	t.Setenv("SOURCE_DATE_EPOCH", "42")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != 42 {
		t.Fatalf("SOURCE_DATE_EPOCH override = %d", cfg.SourceDateEpoch)
	}
}

func TestSourceDateEpochDefaultAndValidation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "")
	t.Setenv("SOURCE_DATE_EPOCH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != provenance.DefaultSourceDateEpoch {
		t.Fatalf("default SourceDateEpoch = %d", cfg.SourceDateEpoch)
	}
	if !cfg.BuildLog || !cfg.SBOM {
		t.Fatalf("defaults BuildLog=%v SBOM=%v, want true", cfg.BuildLog, cfg.SBOM)
	}

	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("invalid SOURCE_DATE_EPOCH accepted")
	}
}

// makeTree creates <root>/usr/share/zignos/recipes/<channel>/<category>.
func makeTree(t *testing.T, root string, channel string, cats ...string) {
	t.Helper()
	for _, cat := range cats {
		d := filepath.Join(root, "usr", "share", "zignos", "recipes", channel, cat)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func loadTestConfig(t *testing.T, root string) Config {
	t.Helper()
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_REPOS", "")
	t.Setenv("ZPKG_CHANNEL", "")
	t.Setenv("ZPKG_BUILD_LOG", "")
	t.Setenv("ZPKG_SBOM", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestChannelDefaultsAndDiscovery(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, "stable", "core", "main", "desktop")
	cfg := loadTestConfig(t, root)
	if cfg.Channel != "stable" || cfg.ChannelSource != "default" {
		t.Fatalf("channel = %q (%s)", cfg.Channel, cfg.ChannelSource)
	}
	if cfg.ChannelMissing || len(cfg.Repos) != 3 {
		t.Fatalf("repos = %v missing=%v", cfg.Repos, cfg.ChannelMissing)
	}
	if filepath.Base(filepath.Dir(cfg.Repos[0])) != "stable" {
		t.Fatalf("first repo = %s", cfg.Repos[0])
	}
	if cfg.TreeRoot != cfg.RecipeRoot {
		t.Fatalf("tree root = %q, want %q", cfg.TreeRoot, cfg.RecipeRoot)
	}
}

func TestChannelMakeConfAndEnv(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, "stable", "core", "main")
	makeTree(t, root, "testing", "core", "main")
	confDir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "make.conf"), []byte("CHANNEL=testing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadTestConfig(t, root)
	if cfg.Channel != "testing" || cfg.ChannelSource != "make.conf" {
		t.Fatalf("channel = %q (%s)", cfg.Channel, cfg.ChannelSource)
	}
	if len(cfg.Repos) != 2 || filepath.Base(filepath.Dir(cfg.Repos[0])) != "testing" {
		t.Fatalf("repos = %v", cfg.Repos)
	}

	t.Setenv("ZPKG_CHANNEL", "stable")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel != "stable" || cfg.ChannelSource != "env" {
		t.Fatalf("env channel = %q (%s)", cfg.Channel, cfg.ChannelSource)
	}
	if len(cfg.Repos) != 2 || filepath.Base(filepath.Dir(cfg.Repos[0])) != "stable" {
		t.Fatalf("env repos = %v", cfg.Repos)
	}
}

func TestChannelLegacyFallback(t *testing.T) {
	root := t.TempDir()
	for _, cat := range []string{"core", "main"} {
		if err := os.MkdirAll(filepath.Join(root, "usr", "share", "zignos", "recipes", cat), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := loadTestConfig(t, root)
	if cfg.ChannelMissing || len(cfg.Repos) != 2 {
		t.Fatalf("legacy stable: repos=%v missing=%v", cfg.Repos, cfg.ChannelMissing)
	}
	if filepath.Base(filepath.Dir(cfg.Repos[0])) != "recipes" {
		t.Fatalf("legacy repo = %s", cfg.Repos[0])
	}

	t.Setenv("ZPKG_CHANNEL", "testing")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ChannelMissing || len(cfg.Repos) != 0 {
		t.Fatalf("testing with only legacy: repos=%v missing=%v", cfg.Repos, cfg.ChannelMissing)
	}
}

func TestApplyChannelAndTreeRoot(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, "stable", "core", "main")
	makeTree(t, root, "testing", "core", "main")
	cfg := loadTestConfig(t, root)
	if err := ApplyChannel(&cfg, "testing"); err != nil {
		t.Fatal(err)
	}
	if cfg.Channel != "testing" || cfg.ChannelSource != "flag" || len(cfg.Repos) != 2 {
		t.Fatalf("apply = %+v", cfg)
	}
	if err := ApplyChannel(&cfg, "nightly"); err == nil {
		t.Fatal("invalid channel accepted")
	}

	recipes := filepath.Join(root, "usr", "share", "zignos", "recipes")
	if got := deriveTreeRoot(filepath.Join(recipes, "stable", "core")); got != recipes {
		t.Fatalf("channel tree root = %q, want %q", got, recipes)
	}
	if got := deriveTreeRoot(filepath.Join(recipes, "core")); got != recipes {
		t.Fatalf("legacy tree root = %q, want %q", got, recipes)
	}
	if got := deriveTreeRoot(filepath.Join(root, "custom", "hello")); got != filepath.Join(root, "custom") {
		t.Fatalf("custom tree root = %q", got)
	}
}

func TestGitRecipeSyncConfig(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "sync.conf"),
		[]byte("RECIPEURL=https://example.invalid/recipes\nRECIPEGIT=1\nRECIPEBRANCH=stable-branch\nRECIPEALLOWUNSIGNED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZPKG_REPO_GIT", "")
	t.Setenv("ZPKG_RECIPE_GIT", "")
	t.Setenv("ZPKG_RECIPE_BRANCH", "")
	t.Setenv("ZPKG_RECIPE_ALLOW_UNSIGNED", "")
	cfg := loadTestConfig(t, root)
	if cfg.RecipeURL != "https://example.invalid/recipes" ||
		!cfg.RecipeGit || cfg.RecipeBranch != "stable-branch" || !cfg.RecipeAllowUnsigned {
		t.Fatalf("git recipe config = %+v", cfg)
	}
	t.Setenv("ZPKG_RECIPE_GIT", "0")
	cfg = loadTestConfig(t, root)
	if cfg.RecipeGit {
		t.Fatal("ZPKG_RECIPE_GIT=0 did not override sync.conf")
	}
}
