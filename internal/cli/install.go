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
		if o.pretend {
			if skip {
				fmt.Printf("skipping %s (already installed)\n", name)
				continue
			}
			if doInstall {
				fmt.Printf("would install %s %s\n", r.Name, r.FullVersion())
			} else {
				fmt.Printf("would build %s %s\n", r.Name, r.FullVersion())
			}
			continue
		}
		if skip {
			fmt.Printf("skipping %s (already installed)\n", name)
			continue
		}
		opts := cfg.Options.ForPackage(r.Name)
		res := build.LoadBinpkg(r, cfg)
		if res == nil {
			// No local cache hit: try a configured binary package
			// repository before falling back to a source build.
			res = build.LoadRepoBinpkg(r, cfg)
		}
		if res != nil {
			fmt.Printf("installed from binpkg %s %s (%d files)\n", r.Name, r.FullVersion(), len(res.Entries))
		} else {
			res, err = build.Run(r, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("built %s %s (%d files)\n", r.Name, r.FullVersion(), len(res.Entries))
			if pkgPath, err := build.PackBinpkg(r, res, cfg.Cache, opts.Hash()); err != nil {
				fmt.Fprintln(os.Stderr, "zpkg: binpkg pack failed:", err)
			} else {
				fmt.Printf("binpkg: %s\n", pkgPath)
			}
		}
		if !doInstall {
			fmt.Printf("destroot: %s\n", res.Destroot)
			continue
		}
		rec := &pkgdb.Rec{
			Name:     r.Name,
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
		if contains(targets, name) && !o.asdeps {
			if err := world.Add(cfg.DB, name); err != nil {
				return err
			}
		}
		if err := runHook(cfg, r, "post_install"); err != nil {
			return err
		}
		refreshLdconfig(cfg)
		fmt.Printf("installed %s %s\n", r.Name, r.FullVersion())
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
		fmt.Printf("removed %s\n", name)
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
