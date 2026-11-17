package cli

import (
	"testing"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

func TestElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{100 * time.Millisecond, "100ms"},
		{900 * time.Millisecond, "900ms"},
		{time.Second, "1s"},
		{62 * time.Second, "1m2s"},
		{90 * time.Second, "1m30s"},
	}
	for _, c := range cases {
		if got := elapsed(c.d); got != c.want {
			t.Errorf("elapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestUseColorRespectsEnvironment(t *testing.T) {
	// Under "go test" stdout is a pipe, so color is off by default; the env
	// switches must keep it off as well.
	t.Setenv("NO_COLOR", "1")
	if useColor() {
		t.Error("NO_COLOR=1 must disable color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if useColor() {
		t.Error("TERM=dumb must disable color")
	}
}

func TestUseString(t *testing.T) {
	// Declared flags appear with - when off; enabled flags the recipe does
	// not declare are appended; the result is sorted.
	r := &recipe.Recipe{IUse: []string{"ssl", "zlib"}}
	if got, want := useString(r, []string{"ssl", "extra"}), "extra ssl -zlib"; got != want {
		t.Errorf("useString = %q, want %q", got, want)
	}
	if got, want := useString(&recipe.Recipe{}, []string{"b", "a"}), "a b"; got != want {
		t.Errorf("useString without iuse = %q, want %q", got, want)
	}
	if got := useString(&recipe.Recipe{IUse: []string{"x"}}, nil); got != "-x" {
		t.Errorf("useString all off = %q, want -x", got)
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "package", "packages"); got != "package" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(0, "package", "packages"); got != "packages" {
		t.Errorf("plural(0) = %q", got)
	}
	if got := plural(2, "dependency", "dependencies"); got != "dependencies" {
		t.Errorf("plural(2) = %q", got)
	}
}
