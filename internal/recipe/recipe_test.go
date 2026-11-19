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

func TestBuildNetwork(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "net")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"version": "1.0\n",
		"build":   "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Load([]string{dir}, "net")
	if err != nil || r.BuildNetwork != nil {
		t.Fatalf("default build_network = %v, %v", r.BuildNetwork, err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "build_network"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Load([]string{dir}, "net")
	if err != nil || r.BuildNetwork == nil || *r.BuildNetwork {
		t.Fatalf("build_network 0 = %v, %v", r.BuildNetwork, err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "build_network"), []byte("yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Load([]string{dir}, "net")
	if err != nil || r.BuildNetwork == nil || !*r.BuildNetwork {
		t.Fatalf("build_network yes = %v, %v", r.BuildNetwork, err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "build_network"), []byte("maybe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{dir}, "net"); err == nil {
		t.Fatal("invalid build_network accepted")
	}
}

func TestUpgradeWarning(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "warn")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"version": "1.0\n",
		"build":   "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Load([]string{dir}, "warn")
	if err != nil || r.UpgradeWarning != "" {
		t.Fatalf("default warning = %q, %v", r.UpgradeWarning, err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "upgrade_warning"),
		[]byte("rebuild all modules\ncheck /etc/foo.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Load([]string{dir}, "warn")
	if err != nil {
		t.Fatal(err)
	}
	if r.UpgradeWarning != "rebuild all modules check /etc/foo.conf" {
		t.Fatalf("warning = %q", r.UpgradeWarning)
	}
}
