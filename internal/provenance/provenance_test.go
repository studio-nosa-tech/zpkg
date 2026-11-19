package provenance

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func TestRecipeDigest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("version", "1.0\n", 0o644)
	write("build", "#!/bin/sh\n", 0o755)
	first, err := RecipeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecipeDigest(dir)
	if err != nil || first != second {
		t.Fatalf("digest not stable: %s %s %v", first, second, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build"), []byte("#!/bin/sh\necho x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	changed, err := RecipeDigest(dir)
	if err != nil || changed == first {
		t.Fatalf("content change not detected: %s %s %v", first, changed, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(filepath.Join(dir, "build"), 0o644); err != nil {
		t.Fatal(err)
	}
	modeChanged, err := RecipeDigest(dir)
	if err != nil || modeChanged == changed {
		t.Fatalf("mode change not detected: %s %s %v", changed, modeChanged, err)
	}
}

func TestEnvironmentAndToolchain(t *testing.T) {
	db := pkgdb.DB{Dir: t.TempDir()}
	save := func(name, version string) {
		t.Helper()
		rec := &pkgdb.Rec{Name: name, Version: version, Revision: 1, Slot: "0",
			Meta: &pkgdb.Meta{Schema: pkgdb.Schema, Name: name, Version: version, Revision: 1, Slot: "0"}}
		if err := db.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	save("gcc", "16.2.0")
	save("glibc", "2.44")
	save("hello", "1.0")
	pkgs, hash, count := InstalledEnvironment(db)
	if count != 3 || len(pkgs) != 3 || hash == "" {
		t.Fatalf("environment = %v %q %d", pkgs, hash, count)
	}
	if pkgs[0] != "gcc-16.2.0-r1" {
		t.Fatalf("first package = %q", pkgs[0])
	}
	tools := Toolchain(db)
	if len(tools) != 2 || tools[0].Name != "gcc" || tools[1].Name != "glibc" {
		t.Fatalf("toolchain = %+v", tools)
	}
}

func TestWriteReadAndFromMeta(t *testing.T) {
	dir := t.TempDir()
	info := &Info{
		Schema:      Schema,
		Package:     Package{Name: "hello", Version: "1.0", Slot: "0", Arch: "amd64"},
		Build:       Build{Origin: "source", Sandbox: "bwrap", Network: "on", SourceDateEpoch: DefaultSourceDateEpoch},
		Environment: Environment{Hash: "abc", Count: 2, Packages: []string{"glibc-2.44", "hello-1.0"}},
		Artifact:    Artifact{Filename: "hello-1.0.pkg.tar.gz", SHA256: "def"},
	}
	path := filepath.Join(dir, "provenance.json")
	if err := Write(path, info); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Package.Name != "hello" || got.Build.Sandbox != "bwrap" || got.Environment.Count != 2 {
		t.Fatalf("got %+v", got)
	}

	rec := &pkgdb.Rec{Name: "hello", Version: "1.0", Revision: 0, Slot: "0",
		Meta: &pkgdb.Meta{Name: "hello", Version: "1.0", Slot: "0", Origin: "binrepo",
			OriginPkgSHA256: "abc", BuildSandbox: "bwrap", BuildNetwork: "off"}}
	mini := FromMeta(rec)
	if mini.Build.Origin != "binrepo" || mini.Build.Network != "off" || mini.Artifact.SHA256 != "abc" {
		t.Fatalf("FromMeta = %+v", mini)
	}
}
