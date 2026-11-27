// Package install applies destroots to the target root and removes packages.
// Every operation is journalled: an interrupted apply is rolled back and an
// interrupted removal is completed by the next zpkg run.
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
// The whole apply is one transaction: either the package ends up fully
// installed with its DB record, or the system is left as it was.
func Apply(rec *pkgdb.Rec, destroot, root string, db pkgdb.DB) error {
	if msg, err := Recover(root, db); err != nil {
		return err
	} else if msg != "" {
		fmt.Fprintln(os.Stderr, "zpkg:", msg)
	}

	// Reject any file owned by another package or another slot of the same
	// package (slots must not overlap files; same name+slot is an upgrade).
	owners, err := db.OwnerIndex()
	if err != nil {
		return err
	}
	for _, e := range rec.Entries {
		if e.Kind == "dir" || manifest.Ignored(e.Path) {
			continue
		}
		owner, ok := owners[e.Path]
		if ok && (owner.Name != rec.Name || owner.Slot != rec.Slot) {
			return fmt.Errorf("%s: already owned by %s", e.Path, owner.String())
		}
	}

	j := &txnJournal{Kind: "install", Name: rec.Name, Slot: rec.Slot, Version: rec.VersionString(), Phase: phaseInstalling}
	oldRec, oldErr := db.Get(rec.Name, rec.Slot)
	if oldErr == nil {
		j.HadRecord = true // upgrade: keep the old record if we must roll back
	}
	if err := saveJournalHeader(db, j); err != nil {
		return err
	}
	backup := filepath.Join(txnDir(db), "backup")

	// applyAll returns the first error; the deferred rollback puts the
	// system back the way it was before returning it.
	applyErr := func() error {
		for _, e := range rec.Entries {
			switch e.Kind {
			case "dir":
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(e.Path)), 0o755); err != nil {
					return err
				}
			case "file", "link":
				dst := filepath.Join(root, filepath.FromSlash(e.Path))
				if _, err := os.Lstat(dst); err == nil {
					// Something is already there: move it aside so a failure
					// later in the apply can put it back.
					bk := filepath.Join(backup, filepath.FromSlash(e.Path))
					if err := os.MkdirAll(filepath.Dir(bk), 0o755); err != nil {
						return err
					}
					// Record the move before making it: a crash between the
					// two leaves the event without a backup (nothing to
					// restore), never a backup the journal does not know.
					j.Replaced = append(j.Replaced, e.Path)
					if err := appendJournalEvent(db, "replaced", e.Path); err != nil {
						return err
					}
					if err := os.Rename(dst, bk); err != nil {
						return err
					}
				} else {
					j.Created = append(j.Created, e.Path)
					if err := appendJournalEvent(db, "created", e.Path); err != nil {
						return err
					}
				}
				if err := applyEntry(e, destroot, root); err != nil {
					return err
				}
			}
		}
		// An upgrade drops files the old version shipped but the new one no
		// longer does (e.g. a runlevel symlink removed from a recipe). They
		// are moved to the backup so a rollback restores them.
		if oldErr == nil {
			if err := pruneObsolete(oldRec, rec, root, backup, db, j); err != nil {
				return err
			}
		}
		j.Phase = phaseInstalled
		return saveJournalHeader(db, j)
	}()
	if applyErr != nil {
		if rbErr := rollbackInstall(root, db, j); rbErr != nil {
			return fmt.Errorf("%w (rollback also failed: %v — run zpkg recover)", applyErr, rbErr)
		}
		return applyErr
	}

	if err := db.Save(rec); err != nil {
		return err
	}
	// Committed: the journal and the backups are no longer needed.
	return os.RemoveAll(txnDir(db))
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
		// Write to a temp file and rename over the destination. Renaming is
		// allowed where truncating a busy (running) binary is not; this is
		// what lets zpkg replace bash - and itself - while running.
		tmp := dst + ".zpkg-new"
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, src)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
		// open(2) ignores the setuid/setgid bits, so apply the full mode
		// explicitly. Without this, setuid helpers (doas, sudo, passwd)
		// would be installed as plain executables.
		full := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		if err := os.Chmod(tmp, full); err != nil {
			os.Remove(tmp)
			return err
		}
		return os.Rename(tmp, dst)
	}
}

// pruneObsolete removes files that the previous version of a package listed
// in its manifest but the new version does not ship. Deletion is conservative:
// a file is only removed when it still matches the old manifest (hash for
// files, target for symlinks), so local edits survive an upgrade. Removed
// paths are recorded as replaced so a failed apply restores them.
func pruneObsolete(old, rec *pkgdb.Rec, root, backup string, db pkgdb.DB, j *txnJournal) error {
	keep := make(map[string]bool, len(rec.Entries))
	for _, e := range rec.Entries {
		keep[e.Path] = true
	}
	for _, e := range old.Entries {
		if e.Kind == "dir" || keep[e.Path] {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(e.Path))
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		switch e.Kind {
		case "link":
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(p)
			if err != nil || target != e.Link {
				continue
			}
		case "file":
			if !info.Mode().IsRegular() {
				continue
			}
			sum, err := hash.File(p)
			if err != nil || sum != e.Hash {
				continue // modified since install: leave it to the admin
			}
		default:
			continue
		}
		bk := filepath.Join(backup, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(bk), 0o755); err != nil {
			return err
		}
		// As in Apply: journal the move before making it.
		j.Replaced = append(j.Replaced, e.Path)
		if err := appendJournalEvent(db, "replaced", e.Path); err != nil {
			return err
		}
		if err := os.Rename(p, bk); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes the files of every slot of an installed package and their
// records. The removal is journalled, so an interrupted removal is finished
// by the next zpkg operation.
func Remove(name, root string, db pkgdb.DB) error {
	if msg, err := Recover(root, db); err != nil {
		return err
	} else if msg != "" {
		fmt.Fprintln(os.Stderr, "zpkg:", msg)
	}

	recs, err := db.GetAll(name)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if err := removeRecord(rec, root, db); err != nil {
			return err
		}
	}
	return os.RemoveAll(txnDir(db))
}

// RemoveSlot deletes one slot of an installed package. Other slots of the
// same name are left untouched.
func RemoveSlot(name, slot, root string, db pkgdb.DB) error {
	if msg, err := Recover(root, db); err != nil {
		return err
	} else if msg != "" {
		fmt.Fprintln(os.Stderr, "zpkg:", msg)
	}
	rec, err := db.Get(name, slot)
	if err != nil {
		return err
	}
	if err := removeRecord(rec, root, db); err != nil {
		return err
	}
	return os.RemoveAll(txnDir(db))
}

func removeRecord(rec *pkgdb.Rec, root string, db pkgdb.DB) error {
	if err := checkIntegrity(rec, root); err != nil {
		return err
	}
	var files []string
	for _, e := range rec.Entries {
		if e.Kind != "dir" {
			files = append(files, e.Path)
		}
	}
	j := &txnJournal{Kind: "remove", Name: rec.Name, Slot: rec.Slot, Version: rec.VersionString(), Phase: phaseRemoving, Files: files}
	if err := saveJournal(db, j); err != nil {
		return err
	}

	// Files and links first (entries are sorted by path, so reverse
	// iteration is deepest first), then prune now-empty directories.
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
		p := filepath.Join(root, filepath.FromSlash(e.Path))
		// Only prune real, empty directories. In particular /lib, /bin
		// and /sbin are symlinks in a usr-merged root: os.Remove would
		// delete the symlink itself, breaking every dynamically linked
		// binary (linux-firmware's "dir lib" did exactly this in M22).
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		os.Remove(p) // best effort: only prunes empty dirs
	}
	return db.Delete(rec.Name, rec.Slot)
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
