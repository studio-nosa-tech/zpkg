package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{
		"":        ModeAuto,
		"auto":    ModeAuto,
		"AUTO":    ModeAuto,
		"require": ModeRequire,
		"off":     ModeOff,
	} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestResolve(t *testing.T) {
	if p, ok, err := Resolve(ModeOff, "/nonexistent/bwrap"); err != nil || ok || p != "" {
		t.Fatalf("off = %q, %v, %v", p, ok, err)
	}
	if _, ok, err := Resolve(ModeAuto, "/nonexistent/bwrap"); err != nil || ok {
		t.Fatalf("auto missing = %v, %v", ok, err)
	}
	if _, _, err := Resolve(ModeRequire, "/nonexistent/bwrap"); err == nil {
		t.Fatal("require with missing bwrap succeeded")
	}

	dir := t.TempDir()
	name := "bwrap"
	if runtime.GOOS == "windows" {
		name = "bwrap.exe"
	}
	fake := filepath.Join(dir, name)
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, ok, err := Resolve(ModeAuto, fake)
	if err != nil || !ok || p == "" {
		t.Fatalf("auto existing = %q, %v, %v", p, ok, err)
	}
}

func TestArgs(t *testing.T) {
	probe := t.TempDir()
	for _, d := range []string{"tmp", "run", "var/lib", "root", "home", "etc/ssh", "etc/ssl/private"} {
		if err := os.MkdirAll(filepath.Join(probe, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(probe, "etc", "shadow"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := Args(Options{
		WorkDir:     "/var/cache/zpkg/build/foo",
		SourceDir:   "/var/cache/zpkg/build/foo/src",
		RecipeDir:   "/usr/share/zignos/recipes/core/foo",
		CacheDir:    "/var/cache/zpkg",
		BuildScript: "/usr/share/zignos/recipes/core/foo/build",
		Env:         []string{"ZPKG_NAME=foo"},
		Network:     false,
		ProbeRoot:   probe,
	})
	for _, seq := range [][]string{
		{"--ro-bind", "/", "/"},
		{"--proc", "/proc"},
		{"--dev", "/dev"},
		{"--tmpfs", "/tmp"},
		{"--tmpfs", "/root"},
		{"--tmpfs", "/etc/ssh"},
		{"--ro-bind", "/dev/null", "/etc/shadow"},
		{"--ro-bind", "/usr/share/zignos/recipes/core/foo", "/usr/share/zignos/recipes/core/foo"},
		{"--bind", "/var/cache/zpkg/build/foo", "/var/cache/zpkg/build/foo"},
		{"--chdir", "/var/cache/zpkg/build/foo/src"},
		{"--unshare-pid"},
		{"--unshare-net"},
		{"--setenv", "ZPKG_NAME", "foo"},
		{"--cap-drop", "ALL"},
		{"--cap-add", "CAP_CHOWN"},
		{"--", "/bin/bash", "-c", umaskWrapper, "/bin/bash", "/usr/share/zignos/recipes/core/foo/build"},
	} {
		if !containsSeq(args, seq) {
			t.Errorf("args missing %v\nargs: %v", seq, args)
		}
	}
	if !containsSeq(args, []string{"--ro-bind", "/var/cache/zpkg", "/var/cache/zpkg"}) {
		t.Error("cache is not re-bound read-only")
	}

	online := Args(Options{WorkDir: "/w", SourceDir: "/w/src", BuildScript: "/b", Network: true, ProbeRoot: probe})
	if containsSeq(online, []string{"--unshare-net"}) {
		t.Error("network-enabled build still unshares net")
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "do-not-leak")
	t.Setenv("https_proxy", "http://proxy.example:3128")
	env := Env(EnvOptions{
		Root: "/", WorkDir: "/work", CacheDir: "/cache", Destroot: "/work/destroot",
		SourceDir: "/work/src", RecipeDir: "/recipe", Name: "foo", Version: "1",
		Network: false, Sandboxed: true, SourceDateEpoch: 946684800,
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HOME=/work/home", "GOCACHE=/work/gocache", "GOPATH=/work/gopath",
		"ZPKG_SANDBOX=bwrap", "ZPKG_BUILD_NET=0", "GOPROXY=off",
		"https_proxy=http://proxy.example:3128", "ZPKG_ROOT=/",
		"SOURCE_DATE_EPOCH=946684800", "PYTHONHASHSEED=0",
		"KBUILD_BUILD_TIMESTAMP=Sat Jan  1 00:00:00 UTC 2000",
		"KBUILD_BUILD_USER=zignos", "KBUILD_BUILD_HOST=zignos-build",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY") {
		t.Error("host secret leaked into build environment")
	}
}

func TestCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	ok := filepath.Join(dir, "bwrap-ok")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Check(ok); err != nil {
		t.Fatalf("Check(ok): %v", err)
	}
	bad := filepath.Join(dir, "bwrap-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho no pivot_root >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Check(bad); err == nil {
		t.Fatal("Check(bad) succeeded")
	}
}

func TestPrepareWork(t *testing.T) {
	work := t.TempDir()
	if err := PrepareWork(work); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"home", "gocache", "gopath"} {
		if fi, err := os.Stat(filepath.Join(work, d)); err != nil || !fi.IsDir() {
			t.Fatalf("%s missing: %v", d, err)
		}
	}
}

func containsSeq(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j := range seq {
			if args[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
