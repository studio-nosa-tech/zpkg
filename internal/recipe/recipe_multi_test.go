package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeRecipe(t *testing.T, dir, version string, extra map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"version": version + "\n",
		"build":   "#!/bin/sh\n",
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

func keys(rs []*Recipe) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Key()+"-"+r.FullVersion())
	}
	return out
}

func TestScanRepoLayouts(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "app"), "1.0", nil)
	writeRecipe(t, filepath.Join(repo, "foo@14"), "2.0", map[string]string{"slot": "14\n"})
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "1.0"), "1.0", nil)
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "2.0"), "2.0", nil)
	writeRecipe(t, filepath.Join(repo, "baz@3", "versions", "1.0"), "1.0", nil)
	writeRecipe(t, filepath.Join(repo, "baz@3", "versions", "1.1"), "1.1", nil)

	rs, err := ScanRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	got := keys(rs)
	want := []string{"app-1.0", "bar-2.0", "bar-1.0", "baz@3-1.1", "baz@3-1.0", "foo@14-2.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for _, r := range rs {
		if r.Repo != repo {
			t.Errorf("%s: repo = %q", r.Key(), r.Repo)
		}
	}
}

func TestLoadCandidateAndDefault(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "foo@14"), "2.0", nil)
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "1.0"), "1.0", nil)
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "2.0"), "2.0", nil)

	r, err := LoadCandidate(repoList(repo), "foo@14")
	if err != nil || r.FullVersion() != "2.0" || r.Slot != "14" {
		t.Fatalf("LoadCandidate(foo@14) = %+v, %v", r, err)
	}
	if _, err := LoadCandidate(repoList(repo), "foo"); err == nil {
		t.Fatal("LoadCandidate(foo) should not find slot 14")
	}
	r, err = LoadCandidate(repoList(repo), "bar")
	if err != nil || r.FullVersion() != "2.0" {
		t.Fatalf("LoadCandidate(bar) = %+v, %v", r, err)
	}
	r, err = Load(repoList(repo), "bar")
	if err != nil || r.FullVersion() != "2.0" {
		t.Fatalf("Load(bar) = %+v, %v", r, err)
	}
	r, err = Load(repoList(repo), "foo")
	if err != nil || r.Slot != "14" || r.FullVersion() != "2.0" {
		t.Fatalf("Load(foo) = %+v, %v", r, err)
	}
}

func repoList(repos ...string) []string { return repos }

func TestSlotDisagreement(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "foo@14"), "1.0", map[string]string{"slot": "15\n"})
	if _, err := ScanRepo(repo); err == nil {
		t.Fatal("slot file/disagreement accepted")
	}
}

func TestInvalidSlotDirectory(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "foo@bad!"), "1.0", nil)
	if _, err := ScanRepo(repo); err == nil {
		t.Fatal("invalid slot accepted")
	}
}

func TestDuplicateCandidate(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "a"), "1.0", nil)
	writeRecipe(t, filepath.Join(repo, "bar", "versions", "b"), "1.0", nil)
	if _, err := ScanRepo(repo); err == nil {
		t.Fatal("duplicate candidate accepted")
	}
}

func TestFlatVersionWinsOverVersionsDir(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "foo"), "1.0", nil)
	// An asset directory named versions/ without a version file must not be
	// treated as a second layout.
	if err := os.MkdirAll(filepath.Join(repo, "foo", "versions", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Load(repoList(repo), "foo")
	if err != nil || r.FullVersion() != "1.0" {
		t.Fatalf("Load(foo) = %+v, %v", r, err)
	}
}

func TestFirstRepoWinsPackageKey(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeRecipe(t, filepath.Join(first, "foo", "versions", "1.0"), "1.0", nil)
	writeRecipe(t, filepath.Join(first, "foo", "versions", "1.1"), "1.1", nil)
	writeRecipe(t, filepath.Join(second, "foo", "versions", "2.0"), "2.0", nil)
	writeRecipe(t, filepath.Join(second, "foo@14"), "3.0", nil)

	rs, err := ScanRepos(repoList(first, second))
	if err != nil {
		t.Fatal(err)
	}
	got := keys(rs)
	want := []string{"foo-1.1", "foo-1.0", "foo@14-3.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestInvalidLegacySlotFile(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, filepath.Join(repo, "foo"), "1.0", map[string]string{"slot": "../x\n"})
	if _, err := Load(repoList(repo), "foo"); err == nil {
		t.Fatal("unsafe slot accepted")
	}
}
