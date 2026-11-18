// Package config resolves zpkg runtime paths from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/options"
)

// Config holds the runtime layout.
type Config struct {
	Repos     []string        // recipe repos, in search order (first match wins)
	Root      string          // target system root, absolute
	Cache     string          // source downloads and build workdirs
	DB        string          // installed package database
	Options   options.Options // USE flags and build options for Root
	BinRepo   string          // binary package repository (URL or local dir), "" = none
	RecipeURL string          // default source for "zpkg sync" (URL or path), "" = none

	TrustedKeys   string // directory of trusted *.pub signing keys
	AllowUnsigned bool   // accept unsigned repository/recipe metadata (development)
	AllowExpired  bool   // ignore signature expiry (recovery)
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
	cfg := Config{
		Repos: splitRepos(os.Getenv("ZPKG_REPOS")),
		Root:  absRoot,
		Cache: os.Getenv("ZPKG_CACHE"),
		DB:    os.Getenv("ZPKG_DB"),
	}
	if cfg.Cache == "" {
		cfg.Cache = filepath.Join(absRoot, "var", "cache", "zpkg")
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(absRoot, "var", "lib", "zpkg")
	}
	if len(cfg.Repos) == 0 {
		// Default to the recipe tree shipped with the system. Categories
		// are searched in this order (core first, then the extras).
		for _, cat := range []string{"core", "main", "desktop"} {
			d := filepath.Join(absRoot, "usr", "share", "zignos", "recipes", cat)
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				cfg.Repos = append(cfg.Repos, d)
			}
		}
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
	return cfg, nil
}

func readSecurityBool(root, env, key string) (bool, error) {
	v, ok := os.LookupEnv(env)
	if !ok || v == "" {
		var err error
		v, err = options.ReadVar(root, "etc/zpkg/security.conf", key)
		if err != nil {
			return false, err
		}
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
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
