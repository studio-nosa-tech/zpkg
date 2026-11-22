package options

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestSetVarPreservesOtherLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "etc", "zpkg", "make.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# build flags\nCFLAGS=-O2\nMAKEOPTS=-j8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(path, "CHANNEL", "testing"); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(path, "CFLAGS", "-O3"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# build flags", "CFLAGS=-O3", "MAKEOPTS=-j8", "CHANNEL=testing"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "CFLAGS=-O2") {
		t.Error("old CFLAGS value survived")
	}
}

func TestVideoCardsLoadAndExpand(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/make.conf"), "VIDEO_CARDS=\"iris, virgl iris\"\n")
	old, had := os.LookupEnv("ZPKG_VIDEO_CARDS")
	os.Unsetenv("ZPKG_VIDEO_CARDS")
	defer func() {
		if had {
			os.Setenv("ZPKG_VIDEO_CARDS", old)
		}
	}()

	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"iris", "virgl"}; !reflect.DeepEqual(o.VideoCards, want) {
		t.Fatalf("VideoCards = %v, want %v", o.VideoCards, want)
	}
	if !o.VideoCardEnabled("iris") || o.VideoCardEnabled("amdgpu") {
		t.Error("VideoCardEnabled is wrong")
	}
	deps := []string{"libdrm", "video_cards_iris?libX11", "video_cards_amdgpu?llvm", "video_cards_iris?"}
	if got, want := o.Expand(deps), []string{"libdrm", "libX11"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = %v, want %v", got, want)
	}
}

func TestVideoCardsEnvOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/make.conf"), "VIDEO_CARDS=iris\n")
	t.Setenv("ZPKG_VIDEO_CARDS", "radeonsi, virgl")
	o, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"radeonsi", "virgl"}; !reflect.DeepEqual(o.VideoCards, want) {
		t.Fatalf("VideoCards = %v, want %v", o.VideoCards, want)
	}
}

func TestHashForVideoCards(t *testing.T) {
	a := Options{VideoCards: []string{"iris"}}
	b := Options{VideoCards: []string{"virgl"}}
	if a.Hash() != "" {
		t.Errorf("Hash without video component = %q, want empty", a.Hash())
	}
	if a.HashFor(false) != b.HashFor(false) {
		t.Error("HashFor(false) must not depend on VIDEO_CARDS")
	}
	if a.HashFor(true) == "" || a.HashFor(true) == b.HashFor(true) {
		t.Error("HashFor(true) must include VIDEO_CARDS")
	}
}

func TestSaveVideoCards(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/zpkg/make.conf"), "CFLAGS=\"-O2\"\n")
	if err := SaveVideoCards(root, []string{"virgl", "iris", "iris"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc/zpkg/make.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "VIDEO_CARDS=iris virgl") {
		t.Fatalf("make.conf = %q", data)
	}
	if !strings.Contains(string(data), "CFLAGS=\"-O2\"") {
		t.Fatalf("CFLAGS was lost: %q", data)
	}
}
