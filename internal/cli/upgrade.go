package cli

import (
	"fmt"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/version"
	"github.com/studio-nosa-tech/zpkg/internal/world"
)

// cmdUpgrade brings the world set (or the named packages) up to the
// versions the recipe tree offers. With --newuse a package is also rebuilt
// when its effective USE flags changed since installation.
func cmdUpgrade(targets []string, o installOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}

	names := targets
	if len(names) == 0 {
		names, err = world.Read(cfg.DB)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println("world is empty; install something first")
			return nil
		}
	}

	var upgradable []string
	for _, name := range names {
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			fmt.Printf("%s: %v\n", name, err)
			continue
		}
		rec, err := db.Get(name)
		if err != nil {
			fmt.Printf("%s: not installed (use zpkg install)\n", name)
			continue
		}
		behind := version.Compare(rec.VersionString(), r.FullVersion()) < 0
		changed := ""
		if o.newuse && useFlagsChanged(rec, name, cfg) {
			changed = " (use flags changed)"
		}
		if !behind && changed == "" {
			if len(targets) > 0 {
				fmt.Printf("%s %s is up to date\n", name, rec.VersionString())
			}
			continue
		}
		if behind {
			fmt.Printf("%s: %s -> %s%s\n", name, rec.VersionString(), r.FullVersion(), changed)
		} else {
			fmt.Printf("%s: rebuilding%s\n", name, changed)
		}
		upgradable = append(upgradable, name)
	}
	if len(upgradable) == 0 {
		if len(targets) == 0 {
			fmt.Println("nothing to upgrade")
		}
		return nil
	}
	o.force = true
	return cmdBuildInstall(upgradable, true, o)
}

// useFlagsChanged reports whether the effective USE flags of name differ
// from the ones recorded at install time. v1 records (no meta) never count
// as changed.
func useFlagsChanged(rec *pkgdb.Rec, name string, cfg config.Config) bool {
	if rec.Meta == nil {
		return false
	}
	current := cfg.Options.ForPackage(name).Use
	installed := rec.Meta.UseFlags
	if len(current) != len(installed) {
		return true
	}
	for i := range current {
		if current[i] != installed[i] {
			return true
		}
	}
	return false
}

// cmdWorld prints the packages installed explicitly.
func cmdWorld() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	names, err := world.Read(cfg.DB)
	if err != nil {
		return err
	}
	for _, n := range names {
		if rec, err := db.Get(n); err == nil {
			fmt.Printf("%s %s\n", n, rec.VersionString())
			continue
		}
		fmt.Printf("%s (not installed)\n", n)
	}
	return nil
}
