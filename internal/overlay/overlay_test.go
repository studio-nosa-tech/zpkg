package overlay

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTree(t *testing.T, root, name, meta string, cats ...string) {
	t.Helper()
	dir := TreeDir(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if meta != "" {
		if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cats {
		if err := os.MkdirAll(filepath.Join(dir, c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpecRoundtripAndLoad(t *testing.T) {
	root := t.TempDir()
	sp := Spec{Name: "zignos-menu", URL: "https://example.invalid/menu.git", Branch: "main", Priority: 20, AllowUnsigned: true, Enabled: true}
	if err := WriteSpec(root, sp); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, "zignos-menu", "NAME=ZIGNOS MENU\nCATEGORIES=apps games\nCHANNELS=stable testing\n", "apps", "games")
	ovs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ovs) != 1 {
		t.Fatalf("overlays = %d", len(ovs))
	}
	ov := ovs[0]
	if ov.Name != sp.Name || ov.Priority != 20 || !ov.AllowUnsigned || !ov.Present || ov.Err != "" {
		t.Fatalf("overlay = %+v", ov)
	}
	if ov.Meta.Name != "ZIGNOS MENU" || !reflect.DeepEqual(ov.Meta.Categories, []string{"apps", "games"}) {
		t.Fatalf("meta = %+v", ov.Meta)
	}
	Activate(ovs, "stable")
	if !ovs[0].Active {
		t.Fatal("stable should activate the overlay")
	}
	Activate(ovs, "unstable")
	if ovs[0].Active {
		t.Fatal("unstable should not activate the overlay")
	}
	Activate(ovs, "testing")
	repos := ActiveRepos(ovs)
	want := []string{filepath.Join(TreeDir(root, sp.Name), "apps"), filepath.Join(TreeDir(root, sp.Name), "games")}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("repos = %v, want %v", repos, want)
	}
	if got := LabelForRepo(ovs, want[0]); got != "zignos-menu/apps" {
		t.Fatalf("label = %q", got)
	}
	if got := NameForRepo(ovs, want[0]); got != "zignos-menu" {
		t.Fatalf("name = %q", got)
	}
	if got := LabelForRepo(ovs, filepath.Join(root, "usr", "share", "zignos", "recipes", "stable", "core")); got != "core" {
		t.Fatalf("base label = %q", got)
	}
}

func TestLoadMissingAndBroken(t *testing.T) {
	root := t.TempDir()
	if err := WriteSpec(root, Spec{Name: "missing", URL: "https://example.invalid/x.git", Branch: "main", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, "broken", "CATEGORIES=apps\n")
	if err := WriteSpec(root, Spec{Name: "broken", URL: "https://example.invalid/y.git", Branch: "main", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ovs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ovs) != 2 {
		t.Fatalf("overlays = %d", len(ovs))
	}
	for _, ov := range ovs {
		if ov.Err == "" {
			t.Errorf("%s: expected an error status", ov.Name)
		}
	}
	Activate(ovs, "stable")
	if len(ActiveRepos(ovs)) != 0 {
		t.Fatalf("broken overlays contributed repos: %v", ActiveRepos(ovs))
	}
}

func TestFlatOverlay(t *testing.T) {
	root := t.TempDir()
	if err := WriteSpec(root, Spec{Name: "flat", URL: "/tmp/flat", Branch: "main", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, "flat", "")
	ovs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	Activate(ovs, "stable")
	repos := ActiveRepos(ovs)
	if len(repos) != 1 || repos[0] != TreeDir(root, "flat") {
		t.Fatalf("repos = %v", repos)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"zignos-menu", "my.overlay", "a_b", "x1"} {
		if !ValidName(ok) {
			t.Errorf("ValidName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "a/b", "a@b", "A", "a b"} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
}
