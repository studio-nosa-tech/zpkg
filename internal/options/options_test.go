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
