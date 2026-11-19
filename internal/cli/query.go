package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

func cmdList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	refs, err := db.List()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", ref.String(), rec.VersionString())
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
	seen := map[string]bool{}
	for _, repo := range cfg.Repos {
		items, err := os.ReadDir(repo)
		if err != nil {
			return err
		}
		for _, it := range items {
			if !it.IsDir() || strings.HasPrefix(it.Name(), ".") || seen[it.Name()] {
				continue
			}
			seen[it.Name()] = true
			r, err := recipe.Load(cfg.Repos, it.Name())
			if err != nil {
				return err
			}
			nameHit := strings.Contains(r.Name, term)
			providesHit := ""
			for _, p := range r.Provides {
				if strings.Contains(p, term) {
					providesHit = p
					break
				}
			}
			if !nameHit && providesHit == "" {
				continue
			}
			if nameHit {
				fmt.Printf("%s %s %s\n", filepath.Base(repo), r.Name, r.FullVersion())
			} else {
				fmt.Printf("%s %s %s (provides %s)\n", filepath.Base(repo), r.Name, r.FullVersion(), providesHit)
			}
		}
	}
	return nil
}

// cmdInfo shows the installed record (if any) and the available recipe.
func cmdInfo(names []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for _, name := range names {
		recs, gerr := db.GetAll(name)
		fmt.Println(name + ":")
		if gerr == nil {
			for _, rec := range recs {
				line := fmt.Sprintf("  installed: %s", rec.VersionString())
				if rec.Meta != nil {
					if rec.Meta.Slot != "" {
						line += fmt.Sprintf(" (slot %s", rec.Meta.Slot)
						if rec.Meta.Repo != "" {
							line += ", repo " + filepath.Base(rec.Meta.Repo)
						}
						if rec.Meta.Installed != "" {
							line += ", " + rec.Meta.Installed
						}
						line += ")"
					}
				}
				fmt.Println(line)
				if rec.Meta != nil && len(rec.Meta.UseFlags) > 0 {
					fmt.Printf("  useflags: %s\n", joinDeps(rec.Meta.UseFlags))
				}
				if rec.Meta != nil && rec.Meta.Origin != "" {
					line := "  origin: " + rec.Meta.Origin
					if rec.Meta.OriginRepo != "" {
						line += " " + rec.Meta.OriginRepo
					}
					if rec.Meta.OriginKeyID != "" {
						line += fmt.Sprintf(" (key %s", rec.Meta.OriginKeyID)
						if rec.Meta.OriginGeneration > 0 {
							line += fmt.Sprintf(", generation %d", rec.Meta.OriginGeneration)
						}
						line += ")"
					}
					fmt.Println(line)
					if rec.Meta.OriginPkgSHA256 != "" {
						fmt.Printf("  package sha256: %s\n", rec.Meta.OriginPkgSHA256)
					}
				}
				if rec.Meta != nil && rec.Meta.BuildSandbox != "" {
					line := "  build: " + rec.Meta.BuildSandbox
					if rec.Meta.BuildNetwork != "" {
						line += ", network " + rec.Meta.BuildNetwork
					}
					if rec.Meta.BuildEnvCount > 0 {
						line += fmt.Sprintf(", env %d packages", rec.Meta.BuildEnvCount)
						if rec.Meta.BuildEnvHash != "" {
							line += " (" + rec.Meta.BuildEnvHash[:12] + "...)"
						}
					}
					fmt.Println(line)
				}
				recordDir := filepath.Join(db.Dir, pkgdb.SlotKey(rec.Name, rec.Slot))
				if info, err := provenance.Read(filepath.Join(recordDir, "provenance.json")); err == nil {
					if info.Recipe.Digest != "" {
						digest := info.Recipe.Digest
						if len(digest) > 12 {
							digest = digest[:12] + "..."
						}
						line := "  provenance: recipe " + digest
						if info.Build.SourceDateEpoch > 0 {
							line += fmt.Sprintf(", source_date_epoch %d", info.Build.SourceDateEpoch)
						}
						fmt.Println(line)
					}
					if info.Log.Path != "" {
						fmt.Printf("  build log: %s\n", info.Log.Path)
					}
				}
				if fileExists(filepath.Join(recordDir, "sbom.json")) {
					fmt.Println("  sbom: SPDX 2.3")
				}
			}
		} else {
			fmt.Println("  installed: -")
		}

		if len(cfg.Repos) == 0 {
			continue
		}
		r, err := recipe.Load(cfg.Repos, name)
		if err != nil {
			if gerr != nil {
				return err
			}
			fmt.Println("  available: (no recipe in the configured repos)")
			continue
		}
		fmt.Printf("  available: %s (slot %s, repo %s)\n", r.FullVersion(), r.Slot, filepath.Base(r.Repo))
		if len(r.Depends) > 0 {
			fmt.Printf("  depends: %s\n", joinDeps(r.Depends))
		}
		if len(r.MakeDepends) > 0 {
			fmt.Printf("  makedepends: %s\n", joinDeps(r.MakeDepends))
		}
		if len(r.Provides) > 0 {
			fmt.Printf("  provides: %s\n", joinDeps(r.Provides))
		}
	}
	return nil
}

// cmdOwner reports which package owns a path. Paths are accepted with or
// without a leading slash; the manifest stores them relative to /.
func cmdOwner(paths []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	for _, p := range paths {
		rel := strings.TrimPrefix(filepath.ToSlash(p), "/")
		owner, err := db.Owner(rel)
		if err != nil {
			return err
		}
		if owner.Name == "" {
			fmt.Printf("%s: no owner\n", p)
			continue
		}
		fmt.Printf("%s: %s\n", p, owner.String())
	}
	return nil
}
