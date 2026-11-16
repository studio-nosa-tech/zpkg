// Package cli implements the zpkg command line interface.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/build"
	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/dep"
	"github.com/studio-nosa-tech/zpkg/internal/install"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

const usage = `zpkg - ZIGNOS package manager (v0)

Usage:
  zpkg depends <pkg>...   print build order including dependencies
  zpkg build [-f] <pkg>...    fetch, verify and build into a destroot (no install)
  zpkg install [-f] <pkg>...  build if needed, then install into ZPKG_ROOT
                              (-f rebuilds and reinstalls even if installed)
  zpkg remove <pkg>...    remove installed packages
  zpkg list               list installed packages
  zpkg search <term>      search recipe repos

Environment:
  ZPKG_REPOS   recipe repo directories, separated by the OS path list
               separator (';' on Windows, ':' on Unix); first match wins.
               Defaults to the tree shipped in $ZPKG_ROOT/usr/share/zignos/
               recipes (core, main, desktop) when unset
  ZPKG_ROOT    target system root (default /)
  ZPKG_CACHE   cache dir (default $ZPKG_ROOT/var/cache/zpkg)
  ZPKG_DB      package db dir (default $ZPKG_ROOT/var/lib/zpkg)
  ZPKG_FORCE   set to 1 to allow removing modified packages
  ZPKG_NO_BINPKG  set to 1 to always build from source (skip cached binpkgs)

Build scripts run under bash and receive ZPKG_NAME, ZPKG_VERSION,
ZPKG_RECIPE_DIR, ZPKG_ROOT, ZPKG_CACHE, ZPKG_DESTROOT, ZPKG_SRC and
ZPKG_WORKDIR. ZPKG_RECIPE_DIR lets recipes carry assets (kernel configs,
extra files) next to their build script.

Successful builds are also packed into $ZPKG_CACHE/binpkgs as
<name>-<version>.pkg.tar.gz; install reuses them instead of rebuilding.
`

// Run dispatches a command line invocation.
func Run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("no command given")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "depends":
		return withArgs(cmd, rest, cmdDepends)
	case "build":
		targets, force := splitForce(rest)
		return withArgs(cmd, targets, func(targets []string) error { return cmdBuildInstall(targets, false, force) })
	case "install":
		targets, force := splitForce(rest)
		return withArgs(cmd, targets, func(targets []string) error { return cmdBuildInstall(targets, true, force) })
	case "remove":
		return withArgs(cmd, rest, cmdRemove)
	case "list":
		return cmdList()
	case "search":
		return withArgs(cmd, rest, cmdSearch)
	default:
		return fmt.Errorf("unknown command %q (try: zpkg help)", cmd)
	}
}

// splitForce separates the -f/--force flag from the package arguments.
func splitForce(args []string) (targets []string, force bool) {
	for _, a := range args {
		switch a {
		case "-f", "--force":
			force = true
		default:
			targets = append(targets, a)
		}
	}
	return targets, force
}

func withArgs(name string, args []string, fn func([]string) error) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zpkg %s <pkg>...", name)
	}
	return fn(args)
}

// contains reports whether name is one of the requested targets.
func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func requireRepos(cfg config.Config) error {
	if len(cfg.Repos) == 0 {
		return fmt.Errorf("no recipe repos found: set ZPKG_REPOS or install the /usr/share/zignos/recipes tree")
	}
	return nil
}

// resolve returns targets with dependencies in build order.
func resolve(cfg config.Config, targets []string) ([]string, error) {
	load := func(name string) ([]string, error) {
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return nil, err
		}
		return r.Depends, nil
	}
	return dep.Order(targets, load)
}

func cmdDepends(targets []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	order, err := resolve(cfg, targets)
	if err != nil {
		return err
	}
	for _, name := range order {
		fmt.Println(name)
	}
	return nil
}

// cmdBuildInstall builds each package in dependency order, skipping already
// installed ones (unless force), optionally installing as it goes.
func cmdBuildInstall(targets []string, doInstall, force bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Cache, 0o755); err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	installed := map[string]bool{}
	names, err := db.List()
	if err != nil {
		return err
	}
	for _, n := range names {
		installed[n] = true
	}

	order, err := resolve(cfg, targets)
	if err != nil {
		return err
	}
	for _, name := range order {
		// With force every named target is rebuilt and reinstalled;
		// dependencies that are already present are still skipped so
		// only the requested packages are replaced.
		if installed[name] && !(force && contains(targets, name)) {
			fmt.Printf("skipping %s (already installed)\n", name)
			continue
		}
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return err
		}
		res := build.LoadBinpkg(r, cfg)
		if res != nil {
			fmt.Printf("installed from binpkg %s %s (%d files)\n", r.Name, r.Version, len(res.Entries))
		} else {
			res, err = build.Run(r, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("built %s %s (%d files)\n", r.Name, r.Version, len(res.Entries))
			if pkgPath, err := build.PackBinpkg(r, res, cfg.Cache); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: binpkg pack failed:", err)
			} else {
				fmt.Printf("binpkg: %s\n", pkgPath)
			}
		}
		if !doInstall {
			fmt.Printf("destroot: %s\n", res.Destroot)
			continue
		}
		rec := &pkgdb.Rec{Name: r.Name, Version: r.Version, Entries: res.Entries}
		if err := install.Apply(rec, res.Destroot, cfg.Root, db); err != nil {
			return err
		}
		fmt.Printf("installed %s %s\n", r.Name, r.Version)
	}
	return nil
}

func cmdRemove(names []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for _, name := range names {
		if err := install.Remove(name, cfg.Root, db); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", name)
	}
	return nil
}

func cmdList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	names, err := db.List()
	if err != nil {
		return err
	}
	for _, n := range names {
		rec, err := db.Get(n)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", rec.Name, rec.Version)
	}
	return nil
}

func cmdSearch(args []string) error {
	term := args[0]
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	for _, repo := range cfg.Repos {
		items, err := os.ReadDir(repo)
		if err != nil {
			return err
		}
		for _, it := range items {
			if !it.IsDir() || !strings.Contains(it.Name(), term) {
				continue
			}
			r, err := recipe.Load(cfg.Repos, it.Name())
			if err != nil {
				return err
			}
			fmt.Printf("%s %s %s\n", filepath.Base(repo), r.Name, r.Version)
		}
	}
	return nil
}
