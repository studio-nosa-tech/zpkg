package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/install"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/upgrade"
	"github.com/studio-nosa-tech/zpkg/internal/world"
)

type upgradeOptions struct {
	pretend        bool
	yes            bool
	newuse         bool
	prune          bool
	allowDowngrade bool
	verify         bool
	noSandbox      bool
	clean          bool
	reproCheck     bool
	channel        string
	targets        []string
}

func parseUpgradeArgs(args []string) (upgradeOptions, error) {
	var o upgradeOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--pretend":
			o.pretend = true
		case a == "-y" || a == "--yes":
			o.yes = true
		case a == "--newuse":
			o.newuse = true
		case a == "--prune":
			o.prune = true
		case a == "--allow-downgrade":
			o.allowDowngrade = true
		case a == "--verify":
			o.verify = true
		case a == "--no-sandbox":
			o.noSandbox = true
		case a == "--clean":
			o.clean = true
		case a == "--repro-check":
			o.reproCheck = true
		case a == "--channel":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--channel needs an argument")
			}
			i++
			o.channel = args[i]
		case strings.HasPrefix(a, "--channel="):
			o.channel = strings.TrimPrefix(a, "--channel=")
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q", a)
		default:
			o.targets = append(o.targets, a)
		}
	}
	return o, nil
}

// cmdUpgrade shows what will change, builds every package, then installs the
// set and verifies the result. A failed build changes nothing; a failed
// install rolls back the in-flight package and leaves a resumable state.
func cmdUpgrade(args []string) error {
	o, err := parseUpgradeArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if o.channel != "" {
		if err := config.ApplyChannel(&cfg, o.channel); err != nil {
			return err
		}
	}
	if o.noSandbox {
		cfg.SandboxMode = "off"
	}
	if err := requireRepos(cfg); err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}

	targets := o.targets
	if len(targets) == 0 {
		targets, err = world.Read(cfg.DB)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			fmt.Println("world is empty; install something first")
			return nil
		}
	}

	plan, err := upgrade.Build(cfg, db, targets, upgrade.Options{NewUse: o.newuse})
	if err != nil {
		return err
	}
	printUpgradePlan(plan, o)

	if o.pretend {
		return nil
	}
	hasActions := len(plan.Entries) > 0
	prunable := planPrunableOrphans(plan)
	if !hasActions && (len(prunable) == 0 || !o.prune) {
		fmt.Println("nothing to upgrade")
		if err := upgrade.WriteAppliedChannel(db, cfg.Channel); err != nil {
			return err
		}
		return nil
	}

	needPrompt := !o.yes
	if plan.HasDowngrades() {
		if o.allowDowngrade {
			// The explicit flag is the confirmation for a downgrade.
			needPrompt = false
		} else {
			ok, err := promptYes("Downgrades are included. Continue? [y/N]", false)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("aborted; nothing was changed")
				return nil
			}
			needPrompt = false
		}
	}
	if needPrompt {
		ok, err := promptYes("Proceed? [Y/n]", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted; nothing was changed")
			return nil
		}
	}

	if hasActions {
		if plan.Resolution == nil {
			return fmt.Errorf("internal error: upgrade plan has no resolution")
		}
		// New dependencies must be present in the build root before the
		// packages that need them are built. Install only the additive
		// KindInstall entries first; upgrades/rebuilds stay deferred so a
		// later build failure does not replace any installed version.
		newDeps, changes := splitUpgradeEntries(plan.Entries)
		before := snapshotVersions(db, plan.Entries)
		if len(newDeps) > 0 {
			phase("Installing %d new dependency(s) before building", len(newDeps))
			installOpts := installOptions{
				force:      true,
				asdeps:     true,
				noSandbox:  o.noSandbox,
				clean:      o.clean,
				reproCheck: o.reproCheck,
				skipPlan:   true,
				channel:    o.channel,
			}
			keys := keySet(newDeps)
			if err := installResolved(cfg, plan.Resolution, keys, keys, true, installOpts); err != nil {
				printUpgradeRecovery(plan, db, before)
				return fmt.Errorf("upgrade failed: %w", err)
			}
		}
		if len(changes) > 0 {
			installOpts := installOptions{
				force:        true,
				asdeps:       true,
				noSandbox:    o.noSandbox,
				clean:        o.clean,
				reproCheck:   o.reproCheck,
				deferInstall: true,
				skipPlan:     true,
				channel:      o.channel,
			}
			phase("Building %d package(s) before installing", len(changes))
			keys := keySet(changes)
			if err := installResolved(cfg, plan.Resolution, keys, keys, true, installOpts); err != nil {
				printUpgradeRecovery(plan, db, before)
				return fmt.Errorf("upgrade failed: %w", err)
			}
		}
		// Record the resolved key (name@slot) rather than the raw atom, so
		// "upgrade virtual/foo" stores the provider and ">=foo-2" stores the
		// package it actually selected.
		for i, raw := range o.targets {
			key := raw
			if i < len(plan.Resolution.Targets) && plan.Resolution.Targets[i] != "" {
				key = plan.Resolution.Targets[i]
			}
			if err := world.Add(cfg.DB, key); err != nil {
				return err
			}
		}
	}

	if o.prune && len(prunable) > 0 {
		for _, orphan := range prunable {
			phase("Pruning %s-%s", install.SlotName(orphan.Name, orphan.Slot), orphan.Version)
			if err := install.RemoveSlot(orphan.Name, orphan.Slot, cfg.Root, db); err != nil {
				return fmt.Errorf("prune %s: %w", pkgdb.SlotKey(orphan.Name, orphan.Slot), err)
			}
		}
	}

	phase("Checking installed system")
	report, err := upgrade.Check(cfg, db, plan, o.verify)
	if err != nil {
		return fmt.Errorf("%w\nre-run \"zpkg upgrade\" after fixing the cause; run \"zpkg recover\" if an operation was interrupted", err)
	}
	phase("Integrity check OK (%d packages, %d files)", report.Packages, report.Files)
	if err := upgrade.WriteAppliedChannel(db, cfg.Channel); err != nil {
		return err
	}
	return nil
}

func printUpgradePlan(plan *upgrade.Plan, o upgradeOptions) {
	src := channelSourceLabel(plan.ChannelSource)
	line := fmt.Sprintf("Channel: %s (%s", plan.Channel, src)
	if plan.PreviousChannel != "" && plan.PreviousChannel != plan.Channel {
		line += fmt.Sprintf("; last applied: %s", plan.PreviousChannel)
	}
	line += ")"
	fmt.Println(line)
	for _, w := range plan.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}

	if len(plan.Entries) == 0 {
		fmt.Println("No package changes.")
	} else {
		fmt.Println("These are the packages that will be upgraded, in order:")
		for i, e := range plan.Entries {
			label := install.SlotName(e.Name, e.Slot)
			switch e.Kind {
			case upgrade.KindInstall:
				fmt.Printf("  [%d] %s %s (new dependency)\n", i+1, label, e.NewVersion)
			case upgrade.KindRebuild:
				fmt.Printf("  [%d] %s %s (rebuild: USE flags changed)\n", i+1, label, e.NewVersion)
			case upgrade.KindDowngrade:
				fmt.Printf("  [%d] %s %s -> %s (downgrade)\n", i+1, label, e.OldVersion, e.NewVersion)
			default:
				fmt.Printf("  [%d] %s %s -> %s\n", i+1, label, e.OldVersion, e.NewVersion)
			}
			if s := depDiffLine("deps", e.DepsAdded, e.DepsRemoved, e.DepsChanged); s != "" {
				fmt.Println("      " + s)
			}
			if s := depDiffLine("makedeps", e.BuildAdded, e.BuildRemoved, e.BuildChanged); s != "" {
				fmt.Println("      " + s)
			}
			for _, w := range e.Warnings {
				fmt.Printf("      warning: %s\n", w)
			}
		}
	}

	if len(plan.Orphans) > 0 {
		label := "No longer required (use --prune to remove):"
		if o.prune {
			label = "No longer required:"
		}
		fmt.Println(label)
		for _, orphan := range plan.Orphans {
			note := ""
			if o.prune {
				if orphan.Prunable() {
					note = " (will be removed)"
				} else {
					note = fmt.Sprintf(" (kept: required by %s)", strings.Join(orphan.RequiredBy, ", "))
				}
			}
			fmt.Printf("  %s-%s%s\n", install.SlotName(orphan.Name, orphan.Slot), orphan.Version, note)
		}
	}

	if len(plan.Entries) > 0 || len(plan.Orphans) > 0 {
		c := plan.Counts()
		var parts []string
		add := func(n int, singular, plural string) {
			if n > 0 {
				if n == 1 {
					parts = append(parts, fmt.Sprintf("%d %s", n, singular))
				} else {
					parts = append(parts, fmt.Sprintf("%d %s", n, plural))
				}
			}
		}
		add(c[upgrade.KindUpgrade], "upgrade", "upgrades")
		add(c[upgrade.KindInstall], "new dependency", "new dependencies")
		add(c[upgrade.KindRebuild], "rebuild", "rebuilds")
		add(c[upgrade.KindDowngrade], "downgrade", "downgrades")
		add(len(plan.Orphans), "orphan", "orphans")
		fmt.Printf("Total: %s\n", strings.Join(parts, ", "))
	}
}

func depDiffLine(label string, added, removed []string, changed []upgrade.DepChange) string {
	var parts []string
	for _, d := range added {
		parts = append(parts, "+"+d)
	}
	for _, d := range removed {
		parts = append(parts, "-"+d)
	}
	for _, d := range changed {
		parts = append(parts, fmt.Sprintf("~%s (%s -> %s)", d.Name, d.Old, d.New))
	}
	if len(parts) == 0 {
		return ""
	}
	return label + ": " + strings.Join(parts, " ")
}

func planPrunableOrphans(plan *upgrade.Plan) []upgrade.Orphan {
	var out []upgrade.Orphan
	for _, orphan := range plan.Orphans {
		if orphan.Prunable() {
			out = append(out, orphan)
		}
	}
	return out
}

// splitUpgradeEntries separates packages that are not installed yet from
// packages whose installed version/recipe is being replaced. New packages can
// be installed before the build phase without changing an existing package;
// upgrades must stay deferred until every build has succeeded.
func splitUpgradeEntries(entries []upgrade.Entry) (newDeps, changes []string) {
	newDeps = make([]string, 0, len(entries))
	changes = make([]string, 0, len(entries))
	for _, e := range entries {
		key := pkgdb.SlotKey(e.Name, e.Slot)
		if e.Kind == upgrade.KindInstall {
			newDeps = append(newDeps, key)
		} else {
			changes = append(changes, key)
		}
	}
	return newDeps, changes
}

func keySet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func snapshotVersions(db pkgdb.DB, entries []upgrade.Entry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		k := pkgdb.SlotKey(e.Name, e.Slot)
		rec, err := db.Get(e.Name, e.Slot)
		if err != nil {
			m[k] = ""
			continue
		}
		m[k] = rec.VersionString()
	}
	return m
}

func printUpgradeRecovery(plan *upgrade.Plan, db pkgdb.DB, before map[string]string) {
	var changed []string
	for _, e := range plan.Entries {
		k := pkgdb.SlotKey(e.Name, e.Slot)
		rec, err := db.Get(e.Name, e.Slot)
		if err != nil {
			continue
		}
		if rec.VersionString() != before[k] {
			changed = append(changed, fmt.Sprintf("%s %s", k, rec.VersionString()))
		}
	}
	if len(changed) == 0 {
		fmt.Fprintln(os.Stderr, "zpkg: no packages were changed (the build phase failed)")
	} else {
		fmt.Fprintf(os.Stderr, "zpkg: packages changed before the failure: %s\n", strings.Join(changed, ", "))
	}
	fmt.Fprintln(os.Stderr, "zpkg: re-run \"zpkg upgrade\" to resume; run \"zpkg recover\" if an operation was interrupted")
}

func promptYes(question string, def bool) (bool, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false, fmt.Errorf("stdin is not a terminal; use --yes to proceed")
	}
	fmt.Print(question + " ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def, nil
	}
	return line == "y" || line == "yes", nil
}

// cmdWorld prints the explicitly installed set with the installed version
// of every slot. Entries that name a slot (name@slot) print only that slot.
func cmdWorld() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db := pkgdb.DB{Dir: cfg.DB}
	names, err := world.Read(cfg.DB)
	if err != nil {
		return err
	}
	for _, w := range names {
		name, slot := pkgdb.SplitKey(w)
		explicitSlot := strings.ContainsRune(w, '@')
		recs, err := db.GetAll(name)
		if err != nil {
			return err
		}
		printed := false
		for _, rec := range recs {
			if explicitSlot && rec.Slot != slot {
				continue
			}
			fmt.Printf("%s %s\n", install.SlotName(name, rec.Slot), rec.VersionString())
			printed = true
		}
		if !printed {
			fmt.Printf("%s (not installed)\n", w)
		}
	}
	return nil
}
