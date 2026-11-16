// Package cli implements the zpkg command line interface.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/dep"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

const usage = `zpkg - ZIGNOS package manager (v0.9)

Usage:
  zpkg depends <pkg>...       print build order including dependencies
  zpkg build [-f] <pkg>...    fetch, verify and build into a destroot (no install)
  zpkg install [-f] [-p] [--asdeps] <pkg>...
                              build if needed, then install into ZPKG_ROOT
                              -f  rebuild/reinstall even if installed
                              -p  pretend: show what would happen
                              --asdeps  do not add the target to world
  zpkg upgrade [-p] [pkg]...  upgrade world (or the named packages) to the
                              recipe versions
  zpkg remove <pkg>...        remove installed packages (and world entries)
  zpkg list                   list installed packages
  zpkg info <pkg>...          installed metadata and the available recipe
  zpkg owner <path>...        which package owns a path
  zpkg world                  packages installed explicitly
  zpkg search <term>          search recipe repos

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

World: packages you name on "install" are recorded in $ZPKG_DB/world and are
what "upgrade" acts on. Dependencies are installed but not added to world.

Build scripts run under bash and receive ZPKG_NAME, ZPKG_VERSION,
ZPKG_RECIPE_DIR, ZPKG_ROOT, ZPKG_CACHE, ZPKG_DESTROOT, ZPKG_SRC and
ZPKG_WORKDIR. ZPKG_RECIPE_DIR lets recipes carry assets (kernel configs,
extra files) next to their build script.

Successful builds are also packed into $ZPKG_CACHE/binpkgs as
<name>-<version>[-rN].pkg.tar.gz; install reuses them instead of rebuilding.
`

// installOptions are the flags shared by install/build/upgrade.
type installOptions struct {
	force   bool
	pretend bool
	asdeps  bool
}

// parseInstallArgs separates flags from package names.
func parseInstallArgs(args []string) (targets []string, o installOptions) {
	for _, a := range args {
		switch a {
		case "-f", "--force":
			o.force = true
		case "-p", "--pretend":
			o.pretend = true
		case "--asdeps":
			o.asdeps = true
		default:
			targets = append(targets, a)
		}
	}
	return targets, o
}

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
		targets, o := parseInstallArgs(rest)
		return withArgs(cmd, targets, func(t []string) error { return cmdBuildInstall(t, false, o) })
	case "install":
		targets, o := parseInstallArgs(rest)
		return withArgs(cmd, targets, func(t []string) error { return cmdBuildInstall(t, true, o) })
	case "upgrade":
		targets, o := parseInstallArgs(rest)
		return cmdUpgrade(targets, o)
	case "remove":
		return withArgs(cmd, rest, cmdRemove)
	case "list":
		return cmdList()
	case "info":
		return withArgs(cmd, rest, cmdInfo)
	case "owner":
		return withArgs(cmd, rest, cmdOwner)
	case "world":
		return cmdWorld()
	case "search":
		return withArgs(cmd, rest, cmdSearch)
	default:
		return fmt.Errorf("unknown command %q (try: zpkg help)", cmd)
	}
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

// recipeDeps is the dependency list used for build ordering: both runtime
// and build-only dependencies must be built before the package.
func recipeDeps(r *recipe.Recipe) []string {
	deps := make([]string, 0, len(r.Depends)+len(r.MakeDepends))
	deps = append(deps, r.Depends...)
	deps = append(deps, r.MakeDepends...)
	return deps
}

// resolve returns targets with dependencies in build order.
func resolve(cfg config.Config, targets []string) ([]string, error) {
	load := func(name string) ([]string, error) {
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return nil, err
		}
		return recipeDeps(r), nil
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

// joinDeps renders a dependency list for display, or "" when empty.
func joinDeps(deps []string) string {
	return strings.Join(deps, " ")
}
