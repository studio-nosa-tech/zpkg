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
		names := make([]string, 0, len(plan.Entries))
		for _, e := range plan.Entries {
			names = append(names, e.Name)
		}
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
		before := snapshotVersions(db, plan.Entries)
		phase("Building %d package(s) before installing", len(names))
		if err := cmdBuildInstall(names, true, installOpts); err != nil {
			printUpgradeRecovery(plan, db, before)
			return fmt.Errorf("upgrade failed: %w", err)
		}
		for _, name := range o.targets {
			if err := world.Add(cfg.DB, name); err != nil {
				return err
			}
		}
	}

	if o.prune && len(prunable) > 0 {
		for _, orphan := range prunable {
			phase("Pruning %s-%s", orphan.Name, orphan.Version)
			if err := install.Remove(orphan.Name, cfg.Root, db); err != nil {
				return fmt.Errorf("prune %s: %w", orphan.Name, err)
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
			switch e.Kind {
			case upgrade.KindInstall:
				fmt.Printf("  [%d] %s %s (new dependency)\n", i+1, e.Name, e.NewVersion)
			case upgrade.KindRebuild:
				fmt.Printf("  [%d] %s %s (rebuild: USE flags changed)\n", i+1, e.Name, e.NewVersion)
			case upgrade.KindDowngrade:
				fmt.Printf("  [%d] %s %s -> %s (downgrade)\n", i+1, e.Name, e.OldVersion, e.NewVersion)
			default:
				fmt.Printf("  [%d] %s %s -> %s\n", i+1, e.Name, e.OldVersion, e.NewVersion)
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
			fmt.Printf("  %s-%s%s\n", orphan.Name, orphan.Version, note)
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

func snapshotVersions(db pkgdb.DB, entries []upgrade.Entry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		rec, err := db.Get(e.Name, e.Slot)
		if err != nil {
			m[e.Name] = ""
			continue
		}
		m[e.Name] = rec.VersionString()
	}
	return m
}

func printUpgradeRecovery(plan *upgrade.Plan, db pkgdb.DB, before map[string]string) {
	var changed []string
	for _, e := range plan.Entries {
		rec, err := db.Get(e.Name, e.Slot)
		if err != nil {
			continue
		}
		if rec.VersionString() != before[e.Name] {
			changed = append(changed, fmt.Sprintf("%s %s", e.Name, rec.VersionString()))
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
// of every slot.
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
	for _, n := range names {
		recs, err := db.GetAll(n)
		if err != nil || len(recs) == 0 {
			fmt.Printf("%s (not installed)\n", n)
			continue
		}
		for _, rec := range recs {
			fmt.Printf("%s %s\n", install.SlotName(n, rec.Slot), rec.VersionString())
		}
	}
	return nil
}
