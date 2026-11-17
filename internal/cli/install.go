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
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/world"
)

// metaFromRecipe captures the provenance of an install so later upgrades
// and queries do not need to re-read the recipe tree.
func metaFromRecipe(r *recipe.Recipe, use []string) *pkgdb.Meta {
	return &pkgdb.Meta{
		Schema:    pkgdb.Schema,
		Name:      r.Name,
		Version:   r.Version,
		Revision:  r.Revision,
		Slot:      r.Slot,
		Repo:      r.Repo,
		Arch:      runtime.GOARCH,
		Installed: time.Now().UTC().Format(time.RFC3339),
		UseFlags:  append([]string{}, use...),
		Depends:   append([]string{}, r.Depends...),
		BuildDeps: append([]string{}, r.MakeDepends...),
	}
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
	if err := requireRepos(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Cache, 0o755); err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	installed := map[string]bool{} // keyed by name@slot
	refs, err := db.List()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		installed[pkgdb.SlotKey(ref.Name, ref.Slot)] = true
	}

	order, err := resolve(cfg, targets)
	if err != nil {
		return err
	}
	// Load every recipe first: the pre-build plan and the "(i of n)" in the
	// progress markers need the list of packages that will actually be
	// handled (skipped ones do not count).
	var jobs []installJob
	for _, name := range order {
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return err
		}
		// A record exists for this name AND slot: it is an upgrade. Other
		// slots of the same name stay installed (that is the point of
		// slots). With force every named target is rebuilt and
		// reinstalled; dependencies that are already present are still
		// skipped so only the requested packages are replaced.
		skip := installed[pkgdb.SlotKey(name, r.Slot)] && !(o.force && contains(targets, name))
		if skip {
			phase("Skipping %s (already installed)", pkgLabel(r))
			continue
		}
		jobs = append(jobs, installJob{r: r, target: contains(targets, name)})
	}

	if len(jobs) > 0 {
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

	for i, jb := range jobs {
		r := jb.r
		label := pkgLabel(r)
		start := time.Now()
		opts := cfg.Options.ForPackage(r.Name)

		res := build.LoadBinpkg(r, cfg)
		if res == nil {
			// No local cache hit: try a configured binary package
			// repository before falling back to a source build.
			res = build.LoadRepoBinpkg(r, cfg)
		}
		fromBinpkg := res != nil
		if res == nil {
			phase("Building %s", label)
			res, err = build.Run(r, cfg)
			if err != nil {
				return err
			}
			phase("Built %s (%d files) in %s", label, len(res.Entries), elapsed(time.Since(start)))
			if pkgPath, err := build.PackBinpkg(r, res, cfg.Cache, opts.Hash()); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: binpkg pack failed:", err)
			} else {
				phase("Packed binpkg %s", pkgPath)
			}
		}
		if !doInstall {
			phase("Destroot %s", res.Destroot)
			continue
		}
		if fromBinpkg {
			phase("Installing (%d of %d) %s (binpkg)", i+1, len(jobs), label)
		} else {
			phase("Installing (%d of %d) %s", i+1, len(jobs), label)
		}
		rec := &pkgdb.Rec{
			Name:     r.Name,
			Slot:     r.Slot,
			Version:  r.Version,
			Revision: r.Revision,
			Entries:  res.Entries,
			Meta:     metaFromRecipe(r, opts.Use),
		}
		if err := install.Apply(rec, res.Destroot, cfg.Root, db); err != nil {
			return err
		}
		// Only packages named on the command line become world members;
		// dependencies are tracked but not part of the world set.
		if contains(targets, r.Name) && !o.asdeps {
			if err := world.Add(cfg.DB, r.Name); err != nil {
				return err
			}
		}
		if err := runHook(cfg, r, "post_install"); err != nil {
			return err
		}
		refreshLdconfig(cfg)
		phase("Installed %s", label)
	}
	return nil
}

func cmdRemove(names []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
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
	dir := cfg.Cache + "/binpkgs"
	if len(args) > 0 {
		dir = args[0]
	}
	n, err := build.MakeIndex(dir)
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d packages in %s\n", n, dir)
	return nil
}
