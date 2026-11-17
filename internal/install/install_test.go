package install

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readTree(t *testing.T, root string, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func recFor(name, version string, files map[string]string) *pkgdb.Rec {
	var entries []manifest.Entry
	for rel, content := range files {
		entries = append(entries, manifest.Entry{Kind: "file", Path: rel, Hash: content})
	}
	// Deterministic order (Apply relies on sorted entries elsewhere).
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &pkgdb.Rec{Name: name, Version: version, Entries: entries}
}

func TestApplyCommit(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}
	files := map[string]string{"usr/bin/tool": "NEW", "etc/tool.conf": "cfg"}
	writeTree(t, dest, files)
	rec := recFor("tool", "1.0", files)

	if err := Apply(rec, dest, root, db); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, root, "usr/bin/tool"); got != "NEW" {
		t.Errorf("tool = %q", got)
	}
	if _, err := db.Get("tool", "0"); err != nil {
		t.Errorf("db record missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(db.Dir, ".txn")); !os.IsNotExist(err) {
		t.Errorf("txn dir still present: %v", err)
	}
}

func TestApplyRollbackOnFailure(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// An existing file that the new package replaces...
	writeTree(t, root, map[string]string{"usr/bin/tool": "OLD"})
	// ...and a landmine: a regular file where a later entry needs a directory.
	writeTree(t, root, map[string]string{"usr/lib/broken": "!"})

	files := map[string]string{
		"usr/bin/tool":     "NEW",
		"usr/lib/broken/x": "never applied",
	}
	writeTree(t, dest, files)
	rec := recFor("tool", "1.0", files)

	if err := Apply(rec, dest, root, db); err == nil {
		t.Fatal("expected the apply to fail on the landmine")
	}
	// The failed apply rolls back in place: the replaced file is back.
	if got := readTree(t, root, "usr/bin/tool"); got != "OLD" {
		t.Errorf("after failure tool = %q, want the original OLD", got)
	}
	if _, err := db.Get("tool", "0"); err == nil {
		t.Error("partial db record must not exist")
	}
	if _, err := os.Stat(filepath.Join(db.Dir, ".txn")); !os.IsNotExist(err) {
		t.Errorf("txn dir still present after in-place rollback: %v", err)
	}

	// Nothing is pending afterwards, so Recover has nothing to do.
	msg, err := Recover(root, db)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("unexpected recovery summary %q", msg)
	}
}

func TestApplyRollbackKeepsOldRecordOnUpgrade(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// An installed package that the new version would upgrade.
	old := recFor("tool", "1.0", map[string]string{"usr/bin/tool": "OLD"})
	writeTree(t, root, map[string]string{"usr/bin/tool": "OLD"})
	if err := db.Save(old); err != nil {
		t.Fatal(err)
	}

	writeTree(t, dest, map[string]string{
		"usr/bin/tool":     "NEW",
		"usr/lib/broken/x": "never applied",
	})
	writeTree(t, root, map[string]string{"usr/lib/broken": "!"})

	if err := Apply(recFor("tool", "2.0", map[string]string{
		"usr/bin/tool":     "NEW",
		"usr/lib/broken/x": "never applied",
	}), dest, root, db); err == nil {
		t.Fatal("expected the upgrade to fail")
	}
	if got := readTree(t, root, "usr/bin/tool"); got != "OLD" {
		t.Errorf("after failed upgrade tool = %q, want OLD", got)
	}
	got, err := db.Get("tool", "0")
	if err != nil {
		t.Fatal("the old record must survive the failed upgrade:", err)
	}
	if got.VersionString() != "1.0" {
		t.Errorf("record version = %q, want 1.0", got.VersionString())
	}
}

func TestRecoverNothing(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: t.TempDir()}
	msg, err := Recover(root, db)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("unexpected summary %q", msg)
	}
}

func TestRecoverCompletesInterruptedRemove(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}
	files := map[string]string{"usr/bin/a": "A", "usr/bin/b": "B"}
	writeTree(t, root, files)
	rec := recFor("gone", "1.0", files)
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash mid-removal: the journal exists and one file is
	// already gone.
	j := &txnJournal{
		Kind: "remove", Name: "gone", Version: "1.0", Phase: phaseRemoving,
		Files: []string{"usr/bin/a", "usr/bin/b"},
	}
	if err := saveJournal(db, j); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "usr/bin/a")); err != nil {
		t.Fatal(err)
	}

	msg, err := Recover(root, db)
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a completion summary")
	}
	for rel := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s still present after recovery", rel)
		}
	}
	if _, err := db.Get("gone", "0"); err == nil {
		t.Error("db record must be gone after completing the removal")
	}
	if _, err := os.Stat(filepath.Join(db.Dir, ".txn")); !os.IsNotExist(err) {
		t.Errorf("txn dir still present: %v", err)
	}
}

func TestSlotCoexistenceAndConflict(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// Slot 0 installs usr/lib/multi/v1.so, slot 1 installs usr/lib/multi/v2.so:
	// the two records coexist because their files do not overlap.
	packSlot := func(version, slot, rel string, destroot, content string) *pkgdb.Rec {
		writeTree(t, destroot, map[string]string{rel: content})
		sum, err := hash.File(filepath.Join(destroot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return &pkgdb.Rec{
			Name:    "multi",
			Slot:    slot,
			Version: version,
			Entries: []manifest.Entry{{Kind: "file", Path: rel, Hash: sum}},
			Meta:    &pkgdb.Meta{Slot: slot},
		}
	}
	dest := t.TempDir()
	rec0 := packSlot("1.0", "0", "usr/lib/multi/v1.so", dest, "V1")
	if err := Apply(rec0, dest, root, db); err != nil {
		t.Fatal(err)
	}
	dest = t.TempDir()
	rec1 := packSlot("2.0", "1", "usr/lib/multi/v2.so", dest, "V2")
	if err := Apply(rec1, dest, root, db); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, root, "usr/lib/multi/v1.so"); got != "V1" {
		t.Errorf("slot 0 file = %q", got)
	}
	if got := readTree(t, root, "usr/lib/multi/v2.so"); got != "V2" {
		t.Errorf("slot 1 file = %q", got)
	}

	// A third slot trying to write a file owned by slot 0 conflicts.
	dest = t.TempDir()
	rec2 := packSlot("3.0", "2", "usr/lib/multi/v1.so", dest, "V3")
	if err := Apply(rec2, dest, root, db); err == nil {
		t.Fatal("cross-slot file overlap must be rejected")
	} else if !strings.Contains(fmt.Sprint(err), "already owned") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Remove removes every slot.
	if err := Remove("multi", root, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("multi", "0"); err == nil {
		t.Error("slot 0 record still present after Remove")
	}
	if _, err := db.Get("multi", "1"); err == nil {
		t.Error("slot 1 record still present after Remove")
	}
	if _, err := os.Stat(filepath.Join(root, "usr/lib/multi/v1.so")); !os.IsNotExist(err) {
		t.Error("slot 0 file still present after Remove")
	}
}

func TestUpgradePrunesDroppedFiles(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// v1 ships a binary, a config file, a modified config and a runlevel
	// symlink; v2 keeps only the binary.
	oldFiles := map[string]string{
		"usr/bin/tool":      "OLD",
		"etc/tool.conf":     "drop me",
		"etc/local.conf":    "admin edit",
		"etc/runlevels/def": "placeholder",
	}
	oldDest := t.TempDir()
	writeTree(t, oldDest, oldFiles)
	writeTree(t, root, oldFiles)
	sum := func(rel string) string {
		t.Helper()
		s, err := hash.File(filepath.Join(oldDest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	oldEntries := []manifest.Entry{
		{Kind: "file", Path: "etc/local.conf", Hash: sum("etc/local.conf")},
		{Kind: "file", Path: "etc/runlevels/def", Hash: sum("etc/runlevels/def")},
		{Kind: "file", Path: "etc/tool.conf", Hash: sum("etc/tool.conf")},
		{Kind: "file", Path: "usr/bin/tool", Hash: sum("usr/bin/tool")},
	}

	// Symlinks need extra privileges on Windows; skip that part there.
	linkPath := "etc/runlevels/default/tool"
	linkChecked := false
	if err := os.MkdirAll(filepath.Join(root, "etc/runlevels/default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/init.d/tool", filepath.Join(root, filepath.FromSlash(linkPath))); err == nil {
		oldEntries = append(oldEntries, manifest.Entry{Kind: "link", Link: "/etc/init.d/tool", Path: linkPath})
		linkChecked = true
	}

	if err := db.Save(&pkgdb.Rec{Name: "tool", Slot: "0", Version: "1.0", Entries: oldEntries}); err != nil {
		t.Fatal(err)
	}

	// The admin edited etc/local.conf after installing v1.
	writeTree(t, root, map[string]string{"etc/local.conf": "changed"})

	newDest := t.TempDir()
	writeTree(t, newDest, map[string]string{"usr/bin/tool": "NEW"})
	newEntries, err := manifest.FromRoot(newDest)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(&pkgdb.Rec{Name: "tool", Slot: "0", Version: "2.0", Entries: newEntries}, newDest, root, db); err != nil {
		t.Fatal(err)
	}

	if got := readTree(t, root, "usr/bin/tool"); got != "NEW" {
		t.Errorf("tool = %q", got)
	}
	dropped := []string{"etc/tool.conf", "etc/runlevels/def"}
	if linkChecked {
		dropped = append(dropped, linkPath)
	}
	for _, rel := range dropped {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s survived the upgrade", rel)
		}
	}
	if got := readTree(t, root, "etc/local.conf"); got != "changed" {
		t.Errorf("locally modified file = %q, want it preserved", got)
	}
}

func TestRemoveKeepsSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// usr-merged layout: /lib is a symlink to usr/lib.
	if err := os.MkdirAll(filepath.Join(root, "usr/lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/lib", filepath.Join(root, "lib")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	// The package installed a file through /lib and its destroot carried
	// directory entries for the whole path, including "lib".
	writeTree(t, root, map[string]string{"usr/lib/firmware/x.bin": "fw"})
	sum, err := hash.File(filepath.Join(root, "usr/lib/firmware/x.bin"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &pkgdb.Rec{Name: "fw", Slot: "0", Version: "1.0", Entries: []manifest.Entry{
		{Kind: "dir", Path: "lib"},
		{Kind: "dir", Path: "usr"},
		{Kind: "dir", Path: "usr/lib"},
		{Kind: "dir", Path: "usr/lib/firmware"},
		{Kind: "file", Path: "usr/lib/firmware/x.bin", Hash: sum},
	}}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := Remove("fw", root, db); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(filepath.Join(root, "lib"))
	if err != nil {
		t.Fatalf("/lib disappeared: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("/lib is no longer a symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "usr/lib/firmware/x.bin")); !os.IsNotExist(err) {
		t.Error("firmware file survived removal")
	}
}

func TestRecoverInstallFromEventLog(t *testing.T) {
	root := t.TempDir()
	db := pkgdb.DB{Dir: filepath.Join(root, "var/lib/zpkg")}

	// State an interrupted install leaves behind: the old version of "old"
	// was moved to the backup and a partial new tree is in place.
	backup := filepath.Join(txnDir(db), "backup/usr/bin/old")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"usr/bin/old": "OLD"})
	if err := os.Rename(filepath.Join(root, "usr/bin/old"), backup); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"usr/bin/old": "NEW", "usr/bin/new": "NEW"})

	j := &txnJournal{Kind: "install", Name: "pkg", Version: "1.0", Phase: phaseInstalling}
	if err := saveJournalHeader(db, j); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []journalEvent{
		{Op: "created", Path: "usr/bin/new"},
		{Op: "replaced", Path: "usr/bin/old"},
	} {
		if err := appendJournalEvent(db, ev.Op, ev.Path); err != nil {
			t.Fatal(err)
		}
	}
	// A crash in the middle of an append leaves a torn last line. The file
	// operation it names never ran, so recovery must ignore it.
	f, err := os.OpenFile(eventsPath(db), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"created","path":"usr/bin/par`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	msg, err := Recover(root, db)
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a rollback summary")
	}
	if got := readTree(t, root, "usr/bin/old"); got != "OLD" {
		t.Errorf("old file restored as %q, want OLD", got)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/bin/new")); !os.IsNotExist(err) {
		t.Errorf("created file survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(db.Dir, ".txn")); !os.IsNotExist(err) {
		t.Errorf("txn dir still present: %v", err)
	}
}
