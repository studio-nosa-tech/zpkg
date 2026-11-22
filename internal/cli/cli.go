// Package cli implements the zpkg command line interface.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
)

// Version is the zpkg release version shipped by this source tree.
const Version = "0.3.4"

const usage = `zpkg - ZIGNOS package manager (v` + Version + `)

Usage:
  zpkg version | --version    print the zpkg version
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
  zpkg upgrade [-p] [-y] [--newuse] [--prune] [--allow-downgrade] [--channel <ch>] [--verify] [pkg]...
                              upgrade world (or the named packages) to the
                              selected release channel. Shows the changes,
                              dependency changes and warnings, builds every
                              package, then installs. --newuse also rebuilds
                              packages whose effective USE flags changed.
                              -p  pretend: show the upgrade plan only
                              -y  do not ask for confirmation
                              --prune  remove no-longer-required dependencies
                              --allow-downgrade  permit channel downgrades
                              --channel  override the channel for this run
                              --verify  hash every file in the final check
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
  zpkg sync [--from <src>] [--dest <dir>] [--git] [--branch <name>]
            [-n] [--allow-unsigned]
                              replace the recipe tree from a stage3 tarball,
                              recipe tarball, directory, URL or git repository
                              --from  source; default $ZPKG_RECIPE_URL or
                                      RECIPEURL in /etc/zpkg/sync.conf
                              --dest  target tree (default $ZPKG_ROOT/usr/
                                      share/zignos/recipes)
                              --git   treat the source as a git repository
                                      (also RECIPEGIT=1 in sync.conf)
                              --branch  git branch (default main / RECIPEBRANCH)
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
  zpkg video-cards [card | -card]...
                              show or edit VIDEO_CARDS in /etc/zpkg/make.conf
  zpkg provenance <pkg>...    show the recorded build provenance (JSON)
  zpkg sbom <pkg>...          show a package's SPDX 2.3 SBOM
  zpkg sbom --system [-o <file>]
                              SPDX 2.3 SBOM of every installed package
  zpkg log [-p] <pkg>...      print a package's build log; -p prints its path
  zpkg channel [stable|testing|unstable]
                              show the release channel and available trees,
                              or write CHANNEL= to /etc/zpkg/make.conf
  zpkg recover                roll back / complete an interrupted transaction

Environment:
  ZPKG_REPOS   recipe repo directories, separated by the OS path list
               separator (';' on Windows, ':' on Unix); first match wins.
               When unset, $ZPKG_ROOT/usr/share/zignos/recipes/<channel>/
               {core,main,desktop} is used (legacy flat trees are accepted
               as stable)
  ZPKG_CHANNEL release channel: stable (default), testing or unstable.
               Overrides CHANNEL= in /etc/zpkg/make.conf for this run
  ZPKG_VIDEO_CARDS  video cards passed to recipes as ZPKG_VIDEO_CARDS;
               overrides VIDEO_CARDS= in /etc/zpkg/make.conf
  ZPKG_ROOT    target system root (default /)
  ZPKG_CACHE   cache dir (default $ZPKG_ROOT/var/cache/zpkg)
  ZPKG_DB      package db dir (default $ZPKG_ROOT/var/lib/zpkg)
  ZPKG_FORCE   set to 1 to allow removing modified packages
  ZPKG_NO_BINPKG  set to 1 to always build from source (skip cached binpkgs
                  and binary repositories)
  ZPKG_RECIPE_URL default source for "zpkg sync" (URL or path); overrides
                  RECIPEURL in /etc/zpkg/sync.conf
  ZPKG_RECIPE_GIT  set to 1 to fetch the recipe source as a git repository
                  (or RECIPEGIT=1 in sync.conf)
  ZPKG_RECIPE_BRANCH  git branch for the recipe source (default main;
                  or RECIPEBRANCH= in sync.conf)
  ZPKG_RECIPE_ALLOW_UNSIGNED  allow an unsigned tree from the git recipe
                  source (or RECIPEALLOWUNSIGNED=1 in sync.conf)
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
  ZPKG_KEEP_WORKDIR default 0. Set to 1 to keep build work directories after
                  a successful install (KEEP_WORKDIR= in build.conf)
  ZPKG_BUILD_BINPKG default 0. Set to 1 to create local binpkgs after a
                  source build (BUILD_BINPKG= in build.conf)
  ZPKG_KEEP_SOURCES default 0. Set to 1 to keep downloaded source archives
                  (KEEP_SOURCES= in build.conf)

World: packages you name on "install" are recorded in $ZPKG_DB/world and are
what "upgrade" acts on. Dependencies are installed but not added to world.

Build scripts run under bash and receive ZPKG_NAME, ZPKG_VERSION,
ZPKG_RECIPE_DIR, ZPKG_ROOT, ZPKG_CACHE, ZPKG_DESTROOT, ZPKG_SRC and
ZPKG_WORKDIR. ZPKG_RECIPE_DIR lets recipes carry assets (kernel configs,
extra files) next to their build script.

Successful builds are packed into $ZPKG_CACHE/binpkgs as
<name>-<version>[-rN].pkg.tar.gz only when BUILD_BINPKG=1; install reuses
them instead of rebuilding. By default work dirs and source archives are
removed after a successful install.
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

	// Upgrade support: build every package before installing any of them,
	// suppress the generic pre-build plan (upgrade prints its own report),
	// and override the release channel for this invocation.
	deferInstall bool
	skipPlan     bool
	channel      string
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
	case "version", "-V", "--version":
		fmt.Printf("zpkg %s\n", Version)
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
		return cmdUpgrade(rest)
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
	case "video-cards":
		return cmdVideoCards(rest)
	case "provenance":
		return withArgs(cmd, rest, cmdProvenance)
	case "sbom":
		return cmdSBOM(rest)
	case "log":
		return cmdLog(rest)
	case "channel":
		return cmdChannel(rest)
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
	if !config.ValidChannel(cfg.Channel) {
		return fmt.Errorf("invalid release channel %q (use stable, testing or unstable)", cfg.Channel)
	}
	if len(cfg.Repos) == 0 {
		if cfg.ChannelMissing {
			return fmt.Errorf("release channel %q is not installed under %s (run \"zpkg sync\" to add it)", cfg.Channel, cfg.RecipeRoot)
		}
		return fmt.Errorf("no recipe repos found: set ZPKG_REPOS or install the /usr/share/zignos/recipes tree")
	}
	return nil
}

// joinDeps renders a dependency list for display, or "" when empty.
func joinDeps(deps []string) string {
	return strings.Join(deps, " ")
}
