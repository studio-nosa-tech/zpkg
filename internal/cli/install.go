package cli

import (
	"fmt"
	"os"
	"runtime"
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
		skip := installed[name] && !(o.force && contains(targets, name))
		if o.pretend {
			if skip {
				fmt.Printf("skipping %s (already installed)\n", name)
				continue
			}
			r, err := recipe.Load(cfg.Repos, name)
			if err != nil {
				return err
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
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			return err
		}
		res := build.LoadBinpkg(r, cfg)
		if res != nil {
			fmt.Printf("installed from binpkg %s %s (%d files)\n", r.Name, r.FullVersion(), len(res.Entries))
		} else {
			res, err = build.Run(r, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("built %s %s (%d files)\n", r.Name, r.FullVersion(), len(res.Entries))
			if pkgPath, err := build.PackBinpkg(r, res, cfg.Cache, cfg.Options.Hash()); err != nil {
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
			Meta:     metaFromRecipe(r, cfg.Options.Use),
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
		if err := install.Remove(name, cfg.Root, db); err != nil {
			return err
		}
		if err := world.Remove(cfg.DB, name); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", name)
	}
	return nil
}
