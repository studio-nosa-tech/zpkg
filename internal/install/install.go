// Package install applies destroots to the target root and removes packages.
package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

// Apply copies a built destroot into root and records the package in db.
func Apply(rec *pkgdb.Rec, destroot, root string, db pkgdb.DB) error {
	// Reject any file already owned by a different installed package.
	for _, e := range rec.Entries {
		if e.Kind == "dir" || manifest.Ignored(e.Path) {
			continue
		}
		owner, err := db.Owner(e.Path)
		if err != nil {
			return err
		}
		if owner != "" && owner != rec.Name {
			return fmt.Errorf("%s: already owned by %s", e.Path, owner)
		}
	}
	for _, e := range rec.Entries {
		if err := applyEntry(e, destroot, root); err != nil {
			return err
		}
	}
	return db.Save(rec)
}

func applyEntry(e manifest.Entry, destroot, root string) error {
	dst := filepath.Join(root, filepath.FromSlash(e.Path))
	switch e.Kind {
	case "dir":
		return os.MkdirAll(dst, 0o755)
	case "link":
		if _, err := os.Lstat(dst); err == nil {
			if err := os.Remove(dst); err != nil {
				return err
			}
		}
		return os.Symlink(e.Link, dst)
	default: // "file"
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		src, err := os.Open(filepath.Join(destroot, filepath.FromSlash(e.Path)))
		if err != nil {
			return err
		}
		defer src.Close()
		info, err := src.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, src)
		return err
	}
}

// Remove deletes the files of an installed package and its record.
func Remove(name, root string, db pkgdb.DB) error {
	rec, err := db.Get(name)
	if err != nil {
		return err
	}
	if err := checkIntegrity(rec, root); err != nil {
		return err
	}
	// Files and links first (entries are sorted by path, so reverse iteration
	// is deepest first), then prune now-empty directories.
	for i := len(rec.Entries) - 1; i >= 0; i-- {
		e := rec.Entries[i]
		if e.Kind == "dir" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(e.Path))); err != nil {
			return err
		}
	}
	for i := len(rec.Entries) - 1; i >= 0; i-- {
		e := rec.Entries[i]
		if e.Kind != "dir" {
			continue
		}
		os.Remove(filepath.Join(root, filepath.FromSlash(e.Path))) // best effort: only prunes empty dirs
	}
	return db.Delete(name)
}

func checkIntegrity(rec *pkgdb.Rec, root string) error {
	if os.Getenv("ZPKG_FORCE") == "1" {
		return nil
	}
	for _, e := range rec.Entries {
		if e.Kind != "file" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(e.Path))
		got, err := hash.File(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue // already gone; removal proceeds
			}
			return err
		}
		if got != e.Hash {
			return fmt.Errorf("%s: modified since install; remove aborted (set ZPKG_FORCE=1 to override)", e.Path)
		}
	}
	return nil
}
