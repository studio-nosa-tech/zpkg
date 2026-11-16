// Transaction support: a journal of in-progress installs and removals so a
// crash (power loss, kill, build failure mid-apply) never leaves the system
// in an unnamed state. The next operation — or an explicit "zpkg recover" —
// rolls an interrupted install back and completes an interrupted removal.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

const (
	phaseInstalling = "installing" // files are being copied
	phaseInstalled  = "installed"  // files copied, DB record not yet written
	phaseRemoving   = "removing"   // files are being deleted
)

// txnJournal is the on-disk record of an in-progress transaction.
type txnJournal struct {
	Kind      string   `json:"kind"` // "install" | "remove"
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Phase     string   `json:"phase"`
	HadRecord bool     `json:"hadrecord"` // a record of Name existed before the install (upgrade)
	Replaced  []string `json:"replaced"`  // install: existing paths moved aside
	Created   []string `json:"created"`   // install: paths that did not exist before
	Files     []string `json:"files"`     // remove: every path to delete
}

func txnDir(db pkgdb.DB) string { return filepath.Join(db.Dir, ".txn") }
func journalPath(db pkgdb.DB) string {
	return filepath.Join(txnDir(db), "journal.json")
}

func saveJournal(db pkgdb.DB, j *txnJournal) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(txnDir(db), 0o755); err != nil {
		return err
	}
	return writeAtomic(journalPath(db), data)
}

// Recover checks for an interrupted transaction and repairs the state:
//
//   - an interrupted install is rolled back (replaced files restored from
//     the backup, created files deleted, the partial DB record removed)
//   - an interrupted removal is completed (remaining files deleted, the
//     DB record removed) — removal is not reversible, completion is.
//
// It returns a human-readable summary, or "" when nothing was pending.
func Recover(root string, db pkgdb.DB) (string, error) {
	data, err := os.ReadFile(journalPath(db))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var j txnJournal
	if err := json.Unmarshal(data, &j); err != nil {
		// Unreadable journal: keep the backup dir for forensics but move it
		// out of the way so zpkg can run again.
		side := txnDir(db) + ".corrupt"
		os.RemoveAll(side)
		if err := os.Rename(txnDir(db), side); err != nil {
			return "", err
		}
		return "moved an unreadable transaction journal aside (.txn.corrupt)", nil
	}

	switch j.Kind {
	case "install":
		if err := rollbackInstall(root, db, &j); err != nil {
			return "", err
		}
		return fmt.Sprintf("rolled back the interrupted install of %s %s", j.Name, j.Version), nil
	case "remove":
		if err := completeRemove(root, db, &j); err != nil {
			return "", err
		}
		return fmt.Sprintf("completed the interrupted removal of %s %s", j.Name, j.Version), nil
	default:
		os.RemoveAll(txnDir(db))
		return "cleared an unknown transaction journal", nil
	}
}

func rollbackInstall(root string, db pkgdb.DB, j *txnJournal) error {
	backup := filepath.Join(txnDir(db), "backup")
	for _, p := range j.Replaced {
		bk := filepath.Join(backup, filepath.FromSlash(p))
		if _, err := os.Lstat(bk); err != nil {
			continue // no backup taken yet
		}
		dst := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(bk, dst); err != nil {
			return err
		}
	}
	for _, p := range j.Created {
		os.Remove(filepath.Join(root, filepath.FromSlash(p)))
	}
	// On a fresh install there is no record to keep. On a failed upgrade the
	// old record is untouched and matches the restored (old) files, so it
	// stays.
	if !j.HadRecord {
		if err := db.Delete(j.Name); err != nil {
			return err
		}
	}
	return os.RemoveAll(txnDir(db))
}

func completeRemove(root string, db pkgdb.DB, j *txnJournal) error {
	for _, p := range j.Files {
		os.RemoveAll(filepath.Join(root, filepath.FromSlash(p)))
	}
	if err := db.Delete(j.Name); err != nil {
		return err
	}
	return os.RemoveAll(txnDir(db))
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
