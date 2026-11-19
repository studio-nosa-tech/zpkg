// Package cli implements the zpkg command line interface.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
)

const usage = `zpkg - ZIGNOS package manager (v0.9)

Usage:
  zpkg depends [-r] <pkg>...  print build order including dependencies;
                              -r/--reverse prints installed dependents
  zpkg build [-f] [--no-sandbox] [--clean] [--repro-check] <pkg>...
                              fetch, verify and build into a destroot (no install)
                              --clean  remove the build work dir first
                              --repro-check  build twice and compare manifests
  zpkg install [-f] [-p] [--asdeps] [--no-sandbox] [--clean] [--repro-check] <pkg>...
                              build if needed, then install into ZPKG_ROOT
                              -f  rebuild/reinstall even if installed
                              -p  pretend: show what would happen
                              --asdeps  do not add the target to world
                              --no-sandbox  build without bubblewrap isolation
                              --clean  remove the build work dir first
                              --repro-check  build twice and compare manifests
  zpkg upgrade [-p] [--newuse] [pkg]...
                              upgrade world (or the named packages) to the
                              recipe versions; --newuse also rebuilds a
                              package whose effective USE flags changed
  zpkg remove [-f] <pkg>...   remove installed packages (all slots) and
                              their world entries; -f skips the reverse-
                              dependency safety check
  zpkg list                   list installed packages
  zpkg info <pkg>...          installed metadata and the available recipe
  zpkg owner <path>...        which package owns a path
  zpkg world                  packages installed explicitly
  zpkg search <term>          search recipe repos
  zpkg mkindex [dir]          write index.json for a binary package
                              repository dir (default: the local binpkg cache)
  zpkg sync [--from <src>] [--dest <dir>] [-n] [--allow-unsigned]
                              replace the recipe tree from a stage3 tarball,
                              recipe tarball, directory or URL
                              --from  source; default $ZPKG_RECIPE_URL or
                                      RECIPEURL in /etc/zpkg/sync.conf
                              --dest  target tree (default $ZPKG_ROOT/usr/
                                      share/zignos/recipes)
                              -n      validate and count only
                              --allow-unsigned  accept a tree with no manifest
                                                or signature (development)
  zpkg key generate --private <path> [--public <path>]
                              create an Ed25519 signing key pair
  zpkg key import <pubkey>... add a trusted repository/recipe signing key
  zpkg key list               list trusted keys
  zpkg key remove <keyid>     remove a trusted key
  zpkg sign --key <private> <file>...
                              write <file>.sig (detached Ed25519 signature)
  zpkg verify <file>...       verify <file>.sig with the trusted keys
  zpkg sign-recipes --key <private> [--generation N] [--expires ...] [dir]
                              sign a recipe tree (recipes.manifest + .sig)
  zpkg mkindex [--key <private>] [--generation N] [--expires ...] [dir]
                              write index.json; --key also signs it and
                              publishes keys/<keyid>.pub
  zpkg use [flag | -flag]...  show or edit /etc/zpkg/use.conf (USE flags)
  zpkg use --package <pkg> [flag | -flag]...
                              show or edit the package's line in package.use
  zpkg provenance <pkg>...    show the recorded build provenance (JSON)
  zpkg sbom <pkg>...          show a package's SPDX 2.3 SBOM
  zpkg sbom --system [-o <file>]
                              SPDX 2.3 SBOM of every installed package
  zpkg log [-p] <pkg>...      print a package's build log; -p prints its path
  zpkg recover                roll back / complete an interrupted transaction

Environment:
  ZPKG_REPOS   recipe repo directories, separated by the OS path list
               separator (';' on Windows, ':' on Unix); first match wins.
               Defaults to the tree shipped in $ZPKG_ROOT/usr/share/zignos/
               recipes (core, main, desktop) when unset
  ZPKG_ROOT    target system root (default /)
  ZPKG_CACHE   cache dir (default $ZPKG_ROOT/var/cache/zpkg)
  ZPKG_DB      package db dir (default $ZPKG_ROOT/var/lib/zpkg)
  ZPKG_FORCE   set to 1 to allow removing modified packages
  ZPKG_NO_BINPKG  set to 1 to always build from source (skip cached binpkgs
                  and binary repositories)
  ZPKG_RECIPE_URL default source for "zpkg sync" (URL or path); overrides
                  RECIPEURL in /etc/zpkg/sync.conf
  ZPKG_BINREPO    base URL/path of a binary package repository; install
                  tries a matching package from it before building from
                  source (or set BINREPO= in /etc/zpkg/binrepo.conf). A
                  match requires the same version, revision and effective
                  USE flags
  ZPKG_TRUSTED_KEYS  directory of trusted *.pub signing keys (default
                  $ZPKG_ROOT/etc/zpkg/trusted.d; also TRUSTED_KEYS= in
                  /etc/zpkg/security.conf)
  ZPKG_ALLOW_UNSIGNED=1  accept repository indexes / recipe trees with no
                  signature at all (development only; a present but invalid
                  signature is always rejected)
  ZPKG_ALLOW_EXPIRED=1   ignore signature expiry (recovery only; rollback
                  checks and signature verification still apply)
  ZPKG_SANDBOX   auto (default), require or off. Package builds run under
                  bubblewrap when available; auto warns and builds directly
                  if bwrap is missing
  ZPKG_BUILD_NET default 1. Set to 0 to run builds in a network namespace
                  without connectivity (recipes can override with a
                  build_network file)
  ZPKG_BWRAP     bubblewrap executable (default: bwrap)
  SOURCE_DATE_EPOCH  fixed build timestamp (default 946684800 = 2000-01-01);
                  ZPKG_SOURCE_DATE_EPOCH overrides, then SOURCE_DATE_EPOCH,
                  then SOURCE_DATE_EPOCH= in /etc/zpkg/build.conf
  ZPKG_BUILD_LOG  default 1. Set to 0 to stop writing build logs to
                  $ZPKG_CACHE/logs (BUILD_LOG= in /etc/zpkg/build.conf)
  ZPKG_SBOM      default 1. Set to 0 to skip SPDX SBOM generation
                  (SBOM= in /etc/zpkg/build.conf)

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
	force      bool
	pretend    bool
	asdeps     bool
	newuse     bool // upgrade: rebuild when effective USE flags changed
	noSandbox  bool
	clean      bool // remove the build work directory before building
	reproCheck bool // build twice and compare the destroot manifests
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
		case "--newuse":
			o.newuse = true
		case "--no-sandbox":
			o.noSandbox = true
		case "--clean":
			o.clean = true
		case "--repro-check":
			o.reproCheck = true
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
		return cmdDepends(rest)
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
		return cmdRemove(rest)
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
	case "use":
		return cmdUse(rest)
	case "provenance":
		return withArgs(cmd, rest, cmdProvenance)
	case "sbom":
		return cmdSBOM(rest)
	case "log":
		return cmdLog(rest)
	case "recover":
		return cmdRecover()
	case "mkindex":
		return cmdMkIndex(rest)
	case "sync":
		return cmdSync(rest)
	case "key":
		return cmdKey(rest)
	case "sign":
		return cmdSign(rest)
	case "verify":
		return cmdVerify(rest)
	case "sign-recipes":
		return cmdSignRecipes(rest)
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

// joinDeps renders a dependency list for display, or "" when empty.
func joinDeps(deps []string) string {
	return strings.Join(deps, " ")
}
