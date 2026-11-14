package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	r, err := Load([]string{"testdata"}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", r.Version, "1.0.0")
	}
	if len(r.Sources) != 1 || !strings.HasSuffix(r.Sources[0], "hello-1.0.tar.gz") {
		t.Errorf("sources = %v, want one URL ending in hello-1.0.tar.gz", r.Sources)
	}
	if len(r.Checksums) != 1 {
		t.Errorf("checksums = %v, want 1 entry", r.Checksums)
	}
	if len(r.Depends) != 0 {
		t.Errorf("depends = %v, want none", r.Depends)
	}
	if _, err := os.Stat(r.Build); err != nil {
		t.Errorf("build script missing: %v", err)
	}
}

func TestLoadNotFound(t *testing.T) {
	if _, err := Load([]string{filepath.Join("testdata", "empty")}, "hello"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestChecksumCountMismatch(t *testing.T) {
	// testdata/bad has one source and no checksums.
	if _, err := Load([]string{"testdata"}, "bad"); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
