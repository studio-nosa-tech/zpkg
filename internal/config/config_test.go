package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/provenance"
)

func TestSourceDateEpochAndBuildSettings(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "build.conf"),
		[]byte("SOURCE_DATE_EPOCH=1234567890\nBUILD_LOG=0\nSBOM=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "")
	t.Setenv("SOURCE_DATE_EPOCH", "")
	t.Setenv("ZPKG_BUILD_LOG", "")
	t.Setenv("ZPKG_SBOM", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != 1234567890 {
		t.Fatalf("SourceDateEpoch = %d", cfg.SourceDateEpoch)
	}
	if cfg.BuildLog || cfg.SBOM {
		t.Fatalf("BuildLog=%v SBOM=%v, want both false", cfg.BuildLog, cfg.SBOM)
	}

	t.Setenv("SOURCE_DATE_EPOCH", "42")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != 42 {
		t.Fatalf("SOURCE_DATE_EPOCH override = %d", cfg.SourceDateEpoch)
	}
}

func TestSourceDateEpochDefaultAndValidation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "")
	t.Setenv("SOURCE_DATE_EPOCH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceDateEpoch != provenance.DefaultSourceDateEpoch {
		t.Fatalf("default SourceDateEpoch = %d", cfg.SourceDateEpoch)
	}
	if !cfg.BuildLog || !cfg.SBOM {
		t.Fatalf("defaults BuildLog=%v SBOM=%v, want true", cfg.BuildLog, cfg.SBOM)
	}

	t.Setenv("ZPKG_SOURCE_DATE_EPOCH", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("invalid SOURCE_DATE_EPOCH accepted")
	}
}
