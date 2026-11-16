package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadOptionalMetadata(t *testing.T) {
	r, err := Load([]string{"testdata"}, "rich")
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 3 {
		t.Errorf("revision = %d, want 3", r.Revision)
	}
	if r.Slot != "libs" {
		t.Errorf("slot = %q, want libs", r.Slot)
	}
	if want := []string{"ncurses"}; !reflect.DeepEqual(r.Depends, want) {
		t.Errorf("depends = %v, want %v", r.Depends, want)
	}
	if want := []string{"gcc", "make"}; !reflect.DeepEqual(r.MakeDepends, want) {
		t.Errorf("makedepends = %v, want %v", r.MakeDepends, want)
	}
	if want := []string{"libfoo.so", "virtual-dev"}; !reflect.DeepEqual(r.Provides, want) {
		t.Errorf("provides = %v, want %v", r.Provides, want)
	}
	if want := []string{"foo-compat"}; !reflect.DeepEqual(r.Conflicts, want) {
		t.Errorf("conflicts = %v, want %v", r.Conflicts, want)
	}
	if got := r.FullVersion(); got != "2.1.0-r3" {
		t.Errorf("FullVersion = %q, want 2.1.0-r3", got)
	}
}

func TestDefaultsForV0Recipes(t *testing.T) {
	r, err := Load([]string{"testdata"}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 0 || r.Slot != "0" {
		t.Errorf("defaults = revision %d slot %q, want 0 and 0", r.Revision, r.Slot)
	}
	if r.FullVersion() != "1.0.0" {
		t.Errorf("FullVersion = %q", r.FullVersion())
	}
}

func TestBadRevision(t *testing.T) {
	// Non-numeric revision must fail loudly.
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("version", "1.0\n")
	write("revision", "x\n")
	write("build", "#!/bin/sh\ntrue\n")
	if _, err := Load([]string{dir}, filepath.Base(dir)); err == nil {
		t.Fatal("expected revision parse error")
	}
}
