package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

func TestPackageBaseIncludesSlot(t *testing.T) {
	base := &recipe.Recipe{Name: "foo", Version: "1.2", Revision: 3}
	if got := PackageBase(base, "abc"); got != "foo-1.2-r3-abc" {
		t.Fatalf("slot 0 base = %q", got)
	}
	base.Slot = "14"
	if got := PackageBase(base, "abc"); got != "foo@14-1.2-r3-abc" {
		t.Fatalf("slot 14 base = %q", got)
	}
}

func writeBinpkgFixture(t *testing.T, dir, base, name, slot, version string) {
	t.Helper()
	m := BinMeta{Name: name, Slot: slot, Version: version, Arch: runtime.GOARCH}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".pkg.tar.gz", ".manifest", ".build.json", ".spdx.json"} {
		if err := os.WriteFile(filepath.Join(dir, base+suffix), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCleanOldBinpkgsKeepsOtherSlots(t *testing.T) {
	cache := t.TempDir()
	cfg := config.Config{Cache: cache}
	dir := filepath.Join(cache, "binpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBinpkgFixture(t, dir, "foo-1.0", "foo", "0", "1.0")
	writeBinpkgFixture(t, dir, "foo@14-1.0", "foo", "14", "1.0")
	writeBinpkgFixture(t, dir, "foo@14-2.0", "foo", "14", "2.0")

	r := &recipe.Recipe{Name: "foo", Slot: "14", Version: "2.0"}
	if err := CleanOldBinpkgs(r, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "foo@14-1.0.pkg.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("old slot 14 binpkg was not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "foo-1.0.pkg.tar.gz")); err != nil {
		t.Fatalf("slot 0 binpkg was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "foo@14-2.0.pkg.tar.gz")); err != nil {
		t.Fatalf("current slot 14 binpkg was removed: %v", err)
	}
}

func TestIndexEntryCarriesSlot(t *testing.T) {
	dir := t.TempDir()
	base := "foo@14-1.0"
	pkg := filepath.Join(dir, base+".pkg.tar.gz")
	if err := os.WriteFile(pkg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBinpkgFixture(t, dir, base, "foo", "14", "1.0")
	entry, err := indexEntry(dir, pkgFile{full: pkg, rel: base + ".pkg.tar.gz"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Slot != "14" {
		t.Fatalf("entry slot = %q", entry.Slot)
	}
	if entry.Name != "foo" || entry.Version != "1.0" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestValidateIndexRejectsDuplicateSlotEntry(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	sha := hex.EncodeToString(sum[:])
	e := IndexEntry{
		Name: "foo", Slot: "14", Version: "1.0",
		Arch: runtime.GOARCH, Sha256: sha,
		Filename: "foo@14-1.0.pkg.tar.gz",
	}
	idx := &IndexFile{Arch: runtime.GOARCH, Packages: []IndexEntry{e, e}}
	if err := validateIndex(idx, false); err == nil {
		t.Fatal("duplicate index entry accepted")
	}
	e.Slot = "bad/slot"
	if err := validateIndex(&IndexFile{Arch: runtime.GOARCH, Packages: []IndexEntry{e}}, false); err == nil {
		t.Fatal("invalid slot accepted")
	}
}

func TestMakeIndexKeepsSlotsApart(t *testing.T) {
	dir := t.TempDir()
	writeBinpkgFixture(t, dir, "foo-1.0", "foo", "0", "1.0")
	writeBinpkgFixture(t, dir, "foo@14-1.0", "foo", "14", "1.0")
	if _, err := MakeIndex(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx IndexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Packages) != 2 {
		t.Fatalf("index entries = %d, want 2", len(idx.Packages))
	}
	slots := map[string]bool{}
	for _, e := range idx.Packages {
		slots[e.Slot] = true
	}
	if !slots["0"] || !slots["14"] {
		t.Fatalf("index slots = %v", slots)
	}
}
