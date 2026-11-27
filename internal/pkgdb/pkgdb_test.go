package pkgdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
)

func TestListIgnoresNonPackageDirs(t *testing.T) {
	// Overlay trees live next to the records under var/lib/zpkg/overlays;
	// List must only report directories that are actual package records.
	db := DB{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(db.Dir, "overlays", "menu", "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(db.Dir, "tool@1")
	if err := os.MkdirAll(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "version"), []byte("1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].String() != "tool@1" {
		t.Fatalf("refs = %v", refs)
	}
}

func TestSaveGetRoundtrip(t *testing.T) {
	db := DB{Dir: t.TempDir()}
	rec := &Rec{
		Name:     "vim",
		Version:  "9.2.1355",
		Revision: 2,
		Entries:  []manifest.Entry{{Kind: "file", Path: "usr/bin/vim", Hash: "x"}},
		Meta: &Meta{
			Slot:      "0",
			Repo:      "/repo/main",
			Arch:      "amd64",
			Installed: "2026-11-16T00:00:00Z",
			Depends:   []string{"ncurses"},
		},
	}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}

	// The v1-compatible version file carries the revision.
	vb, err := os.ReadFile(filepath.Join(db.Dir, "vim", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(vb); got != "9.2.1355-r2\n" {
		t.Fatalf("version file = %q", got)
	}

	got, err := db.Get("vim", "0")
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionString() != "9.2.1355-r2" {
		t.Errorf("VersionString = %q", got.VersionString())
	}
	if got.Meta == nil {
		t.Fatal("Meta is nil after roundtrip")
	}
	if got.Meta.Schema != Schema {
		t.Errorf("schema = %d, want %d", got.Meta.Schema, Schema)
	}
	if got.Meta.Repo != "/repo/main" || got.Meta.Slot != "0" {
		t.Errorf("meta = %+v", got.Meta)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "usr/bin/vim" {
		t.Errorf("entries = %+v", got.Entries)
	}
}

func TestGetV1Record(t *testing.T) {
	db := DB{Dir: t.TempDir()}
	dir := filepath.Join(db.Dir, "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte("1.0-r4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(filepath.Join(dir, "manifest"), nil); err != nil {
		t.Fatal(err)
	}
	rec, err := db.Get("old", "0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Meta != nil {
		t.Error("v1 record should have no Meta")
	}
	if rec.Version != "1.0" || rec.Revision != 4 {
		t.Errorf("parsed = %q r%d, want 1.0 r4", rec.Version, rec.Revision)
	}
	if rec.VersionString() != "1.0-r4" {
		t.Errorf("VersionString = %q", rec.VersionString())
	}
}

func TestOwnerIndex(t *testing.T) {
	db := DB{Dir: t.TempDir()}
	for _, rec := range []*Rec{
		{Name: "one", Version: "1", Entries: []manifest.Entry{
			{Kind: "file", Path: "usr/bin/one", Hash: "x"},
			{Kind: "file", Path: "usr/share/one", Hash: "x"},
		}},
		{Name: "two", Version: "1", Entries: []manifest.Entry{
			{Kind: "file", Path: "usr/bin/two", Hash: "x"},
		}},
	} {
		if err := db.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := db.OwnerIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := idx["usr/bin/one"].Name; got != "one" {
		t.Errorf("usr/bin/one owner = %q", got)
	}
	if got := idx["usr/bin/two"].Name; got != "two" {
		t.Errorf("usr/bin/two owner = %q", got)
	}
	if _, ok := idx["usr/bin/missing"]; ok {
		t.Error("missing path should not be indexed")
	}
}
