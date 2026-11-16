package options

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/use.conf"), "# comment\ndemo ssl\ndemo\n")
	writeFile(t, filepath.Join(root, "etc/zpkg/make.conf"), "CFLAGS=\"-O2 -pipe\"\nMAKEOPTS=-j4\n")

	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"demo", "ssl"}; !reflect.DeepEqual(o.Use, want) {
		t.Errorf("Use = %v, want %v", o.Use, want)
	}
	if !o.Enabled("demo") || o.Enabled("nope") {
		t.Error("Enabled is wrong")
	}
	if o.CFlags != "-O2 -pipe" || o.MakeOpts != "-j4" {
		t.Errorf("CFlags=%q MakeOpts=%q", o.CFlags, o.MakeOpts)
	}
	if o.Hash() == "" {
		t.Error("Hash should be non-empty when options are set")
	}
}

func TestEnvOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/use.conf"), "fileflag\n")
	t.Setenv("ZPKG_USE", "envflag")
	t.Setenv("ZPKG_CFLAGS", "-Os")

	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"envflag"}; !reflect.DeepEqual(o.Use, want) {
		t.Errorf("Use = %v, want envflag only", o.Use)
	}
	if o.CFlags != "-Os" {
		t.Errorf("CFlags = %q", o.CFlags)
	}
}

func TestEnvEmptyOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/use.conf"), "fileflag\n")
	t.Setenv("ZPKG_USE", "") // present but empty: clears the file flags
	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Use) != 0 {
		t.Fatalf("Use = %v, want empty", o.Use)
	}
}

func TestExpand(t *testing.T) {
	o := Options{set: map[string]bool{"x11": true}}
	deps := []string{"glibc", "x11?libxcb", "wayland?libwayland", "x11?"}
	got := o.Expand(deps)
	want := []string{"glibc", "libxcb"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = %v, want %v", got, want)
	}
}

func TestHashEmpty(t *testing.T) {
	o, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if o.Hash() != "" {
		t.Errorf("Hash = %q, want empty", o.Hash())
	}
}

func TestSaveRoundtrip(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, []string{"ssl", "demo"}); err != nil {
		t.Fatal(err)
	}
	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"demo", "ssl"}; !reflect.DeepEqual(o.Use, want) {
		t.Fatalf("Use = %v, want %v", o.Use, want)
	}
}

func TestPackageUse(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/use.conf"), "global\n")
	writeFile(t, filepath.Join(root, "etc/zpkg/package.use"), "vim -global gtk\ngit curl\n")

	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	vim := o.ForPackage("vim")
	if vim.Enabled("global") {
		t.Error("package.use should disable the global flag for vim")
	}
	if !vim.Enabled("gtk") {
		t.Error("package.use should enable gtk for vim")
	}
	if o.ForPackage("git").Enabled("global") != true {
		t.Error("other packages keep the global flag")
	}
	if o.Hash() == vim.Hash() {
		t.Error("effective options differ, hash must differ")
	}
	if !o.ForPackage("other").Enabled("global") {
		t.Error("ForPackage without tokens must return the global set")
	}
}

func TestSavePackageUse(t *testing.T) {
	root := t.TempDir()
	if err := SavePackageUse(root, "vim", []string{"-X", "gtk"}); err != nil {
		t.Fatal(err)
	}
	if err := SavePackageUse(root, "git", []string{"curl"}); err != nil {
		t.Fatal(err)
	}
	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := o.PackageTokens("vim"); len(got) != 2 || got[0] != "-X" || got[1] != "gtk" {
		t.Errorf("vim tokens = %v", got)
	}
	if !o.ForPackage("git").Enabled("curl") {
		t.Error("git curl flag missing")
	}
	if err := SavePackageUse(root, "vim", nil); err != nil {
		t.Fatal(err)
	}
	o, _ = Load(root)
	if len(o.PackageTokens("vim")) != 0 {
		t.Error("vim line should be removed")
	}
}
