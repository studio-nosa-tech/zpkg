// Package resolve selects recipe candidates for dependency atoms.
//
// The universe is every recipe candidate for every package slot, plus
// installed-only candidates for packages whose recipe disappeared. Selection
// is encoded as a boolean satisfiability problem and solved with the in-tree
// CDCL solver in internal/sat. The encoding is complete for the constraints
// zpkg understands: version and slot atoms, virtual provides, conditional
// dependencies, USE and VIDEO_CARDS conditions, conflicts in both directions,
// installed packages, and "at most one version per slot".
//
// Result keys are package identities: "name" for slot 0 and "name@slot"
// otherwise. Slot 0 keeps the legacy spelling, so callers that only use
// default-slot packages see exactly the strings they saw before.
package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/depspec"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/pkgid"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/sat"
	"github.com/studio-nosa-tech/zpkg/internal/version"
)

// Provided is one virtual name a recipe satisfies.
type Provided struct {
	Name    string
	Version string
}

// Candidate is one installable package version in one slot.
type Candidate struct {
	Recipe      *recipe.Recipe
	Name        string
	Slot        string
	Key         string // name or name@slot
	Version     string // FullVersion
	Provides    []Provided
	Depends     []depspec.Atom
	MakeDepends []depspec.Atom
	Conflicts   []depspec.Atom
	IUse        []string

	// InstalledOnly marks a candidate synthesized from the installed database
	// because the recipe tree no longer describes this package key. It can
	// satisfy dependencies but cannot be rebuilt.
	InstalledOnly bool

	// UseFlags and VideoCards are the recorded configuration of an installed
	// record. They are used for installed-only candidates and for the record
	// views consulted by conflict checks.
	UseFlags   []string
	VideoCards []string
}

// Index is the recipe universe.
type Index struct {
	byKey     map[string][]*Candidate
	byName    map[string][]*Candidate
	providers map[string][]*Candidate
	all       []*Candidate
}

// NewIndex loads every recipe candidate in repos. The first repository that
// provides a package key wins for that key; all of its versions are kept.
func NewIndex(repos []string) (*Index, error) {
	recipes, err := recipe.ScanRepos(repos)
	if err != nil {
		return nil, err
	}
	idx := &Index{
		byKey:     map[string][]*Candidate{},
		byName:    map[string][]*Candidate{},
		providers: map[string][]*Candidate{},
	}
	for _, r := range recipes {
		c, err := newCandidate(r)
		if err != nil {
			return nil, err
		}
		idx.byKey[c.Key] = append(idx.byKey[c.Key], c)
		idx.byName[c.Name] = append(idx.byName[c.Name], c)
		idx.all = append(idx.all, c)
		for _, p := range c.Provides {
			if !containsCandidate(idx.providers[p.Name], c) {
				idx.providers[p.Name] = append(idx.providers[p.Name], c)
			}
		}
	}
	for k := range idx.byKey {
		sort.Slice(idx.byKey[k], func(i, j int) bool { return candidateLess(idx.byKey[k][i], idx.byKey[k][j]) })
	}
	for n := range idx.byName {
		sort.Slice(idx.byName[n], func(i, j int) bool { return candidateLess(idx.byName[n][i], idx.byName[n][j]) })
	}
	for n := range idx.providers {
		sort.Slice(idx.providers[n], func(i, j int) bool { return candidateLess(idx.providers[n][i], idx.providers[n][j]) })
	}
	sort.Slice(idx.all, func(i, j int) bool { return candidateLess(idx.all[i], idx.all[j]) })
	return idx, nil
}

func newCandidate(r *recipe.Recipe) (*Candidate, error) {
	c := &Candidate{
		Recipe:  r,
		Name:    r.Name,
		Slot:    r.Slot,
		Key:     r.Key(),
		Version: r.FullVersion(),
		IUse:    append([]string{}, r.IUse...),
	}
	var err error
	if c.Depends, err = parseAtoms(r, r.Depends, "depends"); err != nil {
		return nil, err
	}
	if c.MakeDepends, err = parseAtoms(r, r.MakeDepends, "makedepends"); err != nil {
		return nil, err
	}
	if c.Conflicts, err = parseAtoms(r, r.Conflicts, "conflicts"); err != nil {
		return nil, err
	}
	for _, raw := range r.Provides {
		p, err := depspec.ParseProvided(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: provides: %w", r.Name, err)
		}
		v := p.Version
		if v == "" {
			v = c.Version
		}
		c.Provides = append(c.Provides, Provided{Name: p.Name, Version: v})
	}
	return c, nil
}

func parseAtoms(r *recipe.Recipe, lines []string, field string) ([]depspec.Atom, error) {
	atoms := make([]depspec.Atom, 0, len(lines))
	for _, raw := range lines {
		a, err := depspec.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", r.Name, field, err)
		}
		if a.Condition != "" && !hasFlag(r.IUse, a.Condition) && !strings.HasPrefix(a.Condition, "video_cards_") {
			return nil, fmt.Errorf("%s: %s uses condition %q, which is not in iuse", r.Name, field, a.Condition)
		}
		atoms = append(atoms, a)
	}
	return atoms, nil
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

// Candidates returns the direct recipes and every provider of name.
func (idx *Index) Candidates(name string) []*Candidate {
	var out []*Candidate
	out = append(out, idx.byName[name]...)
	for _, c := range idx.providers[name] {
		if c.Name != name {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return candidateLess(out[i], out[j]) })
	return out
}

// Preference controls which candidate the resolver keeps when several
// versions of a slot can satisfy the constraints.
type Preference int

const (
	// PreferInstalled keeps the installed version when possible. It is the
	// default for install/depends, where the goal is to add the target
	// without churning unrelated packages.
	PreferInstalled Preference = iota
	// PreferNewest selects the newest feasible version. It is used by
	// upgrade/check, where moving forward is the point.
	PreferNewest
)

// Resolver selects a solution for targets.
type Resolver struct {
	Index     *Index
	Options   options.Options
	Installed []*pkgdb.Rec
	Prefer    Preference
}

// Result is a resolved install/build plan.
type Result struct {
	Order    []string // package keys (name or name@slot), dependencies first
	Selected map[string]*Candidate
	Targets  []string // selected package key for each input target
}

type targetRequirement struct {
	atom depspec.Atom
	raw  string
}

type universe struct {
	all       []*Candidate
	byKey     map[string][]*Candidate
	byName    map[string][]*Candidate
	providers map[string][]*Candidate
	installed map[string]*pkgdb.Rec
	recView   map[string]*Candidate // installed records, not selectable
}

func (u *universe) add(c *Candidate) {
	u.all = append(u.all, c)
	u.byKey[c.Key] = append(u.byKey[c.Key], c)
	u.byName[c.Name] = append(u.byName[c.Name], c)
	for _, p := range c.Provides {
		if !containsCandidate(u.providers[p.Name], c) {
			u.providers[p.Name] = append(u.providers[p.Name], c)
		}
	}
}

func (u *universe) sort() {
	for k := range u.byKey {
		sort.Slice(u.byKey[k], func(i, j int) bool { return candidateLess(u.byKey[k][i], u.byKey[k][j]) })
	}
	for n := range u.byName {
		sort.Slice(u.byName[n], func(i, j int) bool { return candidateLess(u.byName[n][i], u.byName[n][j]) })
	}
	for n := range u.providers {
		sort.Slice(u.providers[n], func(i, j int) bool { return candidateLess(u.providers[n][i], u.providers[n][j]) })
	}
	sort.Slice(u.all, func(i, j int) bool { return candidateLess(u.all[i], u.all[j]) })
}

func (u *universe) keys() []string {
	out := make([]string, 0, len(u.byKey))
	for k := range u.byKey {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (u *universe) installedKeys() []string {
	out := make([]string, 0, len(u.recView))
	for k := range u.recView {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Resolve parses targets, chooses candidates and returns a build order.
func (r *Resolver) Resolve(rawTargets []string) (*Result, error) {
	if r.Index == nil {
		return nil, fmt.Errorf("resolver has no recipe index")
	}
	targets := make([]targetRequirement, 0, len(rawTargets))
	for _, raw := range rawTargets {
		a, err := depspec.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", raw, err)
		}
		if a.Condition != "" {
			return nil, fmt.Errorf("target %q: conditional targets are not allowed", raw)
		}
		targets = append(targets, targetRequirement{atom: a, raw: raw})
	}

	u := r.newUniverse()
	s := sat.New()
	varOf := make(map[*Candidate]sat.Lit, len(u.all))
	for _, c := range u.all {
		v := s.NewVar()
		varOf[c] = sat.Pos(v)
	}
	r.assignActivities(s, u, varOf)

	// One activation literal per target lets the solver report a minimal set
	// of targets that cannot be satisfied together.
	targetActs := make([]sat.Lit, len(targets))
	for i, t := range targets {
		matches := u.matching(r, t.atom)
		if len(matches) == 0 {
			return nil, r.noTargetError(t, u)
		}
		act := sat.Pos(s.NewVar())
		targetActs[i] = act
		clause := make([]sat.Lit, 0, len(matches)+1)
		clause = append(clause, act.Not())
		for _, c := range matches {
			clause = append(clause, varOf[c])
		}
		s.AddRequirement(clause, "target "+t.raw)
	}

	r.addDependencyClauses(s, u, varOf)
	r.addSlotClauses(s, u, varOf)
	r.addConflictClauses(s, u, varOf)

	assumptions := append([]sat.Lit{}, targetActs...)
	if !s.Solve(assumptions) {
		core := minimizeCore(s, assumptions)
		return nil, r.unsatError(s, core, targets, targetActs, u)
	}
	selected := r.modelSelected(s.Model(), varOf)

	order, selected, assumptions, err := r.repairCycles(s, u, varOf, selected, assumptions, targetActs)
	if err != nil {
		return nil, err
	}

	selected, assumptions = r.optimize(s, u, varOf, selected, assumptions, targets)
	order, selected, _, err = r.repairCycles(s, u, varOf, selected, assumptions, targetActs)
	if err != nil {
		return nil, err
	}

	result := &Result{Order: order, Selected: selected, Targets: make([]string, len(targets))}
	for i, t := range targets {
		k := r.targetKey(selected, t.atom)
		if k == "" {
			return nil, fmt.Errorf("internal error: target %q has no selected candidate", t.raw)
		}
		result.Targets[i] = k
	}
	return result, nil
}

// newUniverse combines recipe candidates with installed records. A package
// key that has no recipe candidate gets a selectable installed-only
// candidate, so a dependency can still be satisfied after its recipe
// disappears.
func (r *Resolver) newUniverse() *universe {
	u := &universe{
		byKey:     map[string][]*Candidate{},
		byName:    map[string][]*Candidate{},
		providers: map[string][]*Candidate{},
		installed: map[string]*pkgdb.Rec{},
		recView:   map[string]*Candidate{},
	}
	for _, c := range r.Index.all {
		u.add(c)
	}
	for _, rec := range r.Installed {
		k := pkgid.Key(rec.Name, rec.Slot)
		u.installed[k] = rec
		u.recView[k] = r.recordCandidate(rec, r.Index.byKey[k])
		if len(r.Index.byKey[k]) == 0 {
			u.add(r.installedCandidate(rec))
		}
	}
	u.sort()
	return u
}

func (r *Resolver) recordCandidate(rec *pkgdb.Rec, current []*Candidate) *Candidate {
	c := &Candidate{
		Name:    rec.Name,
		Slot:    rec.Slot,
		Key:     pkgid.Key(rec.Name, rec.Slot),
		Version: rec.VersionString(),
	}
	if rec.Meta != nil {
		c.Depends = parseAtomsLenient(rec.Meta.Depends)
		c.MakeDepends = parseAtomsLenient(rec.Meta.BuildDeps)
		c.Conflicts = parseAtomsLenient(rec.Meta.Conflicts)
		c.Provides = providedFromMeta(rec.Meta.Provides, c.Version)
		c.UseFlags = append([]string{}, rec.Meta.UseFlags...)
		c.VideoCards = append([]string{}, rec.Meta.VideoCards...)
		return c
	}
	// v1 records have no metadata. Fall back to the current recipe and the
	// current configuration, matching the pre-SAT resolver's behavior.
	if len(current) > 0 {
		base := current[0]
		c.Depends = append([]depspec.Atom{}, base.Depends...)
		c.MakeDepends = append([]depspec.Atom{}, base.MakeDepends...)
		c.Conflicts = append([]depspec.Atom{}, base.Conflicts...)
		c.Provides = append([]Provided{}, base.Provides...)
	}
	o := r.Options.ForPackage(rec.Name)
	c.UseFlags = append([]string{}, o.Use...)
	c.VideoCards = append([]string{}, o.VideoCards...)
	return c
}

func (r *Resolver) installedCandidate(rec *pkgdb.Rec) *Candidate {
	c := r.recordCandidate(rec, r.Index.byKey[pkgid.Key(rec.Name, rec.Slot)])
	c.InstalledOnly = true
	return c
}

func providedFromMeta(raws []string, defaultVersion string) []Provided {
	var out []Provided
	for _, raw := range raws {
		p, err := depspec.ParseProvided(raw)
		if err != nil {
			continue
		}
		v := p.Version
		if v == "" {
			v = defaultVersion
		}
		out = append(out, Provided{Name: p.Name, Version: v})
	}
	return out
}

func parseAtomsLenient(raws []string) []depspec.Atom {
	var out []depspec.Atom
	for _, raw := range raws {
		a, err := depspec.Parse(raw)
		if err == nil {
			out = append(out, a)
		}
	}
	return out
}

func (r *Resolver) assignActivities(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit) {
	for _, k := range u.keys() {
		ordered := r.orderedCandidates(u, k)
		for rank, c := range ordered {
			act := float64(1000 - rank)
			if r.Prefer == PreferInstalled {
				if rec := u.installed[k]; rec != nil && c.Version == rec.VersionString() {
					act += 10000
				}
			}
			if c.Slot == "0" {
				act += 100
			}
			s.SetActivity(varOf[c].Var(), act)
		}
	}
}

func (r *Resolver) addDependencyClauses(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit) {
	for _, c := range u.all {
		for _, a := range allDeps(c) {
			if !r.active(c, a) {
				continue
			}
			matches := u.matching(r, a)
			clause := make([]sat.Lit, 0, len(matches)+1)
			clause = append(clause, varOf[c].Not())
			for _, d := range matches {
				clause = append(clause, varOf[d])
			}
			if len(matches) == 0 {
				s.AddClause(clause, fmt.Sprintf("%s: no candidate satisfies %s", c.Key, a.String()))
				continue
			}
			s.AddRequirement(clause, fmt.Sprintf("%s depends on %s", c.Key, a.String()))
		}
	}
}

func (r *Resolver) addSlotClauses(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit) {
	for _, k := range u.keys() {
		cs := u.byKey[k]
		for i := 0; i < len(cs); i++ {
			for j := i + 1; j < len(cs); j++ {
				s.AddClause([]sat.Lit{varOf[cs[i]].Not(), varOf[cs[j]].Not()},
					fmt.Sprintf("slot %s can hold only one version", k))
			}
		}
	}
}

func (r *Resolver) addConflictClauses(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit) {
	pairSeen := map[string]bool{}
	replaceSeen := map[string]bool{}

	addPair := func(a, b *Candidate, reason string) {
		if a.Key == b.Key {
			return
		}
		key := a.Key + "\x00" + b.Key
		if a.Key > b.Key {
			key = b.Key + "\x00" + a.Key
		}
		if pairSeen[key] {
			return
		}
		pairSeen[key] = true
		s.AddClause([]sat.Lit{varOf[a].Not(), varOf[b].Not()}, reason)
	}

	for _, c := range u.all {
		for _, a := range c.Conflicts {
			if !r.active(c, a) {
				continue
			}
			for _, d := range u.matching(r, a) {
				if d.Key == c.Key {
					continue // another version of the same slot replaces c
				}
				addPair(c, d, fmt.Sprintf("%s conflicts with %s (%s)", c.Key, d.Key, a.String()))
			}
			for _, ik := range u.installedKeys() {
				if ik == c.Key {
					continue
				}
				view := u.recView[ik]
				if view == nil || !r.matches(view, a) {
					continue
				}
				r.addReplaceClause(s, u, varOf, c, ik, a, replaceSeen)
			}
		}
	}

	// An installed record can also declare a conflict with the new candidate.
	for _, ik := range u.installedKeys() {
		view := u.recView[ik]
		if view == nil {
			continue
		}
		for _, a := range view.Conflicts {
			if !r.active(view, a) {
				continue
			}
			for _, c := range u.all {
				if c.Key == ik || !r.matches(c, a) {
					continue
				}
				r.addReplaceClause(s, u, varOf, c, ik, a, replaceSeen)
			}
		}
	}
}

func (r *Resolver) addReplaceClause(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit,
	c *Candidate, installedKey string, a depspec.Atom, seen map[string]bool) {
	key := c.Key + "\x00" + installedKey + "\x00" + a.String()
	if seen[key] {
		return
	}
	seen[key] = true
	rec := u.installed[installedKey]
	var repl []*Candidate
	for _, d := range u.byKey[installedKey] {
		if r.matches(d, a) {
			continue // this candidate does not remove the conflict
		}
		if rec == nil || !replaceable(d, rec, r.Options) {
			continue // selecting it would not actually replace the record
		}
		repl = append(repl, d)
	}
	if len(repl) == 0 {
		s.AddClause([]sat.Lit{varOf[c].Not()},
			fmt.Sprintf("%s conflicts with installed %s and it cannot be replaced (%s)", c.Key, installedKey, a.String()))
		return
	}
	clause := make([]sat.Lit, 0, len(repl)+1)
	clause = append(clause, varOf[c].Not())
	for _, d := range repl {
		clause = append(clause, varOf[d])
	}
	s.AddRequirement(clause, fmt.Sprintf("%s conflicts with installed %s; replace it (%s)", c.Key, installedKey, a.String()))
}

func (r *Resolver) active(c *Candidate, a depspec.Atom) bool {
	if a.Condition == "" {
		return true
	}
	enabled := r.conditionEnabled(c, a.Condition)
	return enabled != a.Negated
}

func (r *Resolver) conditionEnabled(c *Candidate, condition string) bool {
	if c.Recipe == nil {
		if card, ok := strings.CutPrefix(condition, "video_cards_"); ok {
			return hasFlag(c.VideoCards, card)
		}
		return hasFlag(c.UseFlags, condition)
	}
	o := r.Options.ForPackage(c.Name)
	if card, ok := strings.CutPrefix(condition, "video_cards_"); ok {
		return o.VideoCardEnabled(card)
	}
	return o.Enabled(condition)
}

// matching returns every selectable candidate satisfying a, sorted
// deterministically.
func (u *universe) matching(r *Resolver, a depspec.Atom) []*Candidate {
	var out []*Candidate
	for _, c := range u.byName[a.Name] {
		if r.matches(c, a) {
			out = append(out, c)
		}
	}
	if a.Slot == "" {
		for _, c := range u.providers[a.Name] {
			if c.Name != a.Name && r.matches(c, a) {
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return candidateLess(out[i], out[j]) })
	return out
}

func (r *Resolver) matches(c *Candidate, a depspec.Atom) bool {
	if a.Slot != "" {
		if c.Name != a.Name || c.Slot != a.Slot {
			return false
		}
	}
	v, ok := versionFor(c, a.Name)
	if !ok || !a.Matches(v) {
		return false
	}
	for _, u := range a.Use {
		if c.Recipe == nil {
			enabled := hasFlag(c.UseFlags, u.Flag)
			if !enabled && u.Enabled {
				if u.Optional {
					continue
				}
				return false
			}
			if enabled && !u.Enabled {
				return false
			}
			continue
		}
		if !hasFlag(c.IUse, u.Flag) {
			if u.Optional {
				continue
			}
			return false
		}
		enabled := r.Options.ForPackage(c.Name).Enabled(u.Flag)
		if enabled != u.Enabled {
			return false
		}
	}
	return true
}

func versionFor(c *Candidate, name string) (string, bool) {
	if c.Name == name {
		return c.Version, true
	}
	for _, p := range c.Provides {
		if p.Name == name {
			return p.Version, true
		}
	}
	return "", false
}

func (r *Resolver) orderedCandidates(u *universe, k string) []*Candidate {
	cs := append([]*Candidate{}, u.byKey[k]...)
	rec := u.installed[k]
	sort.SliceStable(cs, func(i, j int) bool {
		if r.Prefer == PreferInstalled && rec != nil {
			ii := cs[i].Version == rec.VersionString()
			jj := cs[j].Version == rec.VersionString()
			if ii != jj {
				return ii
			}
		}
		if c := version.Compare(cs[i].Version, cs[j].Version); c != 0 {
			return c > 0
		}
		di, dj := "", ""
		if cs[i].Recipe != nil {
			di = cs[i].Recipe.Dir
		}
		if cs[j].Recipe != nil {
			dj = cs[j].Recipe.Dir
		}
		return di < dj
	})
	return cs
}

func (r *Resolver) modelSelected(model []bool, varOf map[*Candidate]sat.Lit) map[string]*Candidate {
	out := make(map[string]*Candidate, len(varOf))
	for c, lit := range varOf {
		if lit.Var() < len(model) && model[lit.Var()] {
			out[c.Key] = c
		}
	}
	return out
}

// optimize applies soft preferences lexicographically. Only slots with more
// than one candidate need an extra solve, which keeps the common
// one-version-per-name tree at a single solve.
func (r *Resolver) optimize(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit,
	selected map[string]*Candidate, assumptions []sat.Lit, targets []targetRequirement) (map[string]*Candidate, []sat.Lit) {
	var order []string
	seen := map[string]bool{}
	for _, t := range targets {
		k := r.targetKey(selected, t.atom)
		if k != "" && !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	for _, k := range u.keys() {
		if !seen[k] && len(u.byKey[k]) > 1 {
			order = append(order, k)
		}
	}

	rounds := 0
	for _, k := range order {
		ordered := r.orderedCandidates(u, k)
		if len(ordered) < 2 {
			continue
		}
		pref := ordered[0]
		if sel := selected[k]; sel == nil || sel == pref {
			continue
		}
		if rounds >= 500 {
			break
		}
		g := s.NewVar()
		guard := sat.Pos(g)
		ok := true
		for _, c := range ordered[1:] {
			if !s.AddClause([]sat.Lit{guard.Not(), varOf[c].Not()},
				fmt.Sprintf("prefer %s over %s", pref.Key, c.Key)) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		trial := append(append([]sat.Lit{}, assumptions...), guard)
		if s.Solve(trial) {
			assumptions = trial
			selected = r.modelSelected(s.Model(), varOf)
			rounds++
		} else {
			s.AddClause([]sat.Lit{guard.Not()}, "disable preference for "+k)
		}
	}
	return selected, assumptions
}

// repairCycles makes the selected graph acyclic. A cycle is not expressible
// in CNF; instead the exact set of candidates forming the cycle is blocked
// and the solver is asked for another solution.
func (r *Resolver) repairCycles(s *sat.Solver, u *universe, varOf map[*Candidate]sat.Lit,
	selected map[string]*Candidate, assumptions, targetAssumptions []sat.Lit) ([]string, map[string]*Candidate, []sat.Lit, error) {
	for attempt := 0; attempt < 20; attempt++ {
		order, cycle, err := r.orderSolution(u, selected)
		if err == nil {
			return order, selected, assumptions, nil
		}
		if len(cycle) == 0 {
			return nil, nil, assumptions, err
		}
		var clause []sat.Lit
		for _, k := range cycle {
			if c := selected[k]; c != nil {
				clause = append(clause, varOf[c].Not())
			}
		}
		if len(clause) == 0 {
			return nil, nil, assumptions, err
		}
		s.AddClause(clause, fmt.Sprintf("dependency cycle involving %s", strings.Join(cycle, ", ")))
		if !s.Solve(assumptions) {
			// A soft preference guard may be what prevents an acyclic
			// solution. Retry with only the hard target assumptions.
			if !s.Solve(targetAssumptions) {
				return nil, nil, assumptions, fmt.Errorf("dependency cycle involving %q has no alternative solution", cycle[0])
			}
			assumptions = append([]sat.Lit{}, targetAssumptions...)
		}
		selected = r.modelSelected(s.Model(), varOf)
	}
	return nil, nil, assumptions, fmt.Errorf("dependency cycle could not be resolved")
}

func (r *Resolver) targetKey(selected map[string]*Candidate, a depspec.Atom) string {
	var matches []*Candidate
	for _, c := range selected {
		if r.matches(c, a) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		di := matches[i].Name == a.Name && (a.Slot == "" || matches[i].Slot == a.Slot)
		dj := matches[j].Name == a.Name && (a.Slot == "" || matches[j].Slot == a.Slot)
		if di != dj {
			return di
		}
		return matches[i].Key < matches[j].Key
	})
	return matches[0].Key
}

// orderSolution returns a dependency-first order and, on failure, the keys
// participating in the cycle.
func (r *Resolver) orderSolution(u *universe, selected map[string]*Candidate) ([]string, []string, error) {
	keys := make([]string, 0, len(selected))
	for k := range selected {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	edges := r.dependencyEdges(u, selected)
	state := map[string]int{}
	var stack []string
	var order []string
	var cycle []string
	var visit func(string) bool
	visit = func(k string) bool {
		switch state[k] {
		case 1:
			for i, s := range stack {
				if s == k {
					cycle = append([]string{}, stack[i:]...)
					break
				}
			}
			if len(cycle) == 0 {
				cycle = []string{k}
			}
			return false
		case 2:
			return true
		}
		state[k] = 1
		stack = append(stack, k)
		deps := make([]string, 0, len(edges[k]))
		for d := range edges[k] {
			if _, ok := selected[d]; ok {
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			if !visit(d) {
				return false
			}
		}
		stack = stack[:len(stack)-1]
		state[k] = 2
		order = append(order, k)
		return true
	}
	for _, k := range keys {
		if !visit(k) {
			return nil, cycle, fmt.Errorf("dependency cycle involving %q", cycle[0])
		}
	}
	return order, nil, nil
}

// dependencyEdges picks one selected satisfier per active dependency and
// records conflict replacements as ordering edges. It is keyed by candidate
// keys and deterministic (the lexicographically smallest satisfier wins, as
// in the previous resolver).
func (r *Resolver) dependencyEdges(u *universe, selected map[string]*Candidate) map[string]map[string]bool {
	edges := map[string]map[string]bool{}
	addEdge := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		if edges[from] == nil {
			edges[from] = map[string]bool{}
		}
		edges[from][to] = true
	}
	keys := make([]string, 0, len(selected))
	for k := range selected {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := selected[k]
		for _, a := range allDeps(c) {
			if !r.active(c, a) {
				continue
			}
			var best string
			for _, sk := range keys {
				if sk == k {
					continue
				}
				if r.matches(selected[sk], a) {
					if best == "" || sk < best {
						best = sk
					}
				}
			}
			addEdge(k, best)
		}
		// A package that conflicts with an installed version but is
		// installed together with its replacement must be ordered after the
		// replacement, otherwise the old version is still on disk.
		for _, a := range c.Conflicts {
			if !r.active(c, a) {
				continue
			}
			for _, dk := range keys {
				rec := u.installed[dk]
				if dk == k || rec == nil {
					continue
				}
				view := u.recView[dk]
				if view == nil || !r.matches(view, a) {
					continue
				}
				if r.matches(selected[dk], a) {
					continue
				}
				addEdge(k, dk)
			}
		}
	}
	// The installed record may declare the conflict instead of the new
	// candidate.
	for _, dk := range keys {
		if u.installed[dk] == nil {
			continue
		}
		view := u.recView[dk]
		if view == nil {
			continue
		}
		for _, a := range view.Conflicts {
			if !r.active(view, a) {
				continue
			}
			for _, k := range keys {
				if k == dk || !r.matches(selected[k], a) {
					continue
				}
				if !r.matches(selected[dk], a) {
					addEdge(k, dk)
				}
			}
		}
	}
	return edges
}

func minimizeCore(s *sat.Solver, assumptions []sat.Lit) []sat.Lit {
	core := s.UnsatCore()
	if len(core) == 0 {
		core = append([]sat.Lit{}, assumptions...)
	}
	if len(core) > 200 {
		return core
	}
	for i := 0; i < len(core); {
		trial := append(append([]sat.Lit{}, core[:i]...), core[i+1:]...)
		if s.Solve(trial) {
			i++
			continue
		}
		core = trial
	}
	if s.Solve(core) {
		// A sound core is always UNSAT; fall back to every target rather than
		// reporting a misleading minimal set if the solver disagrees.
		return append([]sat.Lit{}, assumptions...)
	}
	return core
}

func (r *Resolver) unsatError(s *sat.Solver, core []sat.Lit, targets []targetRequirement,
	targetActs []sat.Lit, u *universe) error {
	coreSet := map[sat.Lit]bool{}
	for _, l := range core {
		coreSet[l] = true
	}
	var names []string
	for i, t := range targets {
		if coreSet[targetActs[i]] {
			names = append(names, t.raw)
		}
	}
	if len(names) == 0 {
		for _, t := range targets {
			names = append(names, t.raw)
		}
	}

	var lines []string
	seenLine := map[string]bool{}
	add := func(line string) {
		if line != "" && !seenLine[line] && len(lines) < 8 {
			seenLine[line] = true
			lines = append(lines, line)
		}
	}
	// Explain only candidates reachable from the failing targets. Scanning
	// the whole universe would surface unrelated broken recipes.
	rel := map[string]*Candidate{}
	var frontier []*Candidate
	for i, t := range targets {
		if !coreSet[targetActs[i]] {
			continue
		}
		for _, c := range u.matching(r, t.atom) {
			if rel[c.Key] == nil {
				rel[c.Key] = c
				frontier = append(frontier, c)
			}
		}
	}
	for depth := 0; depth < 3 && len(rel) < 500; depth++ {
		var next []*Candidate
		for _, c := range frontier {
			for _, a := range allDeps(c) {
				if !r.active(c, a) {
					continue
				}
				for _, d := range u.matching(r, a) {
					if rel[d.Key] == nil && len(rel) < 500 {
						rel[d.Key] = d
						next = append(next, d)
					}
				}
			}
		}
		frontier = next
	}
	relKeys := make([]string, 0, len(rel))
	for k := range rel {
		relKeys = append(relKeys, k)
	}
	sort.Strings(relKeys)
	for _, k := range relKeys {
		c := rel[k]
		for _, a := range allDeps(c) {
			if !r.active(c, a) {
				continue
			}
			if len(u.matching(r, a)) == 0 {
				add(fmt.Sprintf("%s: no candidate satisfies %s", c.Key, a.String()))
			}
		}
	}
	for _, k := range relKeys {
		c := rel[k]
		for _, a := range c.Conflicts {
			if !r.active(c, a) {
				continue
			}
			for _, d := range u.matching(r, a) {
				if d.Key == c.Key {
					continue
				}
				if rel[d.Key] != nil || u.installed[d.Key] != nil {
					add(fmt.Sprintf("%s conflicts with %s (%s)", c.Key, d.Key, a.String()))
				}
			}
		}
	}
	for _, line := range s.Explanation() {
		add(line)
	}
	if len(lines) == 0 {
		lines = append(lines, "the selected targets cannot be installed together")
	}
	return fmt.Errorf("dependency resolution failed for %s:\n  %s",
		strings.Join(names, ", "), strings.Join(lines, "\n  "))
}

func (r *Resolver) noTargetError(t targetRequirement, u *universe) error {
	var why string
	for _, c := range u.byName[t.atom.Name] {
		if t.atom.Slot != "" && c.Slot != t.atom.Slot {
			continue
		}
		if v, ok := versionFor(c, t.atom.Name); ok && t.atom.Matches(v) {
			why += fmt.Sprintf("; %s %s", c.Key, v)
		}
	}
	if why == "" {
		why = " (no recipe or provider)"
	}
	return fmt.Errorf("target %q: no candidate satisfies %s%s", t.raw, t.atom.String(), why)
}

func allDeps(c *Candidate) []depspec.Atom {
	out := make([]depspec.Atom, 0, len(c.Depends)+len(c.MakeDepends))
	out = append(out, c.Depends...)
	out = append(out, c.MakeDepends...)
	return out
}

func containsCandidate(cs []*Candidate, c *Candidate) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

func candidateLess(a, b *Candidate) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	if c := version.Compare(a.Version, b.Version); c != 0 {
		return c > 0
	}
	da, db := "", ""
	if a.Recipe != nil {
		da = a.Recipe.Dir
	}
	if b.Recipe != nil {
		db = b.Recipe.Dir
	}
	return da < db
}

// replaceable reports whether selecting c would actually replace the
// installed record. A same-version candidate with identical metadata is
// skipped by install, so it cannot remove a conflict.
func replaceable(c *Candidate, rec *pkgdb.Rec, opts options.Options) bool {
	if c.Version != rec.VersionString() {
		return true
	}
	return MetadataChanged(c, rec, opts)
}

// MetadataChanged reports whether installing c over rec would change the
// dependency/provides/conflicts metadata or the effective build options.
// install uses this to decide whether an already-installed same version must
// be rebuilt (for example to pick up a changed conflicts list).
func MetadataChanged(c *Candidate, rec *pkgdb.Rec, opts options.Options) bool {
	if c.Recipe == nil {
		return false
	}
	if rec.Meta == nil {
		return true
	}
	if !sameStrings(c.Recipe.Depends, rec.Meta.Depends) ||
		!sameStrings(c.Recipe.MakeDepends, rec.Meta.BuildDeps) ||
		!sameStrings(c.Recipe.Provides, rec.Meta.Provides) ||
		!sameStrings(c.Recipe.Conflicts, rec.Meta.Conflicts) {
		return true
	}
	if !sameStrings(opts.ForPackage(c.Name).Use, rec.Meta.UseFlags) {
		return true
	}
	if c.Recipe.UsesVideoCards() && !sameStrings(opts.VideoCards, rec.Meta.VideoCards) {
		return true
	}
	return false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string{}, a...)
	bc := append([]string{}, b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// ParseTargets is a helper for callers that only need normalized names.
func ParseTargets(raw []string) ([]depspec.Atom, error) {
	out := make([]depspec.Atom, 0, len(raw))
	for _, s := range raw {
		a, err := depspec.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// RecipeNames returns the sorted unique recipe names in the index.
func (idx *Index) RecipeNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(idx.byName))
	for n := range idx.byName {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Lookup returns the preferred direct recipe candidate for name: slot 0
// first, then the highest version.
func (idx *Index) Lookup(name string) *Candidate {
	cs := idx.byName[name]
	if len(cs) == 0 {
		return nil
	}
	best := cs[0]
	bestSlot0 := best.Slot == "0"
	for _, c := range cs[1:] {
		c0 := c.Slot == "0"
		if c0 != bestSlot0 {
			if c0 {
				best, bestSlot0 = c, true
			}
			continue
		}
		if cmp := version.Compare(c.Version, best.Version); cmp > 0 {
			best = c
		}
	}
	return best
}
