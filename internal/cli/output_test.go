package cli

import (
	"testing"
	"time"
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
