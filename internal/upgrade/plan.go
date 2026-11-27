// Package upgrade plans and verifies rolling-release upgrades.
//
// A Plan is computed before anything is built or installed. It compares the
// selected release channel's recipe closure with the installed packages and
// reports upgrades, rebuilds, downgrades, new dependencies, dependency
// changes, orphaned packages and breaking-change warnings.
package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/depspec"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/resolve"
	"github.com/studio-nosa-tech/zpkg/internal/version"
)

// Kind is what an upgrade entry will do.
type Kind string

const (
	KindInstall   Kind = "install"   // dependency not installed yet
	KindUpgrade   Kind = "upgrade"   // newer recipe version
	KindRebuild   Kind = "rebuild"   // same version, USE flags or recipe content changed
	KindDowngrade Kind = "downgrade" // recipe version is older (channel switch)
)

// DepChange is one dependency whose expression changed.
type DepChange struct {
	Name string
	Old  string
	New  string
}

// Entry is one package the upgrade will change.
type Entry struct {
	Name         string
	Slot         string
	Kind         Kind
	OldVersion   string
	NewVersion   string
	Recipe       *recipe.Recipe
	Installed    *pkgdb.Rec
	DepsAdded    []string
	DepsRemoved  []string
	DepsChanged  []DepChange
	BuildAdded   []string
	BuildRemoved []string
	BuildChanged []DepChange
	Warnings     []string
}

// Orphan is an installed package no longer required by the selected channel.
type Orphan struct {
	Name       string
	Slot       string
	Version    string
	RequiredBy []string
}

// Prunable reports whether --prune can remove the orphan safely.
func (o Orphan) Prunable() bool { return len(o.RequiredBy) == 0 }

// Options controls plan construction.
type Options struct {
	NewUse bool // also rebuild packages whose effective USE flags changed
}

// Plan is the complete pre-upgrade picture.
type Plan struct {
	Channel         string
	ChannelSource   string
	PreviousChannel string
	ChannelChanged  bool
	Targets         []string
	Entries         []Entry
	Orphans         []Orphan
	Closure         []string
	Warnings        []string

	// Resolution is the exact candidate set the plan was built from.
	// installResolved uses it so the build phase cannot pick different
	// versions by re-resolving the same names.
	Resolution *resolve.Result
}

// Actions returns entries that require building/installing.
func (p *Plan) Actions() []Entry { return p.Entries }

// HasDowngrades reports whether any entry goes backwards.
func (p *Plan) HasDowngrades() bool {
	for _, e := range p.Entries {
		if e.Kind == KindDowngrade {
			return true
		}
	}
	return false
}

// Counts returns the number of entries per kind.
func (p *Plan) Counts() map[Kind]int {
	m := map[Kind]int{}
	for _, e := range p.Entries {
		m[e.Kind]++
	}
	return m
}

// Build resolves targets against the selected channel and compares the
// result with the installed database. It never changes anything.
func Build(cfg config.Config, db pkgdb.DB, targets []string, opts Options) (*Plan, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no upgrade targets")
	}
	idx, err := resolve.NewIndex(cfg.Repos)
	if err != nil {
		return nil, err
	}
	recs, err := installedRecords(db)
	if err != nil {
		return nil, err
	}
	res, err := (&resolve.Resolver{Index: idx, Options: cfg.Options, Installed: recs, Prefer: resolve.PreferNewest}).Resolve(targets)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]*pkgdb.Rec, len(recs))
	slots := make(map[string][]string)
	for _, rec := range recs {
		byKey[pkgdb.SlotKey(rec.Name, rec.Slot)] = rec
		slots[rec.Name] = append(slots[rec.Name], rec.Slot)
	}

	plan := &Plan{
		Channel:       cfg.Channel,
		ChannelSource: cfg.ChannelSource,
		Targets:       append([]string{}, targets...),
		Resolution:    res,
	}
	plan.PreviousChannel = ReadAppliedChannel(db)
	if plan.PreviousChannel != "" && plan.PreviousChannel != cfg.Channel {
		plan.ChannelChanged = true
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("channel changed: %s -> %s", plan.PreviousChannel, cfg.Channel))
	}

	closure := make(map[string]bool, len(res.Order))
	for _, key := range res.Order {
		closure[key] = true
		plan.Closure = append(plan.Closure, key)

		c := res.Selected[key]
		if c == nil || c.Recipe == nil {
			continue
		}
		r := c.Recipe
		rec := byKey[pkgdb.SlotKey(c.Name, c.Slot)]
		use := cfg.Options.ForPackage(c.Name).Use
		e := Entry{
			Name: c.Name, Slot: c.Slot, NewVersion: r.FullVersion(),
			Recipe: r, Installed: rec,
		}
		if rec == nil {
			e.Kind = KindInstall
			if otherSlots := slots[c.Name]; len(otherSlots) > 0 {
				e.Warnings = append(e.Warnings,
					fmt.Sprintf("installed in slot %s; recipe now uses slot %s (old slot remains)", strings.Join(otherSlots, ","), c.Slot))
			}
		} else {
			e.OldVersion = rec.VersionString()
			switch cmp := version.Compare(rec.VersionString(), r.FullVersion()); {
			case cmp < 0:
				e.Kind = KindUpgrade
			case cmp > 0:
				e.Kind = KindDowngrade
			case opts.NewUse && useFlagsChanged(rec, cfg):
				e.Kind = KindRebuild
			case recipeChanged(rec, r):
				e.Kind = KindRebuild
			default:
				continue // up to date
			}
			e.Warnings = append(e.Warnings, upgradeWarnings(rec, r)...)
			if ov := cfg.OverlayName(r.Repo); ov != recMetaOverlay(rec) {
				e.Warnings = append(e.Warnings, fmt.Sprintf("origin changes: %s -> %s", originLabel(recMetaOverlay(rec)), originLabel(ov)))
			}
			fillDependencyDiff(&e, use)
		}
		plan.Entries = append(plan.Entries, e)
	}
	plan.Orphans = findOrphans(recs, installedClosure(recs, targets), closure, updatedDeps(plan.Entries, cfg))
	plan.Warnings = append(plan.Warnings, overlayWarnings(cfg, recs)...)
	return plan, nil
}

// overlayWarnings reports installed packages whose overlay is not active
// anymore. They stay installed and are satisfied by installed-only
// candidates, but no recipe exists to rebuild them.
func overlayWarnings(cfg config.Config, recs []*pkgdb.Rec) []string {
	active := map[string]bool{}
	for _, ov := range cfg.Overlays {
		if ov.Active {
			active[ov.Name] = true
		}
	}
	counts := map[string]int{}
	for _, rec := range recs {
		if rec.Meta == nil || rec.Meta.Overlay == "" || active[rec.Meta.Overlay] {
			continue
		}
		counts[rec.Meta.Overlay]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		out = append(out, fmt.Sprintf("%d installed package(s) come from overlay %q, which is not active; they are kept but cannot be rebuilt until it is added again", counts[name], name))
	}
	return out
}

// recMetaOverlay is the overlay recorded at install time ("" for the
// official tree, and for records written before overlays existed).
func recMetaOverlay(rec *pkgdb.Rec) string {
	if rec == nil || rec.Meta == nil {
		return ""
	}
	return rec.Meta.Overlay
}

// originLabel renders an overlay name for messages.
func originLabel(overlay string) string {
	if overlay == "" {
		return "the official tree"
	}
	return "overlay " + overlay
}

// installedClosure walks the installed DB from the upgrade targets using the
// dependencies recorded at install time. Slots are part of the identity, so
// the old closure is also slot-aware. Orphans are only packages that were
// required before this upgrade and are no longer required after it;
// unrelated installed packages (for example base packages that were never in
// world) must not be offered for pruning.
func installedClosure(recs []*pkgdb.Rec, targets []string) map[string]bool {
	byName := map[string][]*pkgdb.Rec{}
	providers := map[string][]*pkgdb.Rec{}
	for _, rec := range recs {
		byName[rec.Name] = append(byName[rec.Name], rec)
		if rec.Meta == nil {
			continue
		}
		for _, raw := range rec.Meta.Provides {
			if p, err := depspec.ParseProvided(raw); err == nil && p.Name != "" {
				providers[p.Name] = append(providers[p.Name], rec)
			}
		}
	}
	seen := map[string]bool{}
	var queue []*pkgdb.Rec
	for _, raw := range targets {
		a, err := depspec.Parse(raw)
		if err != nil {
			continue
		}
		queue = append(queue, matchInstalled(a, byName, providers)...)
	}
	for len(queue) > 0 {
		rec := queue[0]
		queue = queue[1:]
		k := pkgdb.SlotKey(rec.Name, rec.Slot)
		if seen[k] {
			continue
		}
		seen[k] = true
		if rec.Meta == nil {
			continue
		}
		rawDeps := append(append([]string{}, rec.Meta.Depends...), rec.Meta.BuildDeps...)
		for _, raw := range rawDeps {
			a, err := depspec.Parse(raw)
			if err != nil {
				continue
			}
			if a.Condition != "" {
				enabled := hasRecordedFlag(rec.Meta, a.Condition)
				if enabled == a.Negated {
					continue
				}
			}
			queue = append(queue, matchInstalled(a, byName, providers)...)
		}
	}
	return seen
}

func hasRecordedFlag(meta *pkgdb.Meta, cond string) bool {
	if card, ok := strings.CutPrefix(cond, "video_cards_"); ok {
		for _, c := range meta.VideoCards {
			if c == card {
				return true
			}
		}
		return false
	}
	for _, f := range meta.UseFlags {
		if f == cond {
			return true
		}
	}
	return false
}

// matchInstalled returns every installed record satisfying a. A slot-pinned
// atom only matches that slot; a bare atom can match any slot.
func matchInstalled(a depspec.Atom, byName map[string][]*pkgdb.Rec, providers map[string][]*pkgdb.Rec) []*pkgdb.Rec {
	var out []*pkgdb.Rec
	seen := map[string]bool{}
	add := func(rec *pkgdb.Rec, v string) {
		if a.Slot != "" && rec.Slot != a.Slot {
			return
		}
		if !a.Matches(v) {
			return
		}
		k := pkgdb.SlotKey(rec.Name, rec.Slot)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, rec)
	}
	for _, rec := range byName[a.Name] {
		add(rec, rec.VersionString())
	}
	if a.Slot == "" {
		for _, rec := range providers[a.Name] {
			if rec.Name == a.Name {
				continue
			}
			v := rec.VersionString()
			if rec.Meta != nil {
				for _, raw := range rec.Meta.Provides {
					p, err := depspec.ParseProvided(raw)
					if err == nil && p.Name == a.Name {
						if p.Version != "" {
							v = p.Version
						}
						break
					}
				}
			}
			add(rec, v)
		}
	}
	return out
}

// updatedDeps returns the dependency names each planned package will have
// after the upgrade. Orphan detection must use these, not the old metadata:
// the whole point of an upgrade is often to drop a dependency.
func updatedDeps(entries []Entry, cfg config.Config) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(entries))
	for _, e := range entries {
		use := cfg.Options.ForPackage(e.Name).Use
		names := map[string]bool{}
		for name := range activeDeps(e.Recipe.Depends, use) {
			names[name] = true
		}
		for name := range activeDeps(e.Recipe.MakeDepends, use) {
			names[name] = true
		}
		out[pkgdb.SlotKey(e.Name, e.Slot)] = names
	}
	return out
}

func installedRecords(db pkgdb.DB) ([]*pkgdb.Rec, error) {
	refs, err := db.List()
	if err != nil {
		return nil, err
	}
	recs := make([]*pkgdb.Rec, 0, len(refs))
	for _, ref := range refs {
		rec, err := db.Get(ref.Name, ref.Slot)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func useFlagsChanged(rec *pkgdb.Rec, cfg config.Config) bool {
	if rec.Meta == nil {
		return false
	}
	want := cfg.Options.ForPackage(rec.Name).Use
	got := rec.Meta.UseFlags
	if len(want) != len(got) {
		return true
	}
	for i := range want {
		if want[i] != got[i] {
			return true
		}
	}
	// VideoCards is non-nil only for recipes that consume it. An empty
	// selection is a valid configured state, so compare lengths too.
	if rec.Meta.VideoCards != nil {
		wantCards := cfg.Options.VideoCards
		if len(wantCards) != len(rec.Meta.VideoCards) {
			return true
		}
		for i := range wantCards {
			if wantCards[i] != rec.Meta.VideoCards[i] {
				return true
			}
		}
	}
	return false
}

// activeDeps evaluates conditional atoms with a USE flag set and returns
// name -> raw atom (first occurrence wins).
func activeDeps(raws, use []string) map[string]string {
	useSet := make(map[string]bool, len(use))
	for _, f := range use {
		useSet[f] = true
	}
	out := map[string]string{}
	for _, raw := range raws {
		a, err := depspec.Parse(raw)
		if err != nil || a.Name == "" {
			continue
		}
		if a.Condition != "" {
			enabled := useSet[a.Condition]
			if a.Negated {
				enabled = !enabled
			}
			if !enabled {
				continue
			}
		}
		if _, ok := out[a.Name]; !ok {
			out[a.Name] = a.Raw
		}
	}
	return out
}

// recipeChanged reports whether the installed package was built from a
// different recipe than the one now selected. Versions can stay equal across
// channels (LTS backports) while the recipe content differs; such a package
// must be rebuilt so the channel switch is real.
func recipeChanged(rec *pkgdb.Rec, r *recipe.Recipe) bool {
	if rec.Meta == nil || rec.Meta.RecipeDigest == "" || r.Dir == "" {
		return false
	}
	digest, err := provenance.RecipeDigest(r.Dir)
	if err != nil || digest == "" {
		return false
	}
	return rec.Meta.RecipeDigest != digest
}

func fillDependencyDiff(e *Entry, use []string) {
	newDeps := activeDeps(e.Recipe.Depends, use)
	newBuild := activeDeps(e.Recipe.MakeDepends, use)
	var oldDeps, oldBuild map[string]string
	if e.Installed != nil && e.Installed.Meta != nil {
		oldDeps = activeDeps(e.Installed.Meta.Depends, e.Installed.Meta.UseFlags)
		oldBuild = activeDeps(e.Installed.Meta.BuildDeps, e.Installed.Meta.UseFlags)
	} else {
		oldDeps, oldBuild = map[string]string{}, map[string]string{}
	}
	e.DepsAdded, e.DepsRemoved, e.DepsChanged = compareDeps(oldDeps, newDeps)
	e.BuildAdded, e.BuildRemoved, e.BuildChanged = compareDeps(oldBuild, newBuild)
}

func compareDeps(old, new map[string]string) (added, removed []string, changed []DepChange) {
	for name, raw := range new {
		if oldRaw, ok := old[name]; !ok {
			added = append(added, raw)
		} else if oldRaw != raw {
			changed = append(changed, DepChange{Name: name, Old: oldRaw, New: raw})
		}
	}
	for name, raw := range old {
		if _, ok := new[name]; !ok {
			removed = append(removed, raw)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Slice(changed, func(i, j int) bool { return changed[i].Name < changed[j].Name })
	return added, removed, changed
}

// criticalPackages get a major-version warning even when the recipe does not
// ship an explicit upgrade_warning.
var criticalPackages = map[string]bool{
	"glibc": true, "gcc": true, "binutils": true, "linux": true,
	"linux-headers": true, "openrc": true, "initscripts": true,
	"eudev": true, "busybox": true, "shadow": true, "util-linux": true,
	"e2fsprogs": true, "kbd": true, "zpkg": true,
}

func upgradeWarnings(rec *pkgdb.Rec, r *recipe.Recipe) []string {
	var out []string
	if r.UpgradeWarning != "" {
		out = append(out, r.UpgradeWarning)
	}
	oldV := rec.VersionString()
	newV := r.FullVersion()
	if criticalPackages[r.Name] && majorVersion(oldV) != majorVersion(newV) &&
		majorVersion(oldV) != "" && majorVersion(newV) != "" {
		out = append(out, fmt.Sprintf("major version %s -> %s", oldV, newV))
	}
	oldConflicts := map[string]bool{}
	if rec.Meta != nil {
		for _, c := range rec.Meta.Conflicts {
			if a, err := depspec.Parse(c); err == nil {
				oldConflicts[a.Name] = true
			}
		}
	}
	for _, c := range r.Conflicts {
		a, err := depspec.Parse(c)
		if err == nil && a.Name != "" && !oldConflicts[a.Name] {
			out = append(out, "new conflict: "+a.Raw)
		}
	}
	return out
}

func majorVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	i := 0
	for i < len(v) && (v[i] < '0' || v[i] > '9') {
		i++
	}
	j := i
	for j < len(v) && v[j] >= '0' && v[j] <= '9' {
		j++
	}
	return v[i:j]
}

func findOrphans(recs []*pkgdb.Rec, oldClosure, closure map[string]bool, newDeps map[string]map[string]bool) []Orphan {
	var orphans []Orphan
	for _, rec := range recs {
		k := pkgdb.SlotKey(rec.Name, rec.Slot)
		if !oldClosure[k] || closure[k] {
			continue
		}
		// Virtual names this orphan satisfies. They are what dependents
		// actually name, so they must be matched too.
		provides := map[string]bool{}
		if rec.Meta != nil {
			for _, p := range rec.Meta.Provides {
				if a, err := depspec.ParseProvided(p); err == nil && a.Name != "" {
					provides[a.Name] = true
				}
			}
		}
		o := Orphan{Name: rec.Name, Slot: rec.Slot, Version: rec.VersionString()}
		for _, other := range recs {
			if pkgdb.SlotKey(other.Name, other.Slot) == k || other.Meta == nil {
				continue
			}
			if dependsOn(other, rec.Name, provides, newDeps) {
				o.RequiredBy = append(o.RequiredBy, other.Name)
			}
		}
		sort.Strings(o.RequiredBy)
		orphans = append(orphans, o)
	}
	sort.Slice(orphans, func(i, j int) bool {
		ki := pkgdb.SlotKey(orphans[i].Name, orphans[i].Slot)
		kj := pkgdb.SlotKey(orphans[j].Name, orphans[j].Slot)
		return ki < kj
	})
	return orphans
}

// dependsOn reports whether rec requires target, either directly or through
// one of the virtual names target provides. Planned packages are evaluated
// with their new recipe dependencies.
func dependsOn(rec *pkgdb.Rec, target string, provides map[string]bool, newDeps map[string]map[string]bool) bool {
	if names, ok := newDeps[pkgdb.SlotKey(rec.Name, rec.Slot)]; ok {
		if names[target] {
			return true
		}
		for name := range provides {
			if names[name] {
				return true
			}
		}
		return false
	}
	if rec.Meta == nil {
		return false
	}
	for _, raw := range append(append([]string{}, rec.Meta.Depends...), rec.Meta.BuildDeps...) {
		a, err := depspec.Parse(raw)
		if err != nil || a.Name == "" {
			continue
		}
		if a.Name == target || provides[a.Name] {
			return true
		}
	}
	return false
}

// ReadAppliedChannel returns the channel recorded after the last successful
// upgrade ("" when none has been recorded).
func ReadAppliedChannel(db pkgdb.DB) string {
	data, err := os.ReadFile(filepath.Join(db.Dir, ".channel"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteAppliedChannel records the channel a successful upgrade applied.
func WriteAppliedChannel(db pkgdb.DB, channel string) error {
	if err := os.MkdirAll(db.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(db.Dir, ".channel.tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(channel + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(db.Dir, ".channel"))
}
