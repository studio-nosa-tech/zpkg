package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
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

// planEntry is one line of the pre-build summary.
type planEntry struct {
	label          string
	use            string
	videoCards     string
	usesVideoCards bool
	target         bool // named on the command line (not pulled in as a dependency)
}

// printPlan lists the packages in the order they will be handled, with
// their effective USE flags, and closes with a count — the "these are the
// packages that would be merged" block from emerge.
func printPlan(entries []planEntry, action string) {
	if len(entries) == 0 {
		return
	}
	heads := make([]string, len(entries))
	width := 0
	for i, e := range entries {
		head := fmt.Sprintf("[%d] %s", i+1, e.label)
		if !e.target {
			head += " (dependency)"
		}
		heads[i] = head
		if len(head) > width {
			width = len(head)
		}
	}

	targets, deps := 0, 0
	for _, e := range entries {
		if e.target {
			targets++
		} else {
			deps++
		}
	}

	fmt.Printf("\nThese are the packages that %s, in order:\n", action)
	for i, e := range entries {
		fmt.Printf("  %-*s  USE=%q", width, heads[i], e.use)
		if e.usesVideoCards {
			fmt.Printf(" VIDEO_CARDS=%q", e.videoCards)
		}
		fmt.Println()
	}
	total := fmt.Sprintf("Total: %d %s", len(entries), plural(len(entries), "package", "packages"))
	if deps > 0 {
		total += fmt.Sprintf(" (%d %s, %d %s)",
			targets, plural(targets, "target", "targets"),
			deps, plural(deps, "dependency", "dependencies"))
	}
	fmt.Println(total)
	fmt.Println()
}

// useString renders the package's USE flags for the plan: every flag the
// recipe declares in iuse (prefixed with - when disabled), plus any enabled
// flag the recipe does not list.
func useString(r *recipe.Recipe, enabled []string) string {
	on := make(map[string]bool, len(enabled))
	for _, f := range enabled {
		on[f] = true
	}
	flags := append([]string{}, r.IUse...)
	for _, f := range enabled {
		declared := false
		for _, u := range r.IUse {
			if u == f {
				declared = true
				break
			}
		}
		if !declared {
			flags = append(flags, f)
		}
	}
	sort.Strings(flags)
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		// video_cards_* entries are declared in iuse only so older zpkg
		// releases can parse the recipe tree; they are not USE flags.
		if strings.HasPrefix(f, "video_cards_") {
			continue
		}
		if on[f] {
			out = append(out, f)
		} else {
			out = append(out, "-"+f)
		}
	}
	return strings.Join(out, " ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// elapsed renders a build duration compactly: 0.4s, 12s, 1m2s.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
