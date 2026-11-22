// Package resolve selects recipe candidates for dependency atoms.
//
// It is a deterministic backtracking resolver, not a SAT solver: it tries
// candidates in a stable order (installed version, direct recipe, providers)
// and backtracks when a version/USE constraint or a conflict rules a choice
// out. The API keeps the candidate set explicit so it can be replaced by a
// SAT backend later without changing recipe files.
package resolve

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/depspec"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
)

// Provided is one virtual name a recipe satisfies.
type Provided struct {
	Name    string
	Version string
}

// Candidate is one installable recipe.
type Candidate struct {
	Recipe      *recipe.Recipe
	Name        string
	Version     string // FullVersion
	Provides    []Provided
	Depends     []depspec.Atom
	MakeDepends []depspec.Atom
	Conflicts   []depspec.Atom
}

// Index is the recipe universe.
type Index struct {
	byName    map[string]*Candidate
	providers map[string][]*Candidate
	all       []*Candidate
}

// NewIndex loads every recipe in repos (first repository wins for a name)
// and parses its dependency/provides/conflicts metadata.
func NewIndex(repos []string) (*Index, error) {
	idx := &Index{byName: map[string]*Candidate{}, providers: map[string][]*Candidate{}}
	for _, repo := range repos {
		entries, err := os.ReadDir(repo)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if _, seen := idx.byName[name]; seen {
				continue
			}
			r, err := recipe.Load([]string{repo}, name)
			if err != nil {
				return nil, err
			}
			c, err := newCandidate(r)
			if err != nil {
				return nil, err
			}
			idx.byName[name] = c
			idx.all = append(idx.all, c)
			for _, p := range c.Provides {
				idx.providers[p.Name] = append(idx.providers[p.Name], c)
			}
		}
	}
	sort.Slice(idx.all, func(i, j int) bool { return idx.all[i].Name < idx.all[j].Name })
	return idx, nil
}

func newCandidate(r *recipe.Recipe) (*Candidate, error) {
	c := &Candidate{Recipe: r, Name: r.Name, Version: r.FullVersion()}
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

// Candidates returns the direct recipe and every provider of name.
func (idx *Index) Candidates(name string) []*Candidate {
	var out []*Candidate
	if c := idx.byName[name]; c != nil {
		out = append(out, c)
	}
	for _, c := range idx.providers[name] {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// Resolver selects a solution for targets.
type Resolver struct {
	Index     *Index
	Options   options.Options
	Installed []*pkgdb.Rec
}

// Result is a resolved install/build plan.
type Result struct {
	Order    []string // recipe names, dependencies first
	Selected map[string]*Candidate
	Targets  []string // selected package name for each input target
}

type requirement struct {
	atom     depspec.Atom
	requirer string // package declaring the dependency; "" for a target
	target   int    // index into Result.Targets, -1 for dependencies
}

type solution struct {
	selected map[string]*Candidate
	edges    map[string]map[string]bool
	targets  []string
}

func newSolution(nTargets int) *solution {
	targets := make([]string, nTargets)
	for i := range targets {
		targets[i] = ""
	}
	return &solution{
		selected: map[string]*Candidate{},
		edges:    map[string]map[string]bool{},
		targets:  targets,
	}
}

func (s *solution) clone() *solution {
	n := &solution{
		selected: make(map[string]*Candidate, len(s.selected)),
		edges:    make(map[string]map[string]bool, len(s.edges)),
		targets:  append([]string{}, s.targets...),
	}
	for k, v := range s.selected {
		n.selected[k] = v
	}
	for k, v := range s.edges {
		m := make(map[string]bool, len(v))
		for d := range v {
			m[d] = true
		}
		n.edges[k] = m
	}
	return n
}

func (s *solution) addEdge(from, to string) {
	if from == "" {
		return
	}
	if from == to {
		return
	}
	if s.edges[from] == nil {
		s.edges[from] = map[string]bool{}
	}
	s.edges[from][to] = true
}

// Resolve parses targets, chooses providers and returns a build order.
func (r *Resolver) Resolve(rawTargets []string) (*Result, error) {
	if r.Index == nil {
		return nil, fmt.Errorf("resolver has no recipe index")
	}
	targets := make([]requirement, 0, len(rawTargets))
	for i, raw := range rawTargets {
		a, err := depspec.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", raw, err)
		}
		if a.Condition != "" {
			return nil, fmt.Errorf("target %q: conditional targets are not allowed", raw)
		}
		targets = append(targets, requirement{atom: a, target: i})
	}
	st, err := r.solve(newSolution(len(rawTargets)), targets)
	if err != nil {
		return nil, err
	}
	order, err := orderSolution(st)
	if err != nil {
		return nil, err
	}
	return &Result{Order: order, Selected: st.selected, Targets: st.targets}, nil
}

func (r *Resolver) solve(st *solution, pending []requirement) (*solution, error) {
	if len(pending) == 0 {
		return st, nil
	}
	req := pending[0]
	rest := pending[1:]
	if !r.active(req) {
		return r.solve(st, rest)
	}
	if name, ok := r.selectedMatch(st, req.atom); ok {
		ns := st.clone()
		ns.addEdge(req.requirer, name)
		if req.target >= 0 {
			ns.targets[req.target] = name
		}
		return r.solve(ns, rest)
	}

	cands := r.candidates(req.atom)
	if len(cands) == 0 {
		return nil, r.noCandidateError(req)
	}
	var lastErr error
	for _, c := range cands {
		if _, used := st.selected[c.Name]; used {
			continue
		}
		if why := r.compatible(st, c); why != "" {
			lastErr = fmt.Errorf("%s: %s", c.Name, why)
			continue
		}
		ns := st.clone()
		ns.selected[c.Name] = c
		ns.addEdge(req.requirer, c.Name)
		if req.target >= 0 {
			ns.targets[req.target] = c.Name
		}
		deps := make([]requirement, 0, len(c.Depends)+len(c.MakeDepends))
		for _, a := range c.Depends {
			deps = append(deps, requirement{atom: a, requirer: c.Name, target: -1})
		}
		for _, a := range c.MakeDepends {
			deps = append(deps, requirement{atom: a, requirer: c.Name, target: -1})
		}
		next := make([]requirement, 0, len(rest)+len(deps))
		next = append(next, rest...)
		next = append(next, deps...)
		res, err := r.solve(ns, next)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all candidates are already selected or incompatible")
	}
	return nil, fmt.Errorf("%s: no usable candidate for %s: %w", reqName(req), req.atom.String(), lastErr)
}

func reqName(req requirement) string {
	if req.requirer == "" {
		return "target"
	}
	return req.requirer
}

func (r *Resolver) noCandidateError(req requirement) error {
	why := ""
	for _, c := range r.Index.Candidates(req.atom.Name) {
		if v, ok := versionFor(c, req.atom.Name); ok {
			why += fmt.Sprintf("; %s %s", c.Name, v)
		}
	}
	if why == "" {
		why = " (no recipe or provider)"
	}
	return fmt.Errorf("%s: no candidate satisfies %s%s", reqName(req), req.atom.String(), why)
}

func (r *Resolver) active(req requirement) bool {
	if req.atom.Condition == "" {
		return true
	}
	enabled := r.conditionEnabled(req.requirer, req.atom.Condition)
	return enabled != req.atom.Negated
}

// conditionEnabled evaluates a recipe dependency condition. In addition to
// USE flags it understands video_cards_<card> atoms backed by VIDEO_CARDS.
func (r *Resolver) conditionEnabled(pkg, condition string) bool {
	o := r.Options.ForPackage(pkg)
	if card, ok := strings.CutPrefix(condition, "video_cards_"); ok {
		return o.VideoCardEnabled(card)
	}
	return o.Enabled(condition)
}

func (r *Resolver) selectedMatch(st *solution, a depspec.Atom) (string, bool) {
	names := make([]string, 0, len(st.selected))
	for n := range st.selected {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if r.matches(st.selected[n], a) {
			return n, true
		}
	}
	return "", false
}

func (r *Resolver) candidates(a depspec.Atom) []*Candidate {
	var out []*Candidate
	for _, c := range r.Index.Candidates(a.Name) {
		if r.matches(c, a) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := r.isInstalled(out[i]), r.isInstalled(out[j])
		if oi != oj {
			return oi
		}
		if (out[i].Name == a.Name) != (out[j].Name == a.Name) {
			return out[i].Name == a.Name
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (r *Resolver) isInstalled(c *Candidate) bool {
	for _, rec := range r.Installed {
		if rec.Name == c.Name && rec.Slot == c.Recipe.Slot {
			return true
		}
	}
	return false
}

func (r *Resolver) matches(c *Candidate, a depspec.Atom) bool {
	v, ok := versionFor(c, a.Name)
	if !ok || !a.Matches(v) {
		return false
	}
	for _, u := range a.Use {
		if c.Recipe == nil {
			return false
		}
		if !hasFlag(c.Recipe.IUse, u.Flag) {
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

// compatible checks conflicts against the current selection and installed
// packages. It returns an empty string when the candidate can be added.
func (r *Resolver) compatible(st *solution, c *Candidate) string {
	for _, other := range st.selected {
		if other.Name == c.Name {
			continue
		}
		if reason := r.conflict(c, other); reason != "" {
			return reason
		}
	}
	for _, rec := range r.Installed {
		if rec.Name == c.Name {
			continue // replacing/upgrading the same package
		}
		ic := r.installedCandidate(rec)
		if reason := r.conflict(c, ic); reason != "" {
			return reason
		}
		if reason := r.conflict(ic, c); reason != "" {
			return reason
		}
	}
	return ""
}

func (r *Resolver) installedCandidate(rec *pkgdb.Rec) *Candidate {
	c := r.Index.byName[rec.Name]
	if c == nil {
		return &Candidate{Name: rec.Name, Version: rec.VersionString()}
	}
	ic := *c
	ic.Version = rec.VersionString()
	return &ic
}

// conflict reports a reason when a and b must not be installed together.
func (r *Resolver) conflict(a, b *Candidate) string {
	if a.Name == b.Name {
		return ""
	}
	if atom, ok := r.conflictingAtom(a, b); ok {
		return fmt.Sprintf("conflicts with %s (%s)", b.Name, atom.String())
	}
	if atom, ok := r.conflictingAtom(b, a); ok {
		return fmt.Sprintf("%s conflicts with it (%s)", b.Name, atom.String())
	}
	return ""
}

func (r *Resolver) conflictingAtom(owner, other *Candidate) (depspec.Atom, bool) {
	if owner.Recipe == nil {
		return depspec.Atom{}, false
	}
	for _, a := range owner.Conflicts {
		if a.Condition != "" {
			enabled := r.conditionEnabled(owner.Name, a.Condition)
			if enabled == a.Negated {
				continue
			}
		}
		if r.matches(other, a) {
			return a, true
		}
	}
	return depspec.Atom{}, false
}

func orderSolution(st *solution) ([]string, error) {
	names := make([]string, 0, len(st.selected))
	for n := range st.selected {
		names = append(names, n)
	}
	sort.Strings(names)
	state := map[string]int{}
	var order []string
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("dependency cycle involving %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		deps := make([]string, 0, len(st.edges[name]))
		for d := range st.edges[name] {
			if _, ok := st.selected[d]; ok {
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
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

// RecipeNames returns the sorted recipe names in the index (useful for
// diagnostics and reverse-dependency helpers).
func (idx *Index) RecipeNames() []string {
	out := make([]string, 0, len(idx.byName))
	for n := range idx.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the direct recipe candidate for name.
func (idx *Index) Lookup(name string) *Candidate { return idx.byName[name] }
