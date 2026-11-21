// Package config resolves zpkg runtime paths from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/sandbox"
)

// Config holds the runtime layout.
type Config struct {
	Repos               []string        // recipe repos, in search order (first match wins)
	Root                string          // target system root, absolute
	Cache               string          // source downloads and build workdirs
	DB                  string          // installed package database
	Options             options.Options // USE flags and build options for Root
	BinRepo             string          // binary package repository (URL or local dir), "" = none
	RecipeURL           string          // default source for "zpkg sync" (URL or path), "" = none
	RecipeGit           bool            // fetch RecipeURL as a git repository
	RecipeBranch        string          // git branch for RecipeURL (default "main")
	RecipeAllowUnsigned bool            // allow an unsigned tree from the git recipe source

	TrustedKeys   string // directory of trusted *.pub signing keys
	AllowUnsigned bool   // accept unsigned repository/recipe metadata (development)
	AllowExpired  bool   // ignore signature expiry (recovery)

	SandboxMode  string // auto | require | off
	BuildNetwork bool   // allow network access during builds by default
	BwrapPath    string // bubblewrap executable (default "bwrap")

	SourceDateEpoch int64 // fixed timestamp for reproducible artifacts
	BuildLog        bool  // persist build logs under $ZPKG_CACHE/logs
	SBOM            bool  // write SPDX SBOMs next to artifacts and DB records

	// Release channel selection. Channel is the effective channel; Source
	// says where it came from ("env", "make.conf", "flag", "default").
	Channel        string
	ChannelSource  string
	ReposExplicit  bool   // ZPKG_REPOS was set, so channel does not pick repos
	RecipeRoot     string // $ZPKG_ROOT/usr/share/zignos/recipes
	TreeRoot       string // tree that owns recipes.manifest (provenance)
	ChannelMissing bool   // the selected channel is not present under RecipeRoot
}

// Channels are the supported release channels, most stable first.
var Channels = []string{"stable", "testing", "unstable"}

// ValidChannel reports whether ch is a supported release channel.
func ValidChannel(ch string) bool {
	for _, c := range Channels {
		if ch == c {
			return true
		}
	}
	return false
}

// AvailableChannels lists the channels installed under a recipe root.
func AvailableChannels(recipeRoot string) []string {
	var out []string
	for _, c := range Channels {
		if isDir(filepath.Join(recipeRoot, c, "core")) {
			out = append(out, c)
		}
	}
	return out
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isCategory(s string) bool {
	return s == "core" || s == "main" || s == "desktop"
}

// Load builds a Config from the ZPKG_* environment variables.
func Load() (Config, error) {
	root := os.Getenv("ZPKG_ROOT")
	if root == "" {
		root = "/"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}
	reposEnv := os.Getenv("ZPKG_REPOS")
	cfg := Config{
		Repos:         splitRepos(reposEnv),
		ReposExplicit: reposEnv != "",
		Root:          absRoot,
		Cache:         os.Getenv("ZPKG_CACHE"),
		DB:            os.Getenv("ZPKG_DB"),
		RecipeRoot:    filepath.Join(absRoot, "usr", "share", "zignos", "recipes"),
	}
	if cfg.Cache == "" {
		cfg.Cache = filepath.Join(absRoot, "var", "cache", "zpkg")
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(absRoot, "var", "lib", "zpkg")
	}
	if err := loadChannel(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.ReposExplicit {
		if len(cfg.Repos) > 0 {
			cfg.TreeRoot = deriveTreeRoot(cfg.Repos[0])
		}
	} else {
		cfg.Repos = discoverChannelRepos(cfg.RecipeRoot, cfg.Channel, &cfg.ChannelMissing)
		cfg.TreeRoot = cfg.RecipeRoot
	}
	cfg.Options, err = options.Load(absRoot)
	if err != nil {
		return Config{}, err
	}
	// Binary package repository: env wins, then /etc/zpkg/binrepo.conf.
	cfg.BinRepo = os.Getenv("ZPKG_BINREPO")
	if cfg.BinRepo == "" {
		if cfg.BinRepo, err = options.ReadVar(absRoot, "etc/zpkg/binrepo.conf", "BINREPO"); err != nil {
			return Config{}, err
		}
	}
	// Default recipe source for "zpkg sync": env wins, then
	// /etc/zpkg/sync.conf.
	cfg.RecipeURL = os.Getenv("ZPKG_RECIPE_URL")
	if cfg.RecipeURL == "" {
		if cfg.RecipeURL, err = options.ReadVar(absRoot, "etc/zpkg/sync.conf", "RECIPEURL"); err != nil {
			return Config{}, err
		}
	}
	if cfg.RecipeGit, err = readConfBool(absRoot, "etc/zpkg/sync.conf", "ZPKG_RECIPE_GIT", "RECIPEGIT", false); err != nil {
		return Config{}, err
	}
	cfg.RecipeBranch = os.Getenv("ZPKG_RECIPE_BRANCH")
	if cfg.RecipeBranch == "" {
		if cfg.RecipeBranch, err = options.ReadVar(absRoot, "etc/zpkg/sync.conf", "RECIPEBRANCH"); err != nil {
			return Config{}, err
		}
	}
	if cfg.RecipeBranch == "" {
		cfg.RecipeBranch = "main"
	}
	if cfg.RecipeAllowUnsigned, err = readConfBool(absRoot, "etc/zpkg/sync.conf", "ZPKG_RECIPE_ALLOW_UNSIGNED", "RECIPEALLOWUNSIGNED", false); err != nil {
		return Config{}, err
	}
	// Signing policy: trusted public keys and the two explicit escape
	// hatches for development/recovery. Env wins over security.conf.
	cfg.TrustedKeys = os.Getenv("ZPKG_TRUSTED_KEYS")
	if cfg.TrustedKeys == "" {
		if cfg.TrustedKeys, err = options.ReadVar(absRoot, "etc/zpkg/security.conf", "TRUSTED_KEYS"); err != nil {
			return Config{}, err
		}
	}
	if cfg.TrustedKeys == "" {
		cfg.TrustedKeys = filepath.Join(absRoot, "etc", "zpkg", "trusted.d")
	}
	if cfg.AllowUnsigned, err = readSecurityBool(absRoot, "ZPKG_ALLOW_UNSIGNED", "ALLOW_UNSIGNED"); err != nil {
		return Config{}, err
	}
	if cfg.AllowExpired, err = readSecurityBool(absRoot, "ZPKG_ALLOW_EXPIRED", "ALLOW_EXPIRED"); err != nil {
		return Config{}, err
	}
	// Build sandbox policy: env wins over /etc/zpkg/sandbox.conf.
	cfg.SandboxMode = os.Getenv("ZPKG_SANDBOX")
	if cfg.SandboxMode == "" {
		if cfg.SandboxMode, err = options.ReadVar(absRoot, "etc/zpkg/sandbox.conf", "SANDBOX"); err != nil {
			return Config{}, err
		}
	}
	if cfg.SandboxMode == "" {
		cfg.SandboxMode = string(sandbox.ModeAuto)
	}
	if _, err := sandbox.ParseMode(cfg.SandboxMode); err != nil {
		return Config{}, err
	}
	cfg.BuildNetwork, err = readConfBool(absRoot, "etc/zpkg/sandbox.conf", "ZPKG_BUILD_NET", "BUILD_NET", true)
	if err != nil {
		return Config{}, err
	}
	cfg.BwrapPath = os.Getenv("ZPKG_BWRAP")
	if cfg.BwrapPath == "" {
		if cfg.BwrapPath, err = options.ReadVar(absRoot, "etc/zpkg/sandbox.conf", "BWRAP"); err != nil {
			return Config{}, err
		}
	}
	if cfg.BwrapPath == "" {
		cfg.BwrapPath = "bwrap"
	}
	// Reproducibility settings: env, then /etc/zpkg/build.conf.
	if cfg.SourceDateEpoch, err = readSourceDateEpoch(absRoot); err != nil {
		return Config{}, err
	}
	if cfg.BuildLog, err = readConfBool(absRoot, "etc/zpkg/build.conf", "ZPKG_BUILD_LOG", "BUILD_LOG", true); err != nil {
		return Config{}, err
	}
	if cfg.SBOM, err = readConfBool(absRoot, "etc/zpkg/build.conf", "ZPKG_SBOM", "SBOM", true); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadChannel resolves the release channel without validating it, so
// "zpkg channel" can report and repair a bad value. Repo discovery treats an
// unknown channel as missing.
func loadChannel(cfg *Config) error {
	if v := strings.TrimSpace(os.Getenv("ZPKG_CHANNEL")); v != "" {
		cfg.Channel = strings.ToLower(v)
		cfg.ChannelSource = "env"
		return nil
	}
	v, err := options.ReadVar(cfg.Root, "etc/zpkg/make.conf", "CHANNEL")
	if err != nil {
		return err
	}
	if v = strings.TrimSpace(v); v != "" {
		cfg.Channel = strings.ToLower(v)
		cfg.ChannelSource = "make.conf"
		return nil
	}
	cfg.Channel = "stable"
	cfg.ChannelSource = "default"
	return nil
}

// ApplyChannel overrides the release channel for one invocation. Repos are
// recomputed from RecipeRoot unless ZPKG_REPOS was set explicitly.
func ApplyChannel(cfg *Config, ch string) error {
	ch = strings.ToLower(strings.TrimSpace(ch))
	if !ValidChannel(ch) {
		return fmt.Errorf("invalid release channel %q (use stable, testing or unstable)", ch)
	}
	cfg.Channel = ch
	cfg.ChannelSource = "flag"
	if cfg.ReposExplicit {
		return nil
	}
	cfg.ChannelMissing = false
	cfg.Repos = discoverChannelRepos(cfg.RecipeRoot, ch, &cfg.ChannelMissing)
	cfg.TreeRoot = cfg.RecipeRoot
	return nil
}

// discoverChannelRepos expands a channel tree into category directories.
// A legacy flat tree (core/ directly under RecipeRoot) is accepted as
// stable so systems installed before channels keep working.
func discoverChannelRepos(root, ch string, missing *bool) []string {
	if !ValidChannel(ch) {
		*missing = true
		return nil
	}
	chRoot := filepath.Join(root, ch)
	if isDir(filepath.Join(chRoot, "core")) {
		var repos []string
		for _, cat := range []string{"core", "main", "desktop"} {
			if d := filepath.Join(chRoot, cat); isDir(d) {
				repos = append(repos, d)
			}
		}
		return repos
	}
	if ch == "stable" && isDir(filepath.Join(root, "core")) {
		var legacy []string
		for _, cat := range []string{"core", "main", "desktop"} {
			if d := filepath.Join(root, cat); isDir(d) {
				legacy = append(legacy, d)
			}
		}
		return legacy
	}
	*missing = true
	return nil
}

// deriveTreeRoot finds the directory that owns recipes.manifest for an
// explicitly configured repo. Channel trees are <tree>/<channel>/<category>;
// legacy trees are <tree>/<category>.
func deriveTreeRoot(repo string) string {
	repo = filepath.Clean(repo)
	parent := filepath.Dir(repo)
	if isCategory(filepath.Base(repo)) && ValidChannel(filepath.Base(parent)) {
		return filepath.Dir(parent)
	}
	return parent
}

// readSourceDateEpoch resolves the fixed build timestamp. ZPKG_SOURCE_DATE_EPOCH
// wins over the standard SOURCE_DATE_EPOCH, which wins over build.conf.
func readSourceDateEpoch(root string) (int64, error) {
	v := os.Getenv("ZPKG_SOURCE_DATE_EPOCH")
	if v == "" {
		v = os.Getenv("SOURCE_DATE_EPOCH")
	}
	if v == "" {
		var err error
		v, err = options.ReadVar(root, "etc/zpkg/build.conf", "SOURCE_DATE_EPOCH")
		if err != nil {
			return 0, err
		}
	}
	if strings.TrimSpace(v) == "" {
		return provenance.DefaultSourceDateEpoch, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("SOURCE_DATE_EPOCH=%q: expected a non-negative Unix timestamp", v)
	}
	return n, nil
}

func readSecurityBool(root, env, key string) (bool, error) {
	return readConfBool(root, "etc/zpkg/security.conf", env, key, false)
}

func readConfBool(root, relPath, env, key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(env)
	if !ok || v == "" {
		var err error
		v, err = options.ReadVar(root, relPath, key)
		if err != nil {
			return false, err
		}
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%s=%q: expected 0 or 1", env, v)
	}
}

func splitRepos(v string) []string {
	if v == "" {
		return nil
	}
	// Use the OS path list separator (';' on Windows, ':' on Unix) so that
	// drive-letter paths like "D:/repos/core" survive splitting on Windows.
	var repos []string
	for _, dir := range strings.Split(v, string(os.PathListSeparator)) {
		if dir = strings.TrimSpace(dir); dir != "" {
			repos = append(repos, dir)
		}
	}
	return repos
}
