package install

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

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
	if _, err := db.Get("tool"); err != nil {
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
	if _, err := db.Get("tool"); err == nil {
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
	got, err := db.Get("tool")
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
	if _, err := db.Get("gone"); err == nil {
		t.Error("db record must be gone after completing the removal")
	}
	if _, err := os.Stat(filepath.Join(db.Dir, ".txn")); !os.IsNotExist(err) {
		t.Errorf("txn dir still present: %v", err)
	}
}
