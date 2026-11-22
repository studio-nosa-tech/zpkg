package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func recipeDir(t *testing.T, repo, name string) string {
	t.Helper()
	dir := filepath.Join(repo, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("version", "1.0\n")
	write("build", "#!/bin/sh\n")
	return dir
}

func writeFile(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testOptions(t *testing.T, root string, flags ...string) options.Options {
	t.Helper()
	if err := options.Save(root, flags); err != nil {
		t.Fatal(err)
	}
	o, err := options.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestResolveVersionOrder(t *testing.T) {
	repo := t.TempDir()
	recipeDir(t, repo, "lib")
	d := recipeDir(t, repo, "app")
	writeFile(t, d, "depends", ">=lib-1.0\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib", "app"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order = %v, want %v", got.Order, want)
	}
	if got.Targets[0] != "app" {
		t.Fatalf("target = %q", got.Targets[0])
	}
}

func TestResolveVersionMismatch(t *testing.T) {
	repo := t.TempDir()
	recipeDir(t, repo, "lib") // 1.0
	d := recipeDir(t, repo, "app")
	writeFile(t, d, "depends", ">=lib-2.0\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"app"}); err == nil {
		t.Fatal("version mismatch resolved")
	}
}

func TestResolveProvides(t *testing.T) {
	repo := t.TempDir()
	p := recipeDir(t, repo, "postfix")
	writeFile(t, p, "provides", "virtual/mailer-2.0\n")
	q := recipeDir(t, repo, "sendmail")
	writeFile(t, q, "provides", "virtual/mailer-1.0\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{">=virtual/mailer-1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Targets[0] != "postfix" {
		t.Fatalf("target = %q, want postfix", got.Targets[0])
	}
	if want := []string{"postfix"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order = %v", got.Order)
	}
}

func TestResolveBacktracksOnConflict(t *testing.T) {
	repo := t.TempDir()
	p1 := recipeDir(t, repo, "p1")
	writeFile(t, p1, "provides", "virtual/foo\n")
	writeFile(t, p1, "conflicts", "p2\n")
	p2 := recipeDir(t, repo, "p2")
	writeFile(t, p2, "provides", "virtual/foo\n")
	a := recipeDir(t, repo, "app")
	writeFile(t, a, "depends", "virtual/foo\np2\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Targets[0] != "app" {
		t.Fatalf("target = %q", got.Targets[0])
	}
	want := []string{"p2", "app"}
	if !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order = %v, want %v", got.Order, want)
	}
}

func TestResolveUseConstraintAndCondition(t *testing.T) {
	repo := t.TempDir()
	lib := recipeDir(t, repo, "lib")
	writeFile(t, lib, "iuse", "ssl\n")
	app := recipeDir(t, repo, "app")
	writeFile(t, app, "iuse", "ssl\n")
	writeFile(t, app, "depends", "ssl?lib[ssl]\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, root)}).Resolve([]string{"app"}); err != nil {
		t.Fatalf("conditional dep should be skipped without ssl: %v", err)
	}
	root2 := t.TempDir()
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, root2, "ssl")}).Resolve([]string{"app"}); err != nil {
		t.Fatalf("ssl dependency should resolve: %v", err)
	}
	root3 := t.TempDir()
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, root3, "ssl")}).Resolve([]string{"app"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveNegatedCondition(t *testing.T) {
	repo := t.TempDir()
	lib := recipeDir(t, repo, "lib")
	writeFile(t, lib, "iuse", "ssl\n")
	app := recipeDir(t, repo, "app")
	writeFile(t, app, "iuse", "ssl\n")
	writeFile(t, app, "depends", "!ssl?lib\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	got, err := (&Resolver{Index: idx, Options: testOptions(t, root)}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib", "app"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order = %v", got.Order)
	}
	root2 := t.TempDir()
	got, err = (&Resolver{Index: idx, Options: testOptions(t, root2, "ssl")}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("ssl order = %v", got.Order)
	}
}

func TestResolveInstalledConflict(t *testing.T) {
	repo := t.TempDir()
	app := recipeDir(t, repo, "app")
	writeFile(t, app, "conflicts", "old\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	installed := []*pkgdb.Rec{{Name: "old", Slot: "0", Version: "1.0"}}
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir()), Installed: installed}).Resolve([]string{"app"}); err == nil {
		t.Fatal("conflict with installed package resolved")
	}
}

func TestResolveVideoCardsCondition(t *testing.T) {
	repo := t.TempDir()
	recipeDir(t, repo, "lib")
	app := recipeDir(t, repo, "app")
	writeFile(t, app, "depends", "video_cards_iris?lib\n")
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("ZPKG_VIDEO_CARDS", "")
	got, err := (&Resolver{Index: idx, Options: testOptions(t, root)}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order without iris = %v", got.Order)
	}
	t.Setenv("ZPKG_VIDEO_CARDS", "iris")
	got, err = (&Resolver{Index: idx, Options: testOptions(t, root)}).Resolve([]string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib", "app"}; !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("order with iris = %v", got.Order)
	}
}
