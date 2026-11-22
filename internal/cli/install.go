package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/build"
	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/install"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
	"github.com/studio-nosa-tech/zpkg/internal/world"
)

// metaFromRecipe captures the provenance of an install so later upgrades
// and queries do not need to re-read the recipe tree.
func videoCardsFor(r *recipe.Recipe, opts options.Options) []string {
	if !r.UsesVideoCards() {
		return nil
	}
	return append([]string{}, opts.VideoCards...)
}

func metaFromRecipe(r *recipe.Recipe, use, videoCards []string, channel string) *pkgdb.Meta {
	digest := ""
	if r.Dir != "" {
		digest, _ = provenance.RecipeDigest(r.Dir)
	}
	return &pkgdb.Meta{
		Schema:       pkgdb.Schema,
		Name:         r.Name,
		Version:      r.Version,
		Revision:     r.Revision,
		Slot:         r.Slot,
		Repo:         r.Repo,
		Channel:      channel,
		Arch:         runtime.GOARCH,
		Installed:    time.Now().UTC().Format(time.RFC3339),
		UseFlags:     append([]string{}, use...),
		VideoCards:   videoCards,
		Depends:      append([]string{}, r.Depends...),
		BuildDeps:    append([]string{}, r.MakeDepends...),
		Provides:     append([]string{}, r.Provides...),
		Conflicts:    append([]string{}, r.Conflicts...),
		RecipeDigest: digest,
	}
}

// recipeTreeOrigin finds the recipe tree root (parent of the category dir)
// and returns its signed manifest provenance, when present.
func recipeTreeOrigin(r *recipe.Recipe, cfg config.Config) (root, keyID string, generation int64) {
	root = cfg.TreeRoot
	if root == "" {
		root = filepath.Dir(filepath.Dir(r.Dir))
	}
	m, err := recipesig.ReadManifest(root)
	if err != nil {
		return root, "", 0
	}
	return root, m.KeyID, m.Generation
}

// installJob is one package in the resolved install order. target marks a
// package named on the command line (dependencies are pulled in by resolve).
type installJob struct {
	r      *recipe.Recipe
	target bool
}

// cmdBuildInstall builds each package in dependency order, skipping already
// installed ones (unless force), optionally installing as it goes.
func cmdBuildInstall(targets []string, doInstall bool, o installOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if o.channel != "" {
		if err := config.ApplyChannel(&cfg, o.channel); err != nil {
			return err
		}
	}
	if o.noSandbox {
		cfg.SandboxMode = "off"
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Cache, 0o755); err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	installed := map[string]string{} // name@slot -> installed version
	refs, err := db.List()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return err
		}
		installed[pkgdb.SlotKey(ref.Name, ref.Slot)] = rec.VersionString()
	}

	plan, err := resolvePlan(cfg, targets)
	if err != nil {
		return err
	}
	targetSet := map[string]bool{}
	for _, name := range plan.Targets {
		if name != "" {
			targetSet[name] = true
		}
	}
	// Load every recipe first: the pre-build plan and the "(i of n)" in the
	// progress markers need the list of packages that will actually be
	// handled (skipped ones do not count).
	var jobs []installJob
	for _, name := range plan.Order {
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return err
		}
		// A record exists for this name AND slot: it is an upgrade. Other
		// slots of the same name stay installed (that is the point of
		// slots). With force every named target is rebuilt and
		// reinstalled; dependencies that are already present are still
		// skipped so only the requested packages are replaced.
		// --clean/--repro-check are explicit "build it now" requests even if
		// the package is installed, because a cached binpkg would hide the
		// source build they are meant to exercise.
		explicitBuild := (o.clean || o.reproCheck) && targetSet[name]
		have, installedHere := installed[pkgdb.SlotKey(name, r.Slot)]
		// "install pkg" follows the same UX as other package managers:
		// naming an installed package with a different version/revision
		// upgrades (or downgrades) it. Dependencies are still skipped so an
		// unrelated install does not rebuild the world.
		versionChanged := targetSet[name] && installedHere && have != r.FullVersion()
		skip := installedHere && !(o.force && targetSet[name]) && !explicitBuild && !versionChanged
		if skip {
			phase("Skipping %s (already installed)", pkgLabel(r))
			continue
		}
		jobs = append(jobs, installJob{r: r, target: targetSet[name]})
	}

	if len(jobs) > 0 && !o.skipPlan {
		entries := make([]planEntry, len(jobs))
		for i, jb := range jobs {
			entries[i] = planEntry{
				label:  pkgLabel(jb.r),
				use:    useString(jb.r, cfg.Options.ForPackage(jb.r.Name).Use),
				target: jb.target,
			}
		}
		switch {
		case o.pretend && doInstall:
			printPlan(entries, "would be installed")
		case o.pretend:
			printPlan(entries, "would be built")
		case doInstall:
			printPlan(entries, "will be installed")
		default:
			printPlan(entries, "will be built")
		}
	}
	if o.pretend {
		return nil
	}

	var staged []preparedInstall
	var sourceResults []*build.Result
	for i, jb := range jobs {
		r := jb.r
		label := pkgLabel(r)
		start := time.Now()
		opts := cfg.Options.ForPackage(r.Name)

		var res *build.Result
		// --clean and --repro-check require a fresh source build.
		if !o.clean && !o.reproCheck {
			res = build.LoadBinpkg(r, cfg)
			if res == nil {
				// No local cache hit: try a configured binary package
				// repository before falling back to a source build. Signature,
				// rollback and digest failures are hard errors.
				var repoErr error
				res, repoErr = build.LoadRepoBinpkg(r, cfg)
				if repoErr != nil {
					return repoErr
				}
			}
		}
		fromBinpkg := res != nil
		reproChecked := false
		if res == nil {
			if o.clean || o.reproCheck {
				if err := build.CleanWork(r, cfg); err != nil {
					return err
				}
			}
			phase("Building %s", label)
			res, err = build.Run(r, cfg)
			if err != nil {
				return err
			}
			phase("Built %s (%d files) in %s", label, len(res.Entries), elapsed(time.Since(start)))
			if _, envHash, envCount := provenance.InstalledEnvironment(db); envCount > 0 {
				res.BuildEnvHash, res.BuildEnvCount = envHash, envCount
			}
			sourceResults = append(sourceResults, res)
			// Old work dirs of the same package are always stale once a new
			// build succeeds; KEEP_WORKDIR only preserves the current one.
			if err := build.CleanOldWork(r, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: warning: clean old workdir:", err)
			}
			if cfg.BuildBinpkg || o.reproCheck {
				if _, err := build.PackBinpkg(r, res, cfg.Cache, opts.HashFor(r.UsesVideoCards()), cfg.Channel); err != nil {
					fmt.Fprintln(os.Stderr, "zpkg: binpkg pack failed:", err)
				} else {
					phase("Packed binpkg %s", res.ArtifactPath)
				}
			}
			if err := build.CleanOldBinpkgs(r, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: warning: clean old binpkgs:", err)
			}
			if o.reproCheck {
				firstHash := res.ArtifactSHA256
				if err := build.CleanWork(r, cfg); err != nil {
					return err
				}
				phase("Rebuilding %s for reproducibility check", label)
				second, err := build.Run(r, cfg)
				if err != nil {
					return err
				}
				if _, envHash, envCount := provenance.InstalledEnvironment(db); envCount > 0 {
					second.BuildEnvHash, second.BuildEnvCount = envHash, envCount
				}
				if _, err := build.PackBinpkg(r, second, cfg.Cache, opts.HashFor(r.UsesVideoCards()), cfg.Channel); err != nil {
					return err
				}
				diffs := diffEntries(res.Entries, second.Entries)
				if len(diffs) > 0 {
					limit := len(diffs)
					if limit > 20 {
						limit = 20
					}
					return fmt.Errorf("%s: build is not reproducible (%d differences):\n  %s",
						label, len(diffs), strings.Join(diffs[:limit], "\n  "))
				}
				phase("Reproducible: %s (manifest matches, artifact %s)", label, second.ArtifactSHA256)
				if firstHash != second.ArtifactSHA256 {
					fmt.Fprintf(os.Stderr, "zpkg: note: manifest matches but binpkg bytes differ (first %s, second %s)\n", firstHash, second.ArtifactSHA256)
				}
				res = second
				reproChecked = true
				if !cfg.BuildBinpkg {
					if err := build.RemoveCurrentBinpkg(r, cfg); err != nil {
						fmt.Fprintln(os.Stderr, "zpkg: warning: remove repro-check binpkg:", err)
					} else {
						res.ArtifactPath = ""
					}
				}
			}
		}
		meta := metaFromRecipe(r, opts.Use, videoCardsFor(r, opts), cfg.Channel)
		meta.Origin = res.OriginKind
		meta.OriginRepo = res.OriginRepo
		meta.OriginKeyID = res.OriginKeyID
		meta.OriginGeneration = res.OriginGeneration
		meta.OriginIndexSHA256 = res.OriginIndexSHA256
		meta.OriginPkgSHA256 = res.OriginPkgSHA256
		meta.BuildSandbox = res.Sandbox
		if res.Sandbox != "" {
			if res.Network {
				meta.BuildNetwork = "on"
			} else {
				meta.BuildNetwork = "off"
			}
		}
		meta.BuildEnvHash = res.BuildEnvHash
		meta.BuildEnvCount = res.BuildEnvCount
		if meta.Origin == "source" {
			// The recipe tree itself may have been synced from a signed
			// manifest; record that signer as the source provenance.
			if root, keyID, generation := recipeTreeOrigin(r, cfg); keyID != "" {
				meta.OriginRepo = root
				meta.OriginKeyID = keyID
				meta.OriginGeneration = generation
			}
		}
		info := provenanceForBuild(db, cfg, r, res, meta)
		info.Build.ReproducibleCheck = reproChecked
		if info.Build.Origin == "source" {
			// Provenance and SBOM sidecars live next to the binary package
			// for consumers that never install it.
			if err := writeArtifactProvenance(cfg, res, info); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: provenance write failed:", err)
			}
		}
		if !doInstall {
			phase("Destroot %s", res.Destroot)
			continue
		}
		p := preparedInstall{
			r: r, res: res, meta: meta, info: info, label: label,
			fromBinpkg: fromBinpkg, index: i, total: len(jobs),
		}
		if o.deferInstall {
			// Upgrade mode: build everything first so a build failure
			// leaves the installed system untouched.
			staged = append(staged, p)
			continue
		}
		if err := applyPrepared(p, cfg, db, targetSet, o); err != nil {
			return err
		}
	}
	for _, p := range staged {
		if err := applyPrepared(p, cfg, db, targetSet, o); err != nil {
			return err
		}
	}
	if !cfg.KeepSources {
		for _, res := range sourceResults {
			if err := build.RemoveSourceArchives(res, cfg.Cache); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: warning: remove source archive:", err)
			}
		}
	}
	return nil
}

// preparedInstall is one built package waiting to be applied. It exists so
// "zpkg upgrade" can finish every build before touching the live system.
type preparedInstall struct {
	r          *recipe.Recipe
	res        *build.Result
	meta       *pkgdb.Meta
	info       *provenance.Info
	label      string
	fromBinpkg bool
	index      int
	total      int
}

func applyPrepared(p preparedInstall, cfg config.Config, db pkgdb.DB, targetSet map[string]bool, o installOptions) error {
	if p.fromBinpkg {
		phase("Installing (%d of %d) %s (binpkg)", p.index+1, p.total, p.label)
	} else {
		phase("Installing (%d of %d) %s", p.index+1, p.total, p.label)
	}
	rec := &pkgdb.Rec{
		Name:     p.r.Name,
		Slot:     p.r.Slot,
		Version:  p.r.Version,
		Revision: p.r.Revision,
		Entries:  p.res.Entries,
		Meta:     p.meta,
	}
	if err := install.Apply(rec, p.res.Destroot, cfg.Root, db); err != nil {
		return err
	}
	if err := writeInstalledProvenance(cfg, db, p.r, p.info); err != nil {
		fmt.Fprintln(os.Stderr, "zpkg: provenance write failed:", err)
	}
	// Only packages named on the command line (or the providers chosen
	// for virtual targets) become world members; dependencies are
	// tracked but not part of the world set.
	if targetSet[p.r.Name] && !o.asdeps {
		if err := world.Add(cfg.DB, p.r.Name); err != nil {
			return err
		}
	}
	if err := runHook(cfg, p.r, "post_install"); err != nil {
		return err
	}
	refreshLdconfig(cfg)
	phase("Installed %s", p.label)
	if !cfg.KeepWorkdir {
		if err := build.CleanWork(p.r, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "zpkg: warning: clean workdir:", err)
		}
	}
	return nil
}

func cmdRemove(args []string) error {
	force := false
	var names []string
	for _, a := range args {
		switch a {
		case "-f", "--force":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q", a)
			}
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: zpkg remove [-f] <pkg>...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	if !force {
		dependents, err := reverseDependents(cfg, db, names)
		if err != nil {
			return err
		}
		if len(dependents) > 0 {
			return fmt.Errorf("cannot remove %s: required by %s (use -f to override)",
				strings.Join(names, " "), strings.Join(dependents, ", "))
		}
	}
	for i, name := range names {
		phase("Removing (%d of %d) %s", i+1, len(names), name)
		// The recipe may be gone from the tree; hooks are best-effort.
		r, _ := recipe.Load(cfg.Repos, name)
		if err := install.Remove(name, cfg.Root, db); err != nil {
			return err
		}
		if err := world.Remove(cfg.DB, name); err != nil {
			return err
		}
		if r != nil {
			if err := runHook(cfg, r, "post_remove"); err != nil {
				return err
			}
		}
		refreshLdconfig(cfg)
		phase("Removed %s", name)
	}
	return nil
}

// runHook executes a recipe hook (<recipe>/post_install, post_remove) with
// the package environment. When the target root is runnable we chroot into
// it (so /etc paths mean the target); a live root runs the hook directly,
// and a bare test/seed root runs it on the host with ZPKG_ROOT set.
func runHook(cfg config.Config, r *recipe.Recipe, hook string) error {
	scriptPath := filepath.Join(r.Dir, hook)
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	env := []string{
		"ZPKG_NAME=" + r.Name,
		"ZPKG_VERSION=" + r.FullVersion(),
		"ZPKG_ROOT=" + cfg.Root,
		"ZPKG_USE=" + strings.Join(cfg.Options.ForPackage(r.Name).Use, " "),
		"ZPKG_VIDEO_CARDS=" + strings.Join(cfg.Options.ForPackage(r.Name).VideoCards, " "),
	}

	var cmd *exec.Cmd
	switch {
	case cfg.Root == "/":
		args := append([]string{"/usr/bin/env"}, env...)
		args = append(args, "/usr/bin/bash", "-s")
		cmd = exec.Command(args[0], args[1:]...)
	case fileExists(filepath.Join(cfg.Root, "usr", "bin", "bash")):
		args := append([]string{cfg.Root, "/usr/bin/env"}, env...)
		args = append(args, "/usr/bin/bash", "-s")
		cmd = exec.Command("chroot", args...)
	default:
		args := append([]string{"env"}, env...)
		args = append(args, "bash", "-s")
		cmd = exec.Command(args[0], args[1:]...)
	}
	cmd.Stdin = bytes.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s hook failed: %w (the package is already installed)", r.Name, hook, err)
	}
	return nil
}

// refreshLdconfig updates the shared library cache after install/remove.
// It is best-effort: a cross-build root without a runnable loader is skipped.
func refreshLdconfig(cfg config.Config) {
	lc := filepath.Join(cfg.Root, "usr", "sbin", "ldconfig")
	if !fileExists(lc) {
		return
	}
	var cmd *exec.Cmd
	switch {
	case cfg.Root == "/":
		cmd = exec.Command("/usr/sbin/ldconfig")
	case fileExists(filepath.Join(cfg.Root, "usr", "bin", "bash")):
		cmd = exec.Command("chroot", cfg.Root, "/usr/sbin/ldconfig")
	default:
		return
	}
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "zpkg: ldconfig failed: %v\n", err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cmdRecover repairs the state left by an interrupted install or removal.
func cmdRecover() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	msg, err := install.Recover(cfg.Root, db)
	if err != nil {
		return err
	}
	if msg == "" {
		fmt.Println("no interrupted transaction")
		return nil
	}
	fmt.Println(msg)
	return nil
}

// cmdMkIndex writes index.json for a binary package repository directory.
// With no argument the local binpkg cache is indexed, so the same command
// both seeds a private repository and prepares the cache for copying.
func cmdMkIndex(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	defaultDir := filepath.Join(cfg.Cache, "binpkgs")
	dir, opts, err := parseIndexArgs(args, defaultDir)
	if err != nil {
		return err
	}
	n, err := build.MakeIndexWithOptions(dir, opts)
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d packages in %s\n", n, dir)
	printIndexSummary(dir)
	return nil
}
