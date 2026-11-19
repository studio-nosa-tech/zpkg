package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
)

func writeRecipe(t *testing.T, repo, name, version string, deps, makedeps []string) {
	t.Helper()
	dir := filepath.Join(repo, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"version": version + "\n",
		"build":   "#!/bin/sh\n",
	}
	if len(deps) > 0 {
		files["depends"] = strings.Join(deps, "\n") + "\n"
	}
	if len(makedeps) > 0 {
		files["makedepends"] = strings.Join(makedeps, "\n") + "\n"
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func installed(t *testing.T, db pkgdb.DB, name, version string, deps, use []string) *pkgdb.Rec {
	t.Helper()
	rec := &pkgdb.Rec{Name: name, Slot: "0", Version: version,
		Meta: &pkgdb.Meta{Schema: pkgdb.Schema, Name: name, Version: version, Slot: "0",
			Depends: append([]string{}, deps...), UseFlags: append([]string{}, use...)}}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func testOptions(t *testing.T, flags ...string) options.Options {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "use.conf"), []byte(strings.Join(flags, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := options.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPlanActionsDiffsWarningsAndOrphans(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, repo, "app", "2.0", []string{"lib", ">=newdep-1.0"}, []string{"buildtool"})
	writeRecipe(t, repo, "lib", "2.0", nil, nil)
	writeRecipe(t, repo, "newdep", "1.0", nil, nil)
	writeRecipe(t, repo, "buildtool", "1.0", nil, nil)
	writeRecipe(t, repo, "rebuild", "1.0", nil, nil)
	writeRecipe(t, repo, "downgrade", "1.0", nil, nil)
	writeRecipe(t, repo, "glibc", "2.0", nil, nil)
	if err := os.WriteFile(filepath.Join(repo, "app", "upgrade_warning"),
		[]byte("check /etc/app.conf after upgrading\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db := pkgdb.DB{Dir: t.TempDir()}
	installed(t, db, "app", "1.0", []string{"lib", "olddep"}, nil)
	installed(t, db, "lib", "1.0", nil, nil)
	installed(t, db, "olddep", "1.0", nil, nil)
	installed(t, db, "extra", "1.0", nil, nil)
	installed(t, db, "rebuild", "1.0", nil, nil)
	installed(t, db, "downgrade", "2.0", nil, nil)
	installed(t, db, "glibc", "1.0", nil, nil)
	if err := WriteAppliedChannel(db, "testing"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Repos: []string{repo}, Channel: "stable", ChannelSource: "make.conf",
		Options: testOptions(t, "ssl"),
	}
	plan, err := Build(cfg, db, []string{"app", "rebuild", "downgrade", "glibc"}, Options{NewUse: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ChannelChanged || plan.PreviousChannel != "testing" {
		t.Fatalf("channel change = %v (%q)", plan.ChannelChanged, plan.PreviousChannel)
	}
	kinds := map[string]Kind{}
	var app, glibc *Entry
	for i := range plan.Entries {
		kinds[plan.Entries[i].Name] = plan.Entries[i].Kind
		switch plan.Entries[i].Name {
		case "app":
			app = &plan.Entries[i]
		case "glibc":
			glibc = &plan.Entries[i]
		}
	}
	want := map[string]Kind{
		"app": KindUpgrade, "lib": KindUpgrade, "newdep": KindInstall,
		"buildtool": KindInstall, "rebuild": KindRebuild,
		"downgrade": KindDowngrade, "glibc": KindUpgrade,
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("%s kind = %q, want %q", name, kinds[name], kind)
		}
	}
	if app == nil || len(app.DepsAdded) != 1 || app.DepsAdded[0] != ">=newdep-1.0" ||
		len(app.DepsRemoved) != 1 || app.DepsRemoved[0] != "olddep" {
		t.Fatalf("app deps = %+v", app)
	}
	if len(app.BuildAdded) != 1 || app.BuildAdded[0] != "buildtool" {
		t.Fatalf("app build deps = %+v", app)
	}
	if app.Warnings == nil || !containsString(app.Warnings, "check /etc/app.conf after upgrading") {
		t.Fatalf("app warnings = %v", app.Warnings)
	}
	if glibc == nil || !containsString(glibc.Warnings, "major version 1.0 -> 2.0") {
		t.Fatalf("glibc warnings = %v", glibc)
	}
	if !plan.HasDowngrades() {
		t.Fatal("downgrade not reported")
	}
	orphanNames := map[string]Orphan{}
	for _, o := range plan.Orphans {
		orphanNames[o.Name] = o
	}
	if _, ok := orphanNames["olddep"]; !ok {
		t.Errorf("olddep not orphaned: %+v", plan.Orphans)
	}
	if _, ok := orphanNames["extra"]; ok {
		t.Errorf("unrelated installed package must not be orphaned: %+v", plan.Orphans)
	}
	for name := range orphanNames {
		if !orphanNames[name].Prunable() {
			t.Errorf("%s unexpectedly has dependents: %v", name, orphanNames[name].RequiredBy)
		}
	}
}

func TestPlanOrphanRequiredBySurvivingPackage(t *testing.T) {
	repo := t.TempDir()
	writeRecipe(t, repo, "app", "1.0", nil, nil)
	db := pkgdb.DB{Dir: t.TempDir()}
	installed(t, db, "app", "1.0", []string{"libold"}, nil)
	installed(t, db, "libold", "1.0", nil, nil)

	cfg := config.Config{Repos: []string{repo}, Channel: "stable", Options: testOptions(t)}
	plan, err := Build(cfg, db, []string{"app"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Orphans) != 1 || plan.Orphans[0].Name != "libold" || plan.Orphans[0].Prunable() {
		t.Fatalf("orphans = %+v", plan.Orphans)
	}
	if !containsString(plan.Orphans[0].RequiredBy, "app") {
		t.Fatalf("requiredBy = %v", plan.Orphans[0].RequiredBy)
	}
}

func TestCheckIntegrity(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	writeRecipe(t, repo, "hello", "1.0", nil, nil)
	if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "usr", "bin", "hello")
	if err := os.WriteFile(file, []byte("hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []manifest.Entry{
		{Kind: "dir", Path: "usr"},
		{Kind: "dir", Path: "usr/bin"},
	}
	sum, err := hash.File(file)
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, manifest.Entry{Kind: "file", Path: "usr/bin/hello", Hash: sum})
	if err := os.Symlink("hello", filepath.Join(root, "usr", "bin", "hi")); err == nil {
		entries = append(entries, manifest.Entry{Kind: "link", Path: "usr/bin/hi", Link: "hello"})
	}
	db := pkgdb.DB{Dir: t.TempDir()}
	rec := &pkgdb.Rec{Name: "hello", Slot: "0", Version: "1.0",
		Entries: entries,
		Meta:    &pkgdb.Meta{Schema: pkgdb.Schema, Name: "hello", Version: "1.0", Slot: "0"}}
	if err := db.Save(rec); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Repos: []string{repo}, Root: root, Channel: "stable", Options: testOptions(t)}
	plan := &Plan{Targets: []string{"hello"}, Entries: []Entry{{Name: "hello", Slot: "0", NewVersion: "1.0", Kind: KindUpgrade}}}
	rep, err := Check(cfg, db, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Packages != 1 || rep.Files != len(entries) {
		t.Fatalf("report = %+v", rep)
	}

	if err := os.WriteFile(file, []byte("changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(cfg, db, plan, false); err != nil {
		t.Fatalf("structural check should pass: %v", err)
	}
	if _, err := Check(cfg, db, plan, true); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("hash mismatch not detected: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(cfg, db, plan, false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing file not detected: %v", err)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
