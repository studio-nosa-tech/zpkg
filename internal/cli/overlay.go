package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/overlay"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
)

const overlayUsage = `zpkg overlay — additional recipe repositories (ZIGNOS MENU)

Usage:
  zpkg overlay add <name> [url] [--branch B] [--priority N] [--allow-unsigned] [--no-sync]
  zpkg overlay list
  zpkg overlay sync [name]
  zpkg overlay remove <name> [--keep-tree]

The official overlay "zignos-menu" (ZIGNOS MENU) has a built-in URL, so
"zpkg overlay add zignos-menu" is enough. Overlays are searched before the
official tree, so they can add packages or override official recipes.
`

func cmdOverlay(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(overlayUsage)
		return nil
	}
	switch args[0] {
	case "add":
		return overlayAdd(args[1:])
	case "list":
		return overlayList(args[1:])
	case "sync":
		return overlaySyncCmd(args[1:])
	case "remove":
		return overlayRemove(args[1:])
	default:
		return fmt.Errorf("unknown overlay command %q (use add, list, sync or remove)", args[0])
	}
}

func overlayAdd(args []string) error {
	var name, url, branch string
	priority := 10
	allowUnsigned, allowSet, noSync := false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			fmt.Print(overlayUsage)
			return nil
		case a == "--allow-unsigned":
			allowUnsigned, allowSet = true, true
		case a == "--no-sync":
			noSync = true
		case a == "--branch":
			if i+1 >= len(args) {
				return fmt.Errorf("--branch needs an argument")
			}
			i++
			branch = args[i]
		case strings.HasPrefix(a, "--branch="):
			branch = strings.TrimPrefix(a, "--branch=")
		case a == "--priority":
			if i+1 >= len(args) {
				return fmt.Errorf("--priority needs an argument")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --priority %q", args[i])
			}
			priority = n
		case strings.HasPrefix(a, "--priority="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--priority="))
			if err != nil {
				return fmt.Errorf("invalid --priority %q", a)
			}
			priority = n
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		default:
			if name == "" {
				name = a
			} else if url == "" {
				url = a
			} else {
				return fmt.Errorf("unexpected argument %q", a)
			}
		}
	}
	if name == "" {
		return fmt.Errorf("usage: zpkg overlay add <name> [url]")
	}
	if !overlay.ValidName(name) {
		return fmt.Errorf("invalid overlay name %q (use lowercase letters, digits, '.', '_' and '-')", name)
	}
	officialDefault := false
	if url == "" && name == overlay.OfficialName {
		url = overlay.OfficialURL
		if !allowSet {
			allowUnsigned = true
			officialDefault = true
		}
	}
	if url == "" {
		return fmt.Errorf("overlay %s needs a URL", name)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if overlay.ByName(cfg.Overlays, name) != nil {
		return fmt.Errorf("overlay %s is already configured; use \"zpkg overlay sync %s\" or remove it first", name, name)
	}
	spec := overlay.Spec{
		Name: name, URL: url, Branch: branch, Priority: priority,
		AllowUnsigned: allowUnsigned, Enabled: true,
	}
	if spec.Branch == "" {
		spec.Branch = "main"
	}
	if !noSync {
		stats, err := syncOverlay(cfg, spec, false)
		if err != nil {
			return err
		}
		printOverlayStats(name, stats)
	}
	if err := overlay.WriteSpec(cfg.Root, spec); err != nil {
		if !noSync {
			_ = os.RemoveAll(overlay.TreeDir(cfg.Root, name))
		}
		return err
	}
	if officialDefault {
		fmt.Fprintln(os.Stderr, "zpkg: warning: the official ZIGNOS MENU overlay is currently unsigned; ALLOW_UNSIGNED=1 was recorded for it")
	}
	phase("Added overlay %s", name)
	return nil
}

func overlayList(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: zpkg overlay list")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Overlays) == 0 {
		fmt.Println("no overlays configured")
		fmt.Printf("add the official one with: zpkg overlay add %s\n", overlay.OfficialName)
		return nil
	}
	fmt.Printf("%-16s %-8s %-8s %-10s %-8s %-8s %s\n",
		"NAME", "PRIORITY", "ACTIVE", "CHANNELS", "STATUS", "PACKAGES", "URL")
	for _, ov := range cfg.Overlays {
		active := "yes"
		if !ov.Active {
			active = "no"
		}
		channels := "all"
		if len(ov.Meta.Channels) > 0 {
			channels = strings.Join(ov.Meta.Channels, ",")
		}
		status := "synced"
		if ov.Err != "" {
			status = ov.Err
		}
		pkgs := overlayPackageCount(ov)
		trust := "unsigned"
		if ov.Present {
			if m, err := recipesig.ReadManifest(ov.Dir); err == nil && m.KeyID != "" {
				trust = fmt.Sprintf("gen %d", m.Generation)
			}
		}
		name := ov.Name
		if ov.Meta.Name != "" && ov.Meta.Name != ov.Name {
			name += " (" + ov.Meta.Name + ")"
		}
		fmt.Printf("%-16s %-8d %-8s %-10s %-8s %-8d %-8s %s\n",
			name, ov.Priority, active, channels, status, pkgs, trust, ov.URL)
	}
	return nil
}

func overlaySyncCmd(args []string) error {
	var name string
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Print(overlayUsage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		case name == "":
			name = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var targets []overlay.Overlay
	if name != "" {
		ov := overlay.ByName(cfg.Overlays, name)
		if ov == nil {
			return fmt.Errorf("overlay %s is not configured", name)
		}
		targets = []overlay.Overlay{*ov}
	} else {
		targets = cfg.Overlays
	}
	if len(targets) == 0 {
		fmt.Printf("no overlays configured; add one with \"zpkg overlay add %s\"\n", overlay.OfficialName)
		return nil
	}
	for _, ov := range targets {
		if !ov.Enabled {
			fmt.Printf("Overlay %s is disabled; skipping\n", ov.Name)
			continue
		}
		if ov.URL == "" {
			return fmt.Errorf("overlay %s: %s", ov.Name, ov.Err)
		}
		stats, err := syncOverlay(cfg, ov.Spec, false)
		if err != nil {
			return fmt.Errorf("overlay %s: %w", ov.Name, err)
		}
		printOverlayStats(ov.Name, stats)
	}
	return nil
}

func overlayRemove(args []string) error {
	keepTree := false
	var name string
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Print(overlayUsage)
			return nil
		case a == "--keep-tree":
			keepTree = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		case name == "":
			name = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if name == "" {
		return fmt.Errorf("usage: zpkg overlay remove <name> [--keep-tree]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ov := overlay.ByName(cfg.Overlays, name)
	if ov == nil {
		return fmt.Errorf("overlay %s is not configured", name)
	}
	countInstalled := 0
	var installed []string
	db := pkgdb.DB{Dir: cfg.DB}
	if refs, err := db.List(); err == nil {
		for _, ref := range refs {
			rec, err := db.Get(ref.Name, ref.Slot)
			if err != nil || rec.Meta == nil || rec.Meta.Overlay != name {
				continue
			}
			countInstalled++
			if len(installed) < 8 {
				installed = append(installed, rec.Name)
			}
		}
	}
	if countInstalled > 0 {
		fmt.Fprintf(os.Stderr, "zpkg: warning: %d installed package(s) came from overlay %s and remain installed: %s\n",
			countInstalled, name, strings.Join(installed, " "))
		fmt.Fprintf(os.Stderr, "zpkg: remove them separately with \"zpkg remove <pkg>...\"\n")
	}
	if err := overlay.RemoveSpec(cfg.Root, name); err != nil {
		return err
	}
	if !keepTree {
		if err := os.RemoveAll(ov.Dir); err != nil {
			return err
		}
		_ = os.Remove(ov.Dir + ".commit")
	}
	phase("Removed overlay %s", name)
	return nil
}

// overlayPackageCount counts unique package keys in an overlay without
// failing the list command when one recipe is broken.
func overlayPackageCount(ov overlay.Overlay) int {
	if !ov.Present || ov.Err != "" {
		return 0
	}
	stats, err := validateOverlayTree(ov.Dir)
	if err != nil {
		return 0
	}
	return stats.Packages
}

func printOverlayStats(name string, stats overlayStats) {
	kind := "unsigned"
	if stats.Signed {
		kind = fmt.Sprintf("signed generation %d, key %s", stats.Generation, shortCommit(stats.KeyID))
	}
	commit := ""
	if stats.Commit != "" {
		commit = " (" + shortCommit(stats.Commit) + ")"
	}
	if stats.Unchanged {
		phase("Overlay %s is up to date%s", name, commit)
	} else {
		phase("Overlay %s: %d package(s), %d categories, %s%s", name, stats.Packages, len(stats.Categories), kind, commit)
	}
	if len(stats.Shadowed) > 0 {
		show := stats.Shadowed
		if len(show) > 8 {
			show = show[:8]
		}
		fmt.Fprintf(os.Stderr, "zpkg: warning: overlay %s overrides %d base package(s): %s\n",
			name, len(stats.Shadowed), strings.Join(show, " "))
	}
}
