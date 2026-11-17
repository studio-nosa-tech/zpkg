package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

// phase prints an emerge-style progress marker before/after a build or
// install step: ">>> Building fastfetch-2.71.0-r2". Markers are green when
// stdout is a terminal (logs and pipes stay plain) unless NO_COLOR is set.
func phase(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if useColor() {
		fmt.Printf(">>> \x1b[1;32m%s\x1b[0m\n", msg)
	} else {
		fmt.Printf(">>> %s\n", msg)
	}
}

// useColor reports whether ANSI color is appropriate for stdout.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// pkgLabel is the name-version-rN form used in progress markers.
func pkgLabel(r *recipe.Recipe) string {
	return r.Name + "-" + r.FullVersion()
}

// elapsed renders a build duration compactly: 0.4s, 12s, 1m2s.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
