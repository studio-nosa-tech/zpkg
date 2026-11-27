package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func writeMulti(t *testing.T, repo, rel, version, slot string, extra map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"version": version + "\n",
		"build":   "#!/bin/sh\n",
	}
	if slot != "" {
		files["slot"] = slot + "\n"
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func resolveOnce(t *testing.T, repo string, installed []*pkgdb.Rec, prefer Preference, targets ...string) *Result {
	t.Helper()
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Resolver{
		Index:     idx,
		Options:   testOptions(t, t.TempDir()),
		Installed: installed,
		Prefer:    prefer,
	}).Resolve(targets)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestResolvePicksVersionSatisfyingConstraint(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "lib/versions/1.0", "1.0", "", nil)
	writeMulti(t, repo, "lib/versions/2.0", "2.0", "", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "=lib-1.0\n"})

	res := resolveOnce(t, repo, nil, PreferNewest, "app")
	if want := []string{"lib", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
	if got := res.Selected["lib"].Version; got != "1.0" {
		t.Fatalf("lib version = %q, want 1.0", got)
	}
}

func TestResolvePreferInstalledVersion(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "lib/versions/1.0", "1.0", "", nil)
	writeMulti(t, repo, "lib/versions/2.0", "2.0", "", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": ">=lib-1.0\n"})

	installed := []*pkgdb.Rec{{Name: "lib", Slot: "0", Version: "1.0"}}
	res := resolveOnce(t, repo, installed, PreferInstalled, "app")
	if got := res.Selected["lib"].Version; got != "1.0" {
		t.Fatalf("lib version = %q, want installed 1.0", got)
	}
	res = resolveOnce(t, repo, installed, PreferNewest, "app")
	if got := res.Selected["lib"].Version; got != "2.0" {
		t.Fatalf("upgrade lib version = %q, want 2.0", got)
	}
}

func TestResolveTwoSlotsTogether(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "foo@1", "1.0", "1", nil)
	writeMulti(t, repo, "foo@2", "2.0", "2", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "foo@1\nfoo@2\n"})

	res := resolveOnce(t, repo, nil, PreferNewest, "app")
	if want := []string{"foo@1", "foo@2", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
	if res.Selected["foo@1"] == nil || res.Selected["foo@2"] == nil {
		t.Fatalf("selected = %v", res.Selected)
	}
	if res.Targets[0] != "app" {
		t.Fatalf("target = %q", res.Targets[0])
	}
}

func TestResolveBareAtomPrefersSlotZero(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "foo", "1.0", "", nil)
	writeMulti(t, repo, "foo@14", "2.0", "14", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "foo\n"})

	res := resolveOnce(t, repo, nil, PreferNewest, "app")
	if want := []string{"foo", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
}

func TestResolveConflictsBetweenSlots(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "foo@1", "1.0", "1", map[string]string{"conflicts": "foo@2\n"})
	writeMulti(t, repo, "foo@2", "2.0", "2", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "foo@1\nfoo@2\n"})

	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{Index: idx, Options: testOptions(t, t.TempDir()), Prefer: PreferNewest}).Resolve([]string{"app"})
	if err == nil {
		t.Fatal("conflicting slots resolved")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveUpgradesInstalledPackageToResolveConflict(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "old/versions/1.0", "1.0", "", nil)
	writeMulti(t, repo, "old/versions/2.0", "2.0", "", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"conflicts": "=old-1.0\n"})

	installed := []*pkgdb.Rec{{Name: "old", Slot: "0", Version: "1.0"}}
	res := resolveOnce(t, repo, installed, PreferNewest, "app")
	if got := res.Selected["old"].Version; got != "2.0" {
		t.Fatalf("old version = %q, want 2.0 (conflict replacement)", got)
	}
	if want := []string{"old", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
}

func TestResolveInstalledConflictWithoutReplacementFails(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"conflicts": "old\n"})
	installed := []*pkgdb.Rec{{Name: "old", Slot: "0", Version: "1.0"}}
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Resolver{Index: idx, Options: testOptions(t, t.TempDir()), Installed: installed}).Resolve([]string{"app"}); err == nil {
		t.Fatal("conflict with unreplaceable installed package resolved")
	}
}

func TestResolveInstalledOnlyDependency(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "lib\n"})
	installed := []*pkgdb.Rec{{Name: "lib", Slot: "0", Version: "1.0"}}
	res := resolveOnce(t, repo, installed, PreferInstalled, "app")
	if want := []string{"lib", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
	c := res.Selected["lib"]
	if c == nil || !c.InstalledOnly || c.Recipe != nil {
		t.Fatalf("lib candidate = %+v", c)
	}
}

func TestResolveUnsatDiagnostics(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "lib", "1.0", "", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": ">=lib-2.0\n"})
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"app"})
	if err == nil {
		t.Fatal("version mismatch resolved")
	}
	if !strings.Contains(err.Error(), "no candidate satisfies >=lib-2.0") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTwoTargetConflictDiagnostics(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "a", "1.0", "", map[string]string{"conflicts": "b\n"})
	writeMulti(t, repo, "b", "1.0", "", nil)
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"a", "b"})
	if err == nil {
		t.Fatal("conflicting targets resolved")
	}
	if !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("error does not name the target set: %v", err)
	}
	if !strings.Contains(err.Error(), "conflicts with") {
		t.Fatalf("error has no conflict explanation: %v", err)
	}
}

func TestResolveDeterministic(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "lib/versions/1.0", "1.0", "", nil)
	writeMulti(t, repo, "lib/versions/2.0", "2.0", "", nil)
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "lib\n"})
	first := resolveOnce(t, repo, nil, PreferNewest, "app")
	for i := 0; i < 3; i++ {
		next := resolveOnce(t, repo, nil, PreferNewest, "app")
		if !reflect.DeepEqual(first.Order, next.Order) {
			t.Fatalf("order changed: %v vs %v", first.Order, next.Order)
		}
		for k := range first.Selected {
			if next.Selected[k] == nil || next.Selected[k].Version != first.Selected[k].Version {
				t.Fatalf("selection changed for %s", k)
			}
		}
	}
}

func TestResolvePreferenceOptimizationReconsidersProviders(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "lib/versions/1.0", "1.0", "", nil)
	writeMulti(t, repo, "lib/versions/2.0", "2.0", "", nil)
	writeMulti(t, repo, "p1", "1.0", "", map[string]string{
		"provides":  "virtual/x\n",
		"conflicts": "=lib-2.0\n",
	})
	writeMulti(t, repo, "p2", "1.0", "", map[string]string{"provides": "virtual/x\n"})
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "virtual/x\nlib\n"})

	res := resolveOnce(t, repo, nil, PreferNewest, "app")
	if got := res.Selected["lib"].Version; got != "2.0" {
		t.Fatalf("lib = %s, want preferred 2.0 (p2 must replace p1)", got)
	}
	if res.Selected["p1"] != nil || res.Selected["p2"] == nil {
		t.Fatalf("providers: p1=%v p2=%v", res.Selected["p1"], res.Selected["p2"])
	}
	if want := []string{"lib", "p2", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
}

func TestResolveBreaksCycleByProviderChoice(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "p1", "1.0", "", map[string]string{
		"provides": "virtual/x\n",
		"depends":  "app\n",
	})
	writeMulti(t, repo, "p2", "1.0", "", map[string]string{"provides": "virtual/x\n"})
	writeMulti(t, repo, "app", "1.0", "", map[string]string{"depends": "virtual/x\n"})

	res := resolveOnce(t, repo, nil, PreferNewest, "app")
	if want := []string{"p2", "app"}; !reflect.DeepEqual(res.Order, want) {
		t.Fatalf("order = %v, want %v", res.Order, want)
	}
	if res.Selected["p1"] != nil {
		t.Fatalf("cyclic provider p1 selected: %v", res.Selected)
	}
}

func TestResolveDependencyCycleFails(t *testing.T) {
	repo := t.TempDir()
	writeMulti(t, repo, "a", "1.0", "", map[string]string{"depends": "b\n"})
	writeMulti(t, repo, "b", "1.0", "", map[string]string{"depends": "a\n"})
	idx, err := NewIndex([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{Index: idx, Options: testOptions(t, t.TempDir())}).Resolve([]string{"a"})
	if err == nil {
		t.Fatal("dependency cycle resolved")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %v", err)
	}
}
