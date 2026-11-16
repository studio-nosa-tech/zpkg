// Package config resolves zpkg runtime paths from the environment.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Config holds the runtime layout.
type Config struct {
	Repos []string // recipe repos, in search order (first match wins)
	Root  string   // target system root, absolute
	Cache string   // source downloads and build workdirs
	DB    string   // installed package database
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
	return cfg, nil
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
