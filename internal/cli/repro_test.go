package cli

import (
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func TestDiffEntries(t *testing.T) {
	a := []manifest.Entry{
		{Kind: "file", Path: "usr/bin/hello", Hash: "aaa"},
		{Kind: "dir", Path: "usr/share/hello"},
		{Kind: "link", Path: "usr/bin/hi", Link: "hello"},
	}
	b := []manifest.Entry{
		{Kind: "file", Path: "usr/bin/hello", Hash: "bbb"},
		{Kind: "dir", Path: "usr/share/hello"},
		{Kind: "file", Path: "usr/share/hello/data", Hash: "ccc"},
	}
	diffs := diffEntries(a, b)
	want := map[string]bool{"~ usr/bin/hello": true, "- usr/bin/hi": true, "+ usr/share/hello/data": true}
	if len(diffs) != len(want) {
		t.Fatalf("diffs = %v", diffs)
	}
	for _, d := range diffs {
		if !want[d] {
			t.Fatalf("unexpected diff %q", d)
		}
	}
	if len(diffEntries(a, a)) != 0 {
		t.Fatal("identical manifests differ")
	}
}

func TestDepComponents(t *testing.T) {
	db := pkgdb.DB{Dir: t.TempDir()}
	for _, name := range []string{"openssl", "zlib"} {
		rec := &pkgdb.Rec{Name: name, Version: "1.0", Revision: 2, Slot: "0",
			Meta: &pkgdb.Meta{Name: name, Version: "1.0", Revision: 2, Slot: "0"}}
		if err := db.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	atoms := []string{"openssl", "ssl?zlib", "missing"}
	got := depComponents(db, atoms, nil)
	if len(got) != 1 || got[0].Name != "openssl" || got[0].Version != "1.0-r2" {
		t.Fatalf("without ssl = %+v", got)
	}
	got = depComponents(db, atoms, []string{"ssl"})
	if len(got) != 2 || got[0].Name != "openssl" || got[1].Name != "zlib" {
		t.Fatalf("with ssl = %+v", got)
	}
	// The inverse form applies when the flag is disabled.
	if got := depComponents(db, []string{"!ssl?zlib"}, nil); len(got) != 1 || got[0].Name != "zlib" {
		t.Fatalf("inverse without ssl = %+v", got)
	}
	if got := depComponents(db, []string{"!ssl?zlib"}, []string{"ssl"}); len(got) != 0 {
		t.Fatalf("inverse with ssl = %+v", got)
	}
}
