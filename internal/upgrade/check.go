package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/resolve"
)

// CheckReport summarises a post-upgrade integrity check.
type CheckReport struct {
	Packages int
	Files    int
}

// Check re-resolves the target set and verifies every package in the
// resulting closure. Structural checks (path, kind, link target, version)
// always run; full=true also hashes every file.
func Check(cfg config.Config, db pkgdb.DB, plan *Plan, full bool) (CheckReport, error) {
	var report CheckReport
	var issues []string

	idx, err := resolve.NewIndex(cfg.Repos)
	if err != nil {
		return report, err
	}
	recs, err := installedRecords(db)
	if err != nil {
		return report, err
	}
	res, err := (&resolve.Resolver{Index: idx, Options: cfg.Options, Installed: recs, Prefer: resolve.PreferNewest}).Resolve(plan.Targets)
	if err != nil {
		issues = append(issues, "dependency resolution: "+err.Error())
	} else {
		for _, key := range res.Order {
			c := res.Selected[key]
			if c == nil || c.Recipe == nil {
				continue
			}
			rec, err := db.Get(c.Name, c.Slot)
			if err != nil {
				issues = append(issues, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			report.Packages++
			report.Files += len(rec.Entries)
			for _, e := range rec.Entries {
				if msg := verifyEntry(cfg.Root, e, full); msg != "" {
					issues = append(issues, key+": "+msg)
				}
			}
		}
	}

	// Also verify that every planned package reached its expected version,
	// even when the re-resolution above failed.
	for _, e := range plan.Entries {
		rec, err := db.Get(e.Name, e.Slot)
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: not installed after upgrade", e.Name))
			continue
		}
		if rec.VersionString() != e.NewVersion {
			issues = append(issues, fmt.Sprintf("%s: installed %s, expected %s", e.Name, rec.VersionString(), e.NewVersion))
		}
	}

	if len(issues) > 0 {
		if len(issues) > 40 {
			issues = append(issues[:40], fmt.Sprintf("... and %d more", len(issues)-40))
		}
		return report, fmt.Errorf("integrity check failed:\n  %s", strings.Join(issues, "\n  "))
	}
	return report, nil
}

func verifyEntry(root string, e manifest.Entry, full bool) string {
	p := filepath.Join(root, filepath.FromSlash(e.Path))
	switch e.Kind {
	case "dir":
		fi, err := os.Stat(p)
		if err != nil {
			return e.Path + ": missing"
		}
		if !fi.IsDir() {
			return e.Path + ": not a directory"
		}
	case "link":
		fi, err := os.Lstat(p)
		if err != nil {
			return e.Path + ": missing"
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return e.Path + ": not a symlink"
		}
		target, err := os.Readlink(p)
		if err != nil {
			return e.Path + ": " + err.Error()
		}
		if target != e.Link {
			return fmt.Sprintf("%s: link target %q, expected %q", e.Path, target, e.Link)
		}
	default:
		fi, err := os.Lstat(p)
		if err != nil {
			return e.Path + ": missing"
		}
		if !fi.Mode().IsRegular() {
			return e.Path + ": not a regular file"
		}
		if full && e.Hash != "" {
			sum, err := hash.File(p)
			if err != nil {
				return e.Path + ": " + err.Error()
			}
			if sum != e.Hash {
				return fmt.Sprintf("%s: sha256 %s, expected %s", e.Path, sum, e.Hash)
			}
		}
	}
	return ""
}
