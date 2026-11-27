package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/depspec"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/resolve"
)

func installedRecs(cfg config.Config) ([]*pkgdb.Rec, error) {
	db := pkgdb.DB{Dir: cfg.DB}
	refs, err := db.List()
	if err != nil {
		return nil, err
	}
	recs := make([]*pkgdb.Rec, 0, len(refs))
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// resolvePlan is the single entry point for dependency resolution: recipe
// index, installed records (for conflict checks/preference), and the
// effective USE configuration.
func resolvePlan(cfg config.Config, targets []string) (*resolve.Result, error) {
	idx, err := resolve.NewIndex(cfg.Repos)
	if err != nil {
		return nil, err
	}
	recs, err := installedRecs(cfg)
	if err != nil {
		return nil, err
	}
	return (&resolve.Resolver{Index: idx, Options: cfg.Options, Installed: recs}).Resolve(targets)
}

// cmdDepends prints the build order. With -r/--reverse it prints installed
// packages that depend on the named packages (the safety view used by
// remove).
func cmdDepends(args []string) error {
	reverse := false
	var targets []string
	for _, a := range args {
		switch a {
		case "-r", "--reverse":
			reverse = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q", a)
			}
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("usage: zpkg depends [-r] <pkg>...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	if reverse {
		db := pkgdb.DB{Dir: cfg.DB}
		deps, err := reverseDependents(cfg, db, targets)
		if err != nil {
			return err
		}
		for _, d := range deps {
			fmt.Println(d)
		}
		return nil
	}
	plan, err := resolvePlan(cfg, targets)
	if err != nil {
		return err
	}
	for _, name := range plan.Order {
		fmt.Println(name)
	}
	return nil
}

// reverseDependents returns installed packages whose runtime or build
// dependencies refer to one of targets (by name or provides). Conditional
// dependencies are evaluated with the dependent's recorded USE flags.
func reverseDependents(cfg config.Config, db pkgdb.DB, targets []string) ([]string, error) {
	targetNames := map[string]bool{}
	targetBare := map[string]bool{}
	targetSlots := map[string]map[string]bool{}
	targetVersions := map[string][]string{}
	targetProvides := map[string]bool{}
	for _, t := range targets {
		a, err := depspec.Parse(t)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", t, err)
		}
		targetNames[a.Name] = true
		if a.Slot == "" {
			targetBare[a.Name] = true
		} else {
			if targetSlots[a.Name] == nil {
				targetSlots[a.Name] = map[string]bool{}
			}
			targetSlots[a.Name][a.Slot] = true
		}
		recs, err := db.GetAll(a.Name)
		if err == nil {
			for _, rec := range recs {
				if a.Slot != "" && rec.Slot != a.Slot {
					continue
				}
				targetVersions[a.Name] = append(targetVersions[a.Name], rec.VersionString())
			}
		}
		var r *recipe.Recipe
		if a.Slot != "" {
			r, err = recipe.LoadCandidate(cfg.Repos, pkgdb.SlotKey(a.Name, a.Slot))
		} else {
			r, err = recipe.Load(cfg.Repos, a.Name)
		}
		if err == nil {
			for _, raw := range r.Provides {
				p, err := depspec.ParseProvided(raw)
				if err == nil {
					targetProvides[p.Name] = true
					if p.Version != "" {
						targetVersions[p.Name] = append(targetVersions[p.Name], p.Version)
					}
				}
			}
		}
		// Installed metadata also carries provides from newer records.
		if recs, err := db.GetAll(a.Name); err == nil {
			for _, rec := range recs {
				if a.Slot != "" && rec.Slot != a.Slot {
					continue
				}
				if rec.Meta != nil {
					for _, raw := range rec.Meta.Provides {
						if p, err := depspec.ParseProvided(raw); err == nil {
							targetProvides[p.Name] = true
						}
					}
				}
			}
		}
	}

	refs, err := db.List()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, ref := range refs {
		if targetNames[ref.Name] &&
			(targetBare[ref.Name] || targetSlots[ref.Name][ref.Slot]) {
			continue // the package being removed is not its own reverse dep
		}
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return nil, err
		}
		rawDeps := []string{}
		useFlags := cfg.Options.ForPackage(ref.Name).Use
		if rec.Meta != nil {
			rawDeps = append(rawDeps, rec.Meta.Depends...)
			rawDeps = append(rawDeps, rec.Meta.BuildDeps...)
			if len(rec.Meta.UseFlags) > 0 {
				useFlags = rec.Meta.UseFlags
			}
		} else if r, err := recipe.LoadCandidate(cfg.Repos, pkgdb.SlotKey(ref.Name, ref.Slot)); err == nil {
			rawDeps = append(rawDeps, r.Depends...)
			rawDeps = append(rawDeps, r.MakeDepends...)
		}
		for _, raw := range rawDeps {
			a, err := depspec.Parse(raw)
			if err != nil {
				continue // historical metadata: ignore unparsable entries
			}
			if a.Condition != "" {
				enabled := contains(useFlags, a.Condition)
				if enabled == a.Negated {
					continue
				}
			}
			if !targetNames[a.Name] && !targetProvides[a.Name] {
				continue
			}
			if a.Slot != "" && targetSlots[a.Name] != nil && !targetSlots[a.Name][a.Slot] {
				continue
			}
			if vers := targetVersions[a.Name]; len(vers) > 0 {
				matched := false
				for _, v := range vers {
					if a.Matches(v) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			out[ref.String()] = true
		}
	}
	list := make([]string, 0, len(out))
	for n := range out {
		list = append(list, n)
	}
	sort.Strings(list)
	return list, nil
}
