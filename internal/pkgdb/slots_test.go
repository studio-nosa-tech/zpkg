package pkgdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/manifest"
)

func saveRec(t *testing.T, db DB, name, slot, version, file string) {
	t.Helper()
	rec := &Rec{
		Name:    name,
		Slot:    slot,
		Version: version,
		Entries: []manifest.Entry{{Kind: "file", Path: file, Hash: "x"}},
		Meta:    &Meta{Slot: slot},
	}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}
}

func TestSlotKeys(t *testing.T) {
	if SlotKey("vim", "0") != "vim" || SlotKey("vim", "") != "vim" {
		t.Error("slot 0 must keep the legacy plain-name layout")
	}
	if SlotKey("gcc", "14") != "gcc@14" {
		t.Error("non-zero slots must get name@slot")
	}
	name, slot := SplitKey("gcc@14")
	if name != "gcc" || slot != "14" {
		t.Errorf("SplitKey = (%q, %q)", name, slot)
	}
	name, slot = SplitKey("vim")
	if name != "vim" || slot != "0" {
		t.Errorf("SplitKey = (%q, %q)", name, slot)
	}
}

func TestSlotCoexistence(t *testing.T) {
	db := DB{Dir: t.TempDir()}
	saveRec(t, db, "multi", "0", "1.0", "usr/lib/multi/v1.so")
	saveRec(t, db, "multi", "1", "2.0", "usr/lib/multi/v2.so")

	// Legacy layout: slot 0 is a plain dir, slot 1 is name@1.
	for _, key := range []string{"multi", "multi@1"} {
		if _, err := os.Stat(filepath.Join(db.Dir, key)); err != nil {
			t.Errorf("record dir %s missing: %v", key, err)
		}
	}

	refs, err := db.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("List = %+v, want 2 refs", refs)
	}

	r0, err := db.Get("multi", "0")
	if err != nil {
		t.Fatal(err)
	}
	if r0.Version != "1.0" || r0.Slot != "0" {
		t.Errorf("slot 0 = %s (slot %s)", r0.Version, r0.Slot)
	}
	r1, err := db.Get("multi", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Version != "2.0" || r1.Slot != "1" {
		t.Errorf("slot 1 = %s (slot %s)", r1.Version, r1.Slot)
	}

	// Owner reports the slot so install can reject cross-slot overlaps.
	ref, err := db.Owner("usr/lib/multi/v1.so")
	if err != nil || ref.Name != "multi" || ref.Slot != "0" {
		t.Errorf("Owner = %+v, want multi@0", ref)
	}
	ref, _ = db.Owner("usr/lib/multi/v2.so")
	if ref.Slot != "1" {
		t.Errorf("Owner = %+v, want multi@1", ref)
	}

	// GetAll returns every slot.
	recs, err := db.GetAll("multi")
	if err != nil || len(recs) != 2 {
		t.Fatalf("GetAll = %d records, want 2", len(recs))
	}

	// Removing one slot keeps the other.
	if err := db.Delete("multi", "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("multi", "1"); err != nil {
		t.Error("slot 1 must survive slot 0 removal")
	}
}
