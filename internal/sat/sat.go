// Package sat implements a small deterministic CDCL SAT solver.
//
// It is used by the dependency resolver to encode package selection as a
// boolean satisfiability problem. The solver has no external dependencies and
// supports assumptions plus unsat-core extraction, which the resolver uses to
// report which targets cannot be satisfied together.
package sat

import (
	"fmt"
	"math"
)

// Lit is a signed boolean literal: variable v is encoded as 2*v (positive)
// or 2*v+1 (negative).
type Lit int32

// Pos returns the positive literal of v.
func Pos(v int) Lit { return Lit(v << 1) }

// Neg returns the negative literal of v.
func Neg(v int) Lit { return Lit(v<<1 | 1) }

// Var returns the variable index.
func (l Lit) Var() int { return int(l) >> 1 }

// Negated reports whether l is a negative literal.
func (l Lit) Negated() bool { return l&1 == 1 }

// Not returns the complementary literal.
func (l Lit) Not() Lit { return l ^ 1 }

func (l Lit) String() string {
	if l.Negated() {
		return fmt.Sprintf("-%d", l.Var())
	}
	return fmt.Sprintf("%d", l.Var())
}

type clause struct {
	lits        []Lit
	reason      string
	requirement bool // target/dependency clause: satisfying a literal is preferred
	learnt      bool
}

// Solver is a CDCL SAT solver with watched literals and 1UIP learning.
type Solver struct {
	nvars   int
	clauses []clause
	watches [][]int // literal -> clause indexes watching it

	assigns  []int8 // 1 true, -1 false, 0 unassigned
	level    []int
	reason   []int32 // clause index, -1 decision/unassigned
	trail    []Lit
	trailLim []int
	qhead    int

	activity []float64
	varInc   float64
	varDecay float64

	ok           bool
	model        []bool
	lastConflict int
	explanation  []string
	core         []Lit

	conflicts       int
	decisions       int
	propagations    int
	restarts        int
	nextRestart     int
	assumptionCount int
}

// New returns an empty solver.
func New() *Solver {
	return &Solver{
		ok:           true,
		varInc:       1,
		varDecay:     0.95,
		nextRestart:  100,
		lastConflict: -1,
	}
}

// NewVar creates a variable and returns its index.
func (s *Solver) NewVar() int {
	v := s.nvars
	s.nvars++
	s.assigns = append(s.assigns, 0)
	s.level = append(s.level, 0)
	s.reason = append(s.reason, -1)
	s.activity = append(s.activity, 0)
	s.watches = append(s.watches, nil, nil)
	return v
}

// SetActivity sets the initial activity of v. Higher activity makes v more
// attractive in requirement clauses and less likely to be falsified in
// conflict/at-most-one clauses.
func (s *Solver) SetActivity(v int, a float64) {
	if v >= 0 && v < s.nvars {
		s.activity[v] = a
	}
}

// AddClause adds a hard clause with a human-readable reason used in conflict
// explanations. It must be called before Solve (or after a Solve call has
// returned; the solver is then at decision level 0). It returns false when
// the formula is already unsatisfiable.
func (s *Solver) AddClause(lits []Lit, reason string) bool {
	return s.addClause(lits, reason, false)
}

// AddRequirement adds a clause that represents a positive requirement: a
// target or a dependency. When such a clause is open, the solver prefers to
// satisfy it by selecting the highest-activity candidate.
func (s *Solver) AddRequirement(lits []Lit, reason string) bool {
	return s.addClause(lits, reason, true)
}

func (s *Solver) addClause(lits []Lit, reason string, requirement bool) bool {
	if len(s.trailLim) != 0 {
		// Clauses are only added at decision level 0. Callers construct the
		// formula up front and guarded optimization clauses between solves.
		return false
	}
	var tautology bool
	lits, tautology = normalize(lits)
	if tautology {
		return true
	}
	if len(lits) == 0 {
		s.ok = false
		return false
	}
	var unassigned []Lit
	satisfied := false
	for _, l := range lits {
		switch s.valueLit(l) {
		case 1:
			satisfied = true
		case 0:
			unassigned = append(unassigned, l)
		}
	}
	if satisfied {
		// Permanently satisfied by a level-0 assignment.
		return true
	}
	if len(unassigned) == 0 {
		s.ok = false
		return false
	}
	if len(unassigned) == 1 {
		ci := len(s.clauses)
		s.clauses = append(s.clauses, clause{lits: append([]Lit{}, lits...), reason: reason, requirement: requirement})
		if !s.enqueue(unassigned[0], int32(ci)) {
			s.ok = false
			return false
		}
		return true
	}
	// Watch two literals that are still unassigned. Level-0 false literals
	// must not be watched: their assignment predates the clause and would not
	// trigger propagation.
	ordered := make([]Lit, 0, len(lits))
	ordered = append(ordered, unassigned...)
	for _, l := range lits {
		if s.valueLit(l) == -1 {
			ordered = append(ordered, l)
		}
	}
	ci := len(s.clauses)
	s.clauses = append(s.clauses, clause{lits: ordered, reason: reason, requirement: requirement})
	// watches[x] holds clauses watching literal !x, so an assignment that
	// makes x true processes exactly the clauses whose watched literal x
	// became false.
	s.watches[ordered[0].Not()] = append(s.watches[ordered[0].Not()], ci)
	s.watches[ordered[1].Not()] = append(s.watches[ordered[1].Not()], ci)
	return true
}

// normalize removes duplicate literals and reports tautologies. A tautology
// needs no clause because it is always satisfied.
func normalize(lits []Lit) ([]Lit, bool) {
	if len(lits) < 2 {
		return append([]Lit{}, lits...), false
	}
	seen := make(map[Lit]bool, len(lits))
	out := make([]Lit, 0, len(lits))
	for _, l := range lits {
		if seen[l] {
			continue
		}
		if seen[l.Not()] {
			return nil, true
		}
		seen[l] = true
		out = append(out, l)
	}
	return out, false
}

// Solve searches for a model under assumptions. It returns true and stores
// the model when the formula is satisfiable. When false, UnsatCore reports a
// subset of the assumptions that is already inconsistent.
func (s *Solver) Solve(assumptions []Lit) bool {
	s.model = nil
	s.explanation = nil
	s.core = nil
	if !s.ok {
		return false
	}
	s.cancelUntil(0)
	s.qhead = 0
	if ci := s.propagate(); ci >= 0 {
		s.ok = false
		s.captureExplanation(ci)
		s.cancelUntil(0)
		return false
	}
	s.conflicts = 0
	s.restarts = 0
	s.nextRestart = 100
	s.assumptionCount = len(assumptions)

	for {
		ci := s.propagate()
		if ci >= 0 {
			if s.decisionLevel() == 0 {
				s.ok = false
				s.captureExplanation(ci)
				s.core = append([]Lit{}, assumptions...)
				s.cancelUntil(0)
				return false
			}
			s.conflicts++
			s.varInc /= s.varDecay
			learnt, bt := s.analyze(ci)
			if len(learnt) == 0 {
				s.ok = false
				s.captureExplanation(ci)
				s.core = append([]Lit{}, assumptions...)
				s.cancelUntil(0)
				return false
			}
			s.cancelUntil(bt)
			ci2 := s.addLearnt(learnt)
			if !s.enqueue(learnt[0], int32(ci2)) {
				s.ok = false
				s.core = append([]Lit{}, assumptions...)
				s.cancelUntil(0)
				return false
			}
			continue
		}

		// Apply assumptions as the first decision levels. A dummy level is
		// created for an assumption already implied at a lower level so the
		// level index stays aligned with the assumption position.
		if s.decisionLevel() < len(assumptions) {
			p := assumptions[s.decisionLevel()]
			switch s.valueLit(p) {
			case 1:
				s.newDecisionLevel()
			case -1:
				s.core = append([]Lit{}, assumptions[:s.decisionLevel()+1]...)
				s.captureExplanation(-1)
				s.cancelUntil(0)
				return false
			default:
				s.newDecisionLevel()
				s.enqueue(p, -1)
			}
			continue
		}

		if s.conflicts >= s.nextRestart {
			s.restarts++
			s.nextRestart = s.conflicts + int(100*math.Pow(1.5, float64(s.restarts)))
			if s.nextRestart > 100000 {
				s.nextRestart = s.conflicts + 100000
			}
			s.cancelUntil(0)
			continue
		}

		lit := s.pickBranchLit()
		if !s.ok {
			s.captureExplanation(s.lastConflict)
			s.core = append([]Lit{}, assumptions...)
			s.cancelUntil(0)
			return false
		}
		if lit < 0 {
			s.model = make([]bool, s.nvars)
			for i := range s.model {
				s.model[i] = s.assigns[i] == 1
			}
			s.cancelUntil(0)
			return true
		}
		s.decisions++
		s.newDecisionLevel()
		s.enqueue(lit, -1)
	}
}

// Model returns the last satisfying assignment. Indexes not assigned in the
// model are false.
func (s *Solver) Model() []bool { return s.model }

// UnsatCore returns the assumptions that were found inconsistent by the last
// Solve call. It may be a superset of a minimal core; callers can minimize it
// by re-solving with subsets.
func (s *Solver) UnsatCore() []Lit { return append([]Lit{}, s.core...) }

// Explanation returns short human-readable reasons collected from the final
// conflict's implication graph.
func (s *Solver) Explanation() []string { return append([]string{}, s.explanation...) }

// Stats returns solver counters for tests and diagnostics.
func (s *Solver) Stats() (conflicts, decisions, propagations int) {
	return s.conflicts, s.decisions, s.propagations
}

func (s *Solver) valueLit(l Lit) int {
	v := s.assigns[l.Var()]
	if v == 0 {
		return 0
	}
	if l.Negated() {
		return int(-v)
	}
	return int(v)
}

func (s *Solver) decisionLevel() int { return len(s.trailLim) }

func (s *Solver) newDecisionLevel() {
	s.trailLim = append(s.trailLim, len(s.trail))
}

func (s *Solver) enqueue(l Lit, reason int32) bool {
	v := l.Var()
	want := int8(1)
	if l.Negated() {
		want = -1
	}
	if s.assigns[v] != 0 {
		if s.assigns[v] == want {
			return true
		}
		s.lastConflict = int(reason)
		return false
	}
	s.assigns[v] = want
	s.level[v] = s.decisionLevel()
	s.reason[v] = reason
	s.trail = append(s.trail, l)
	return true
}

func (s *Solver) cancelUntil(lvl int) {
	if lvl >= s.decisionLevel() {
		return
	}
	target := s.trailLim[lvl]
	for i := len(s.trail) - 1; i >= target; i-- {
		v := s.trail[i].Var()
		s.assigns[v] = 0
		s.level[v] = 0
		s.reason[v] = -1
	}
	s.trail = s.trail[:target]
	s.trailLim = s.trailLim[:lvl]
	if s.qhead > target {
		s.qhead = target
	}
}

func (s *Solver) propagate() int {
	for s.qhead < len(s.trail) {
		p := s.trail[s.qhead]
		s.qhead++
		s.propagations++
		// p was assigned true; clauses watching !p now have a false watch.
		falseLit := p.Not()
		ws := s.watches[p]
		s.watches[p] = nil
		for _, ci := range ws {
			c := &s.clauses[ci]
			if c.lits[0] == falseLit {
				c.lits[0], c.lits[1] = c.lits[1], c.lits[0]
			}
			if c.lits[1] != falseLit {
				// Stale watch; keep it for the literal that was expected.
				s.watches[p] = append(s.watches[p], ci)
				continue
			}
			first := c.lits[0]
			if s.valueLit(first) == 1 {
				s.watches[p] = append(s.watches[p], ci)
				continue
			}
			found := false
			for k := 2; k < len(c.lits); k++ {
				if s.valueLit(c.lits[k]) != -1 {
					c.lits[1], c.lits[k] = c.lits[k], c.lits[1]
					s.watches[c.lits[1].Not()] = append(s.watches[c.lits[1].Not()], ci)
					found = true
					break
				}
			}
			if found {
				continue
			}
			s.watches[p] = append(s.watches[p], ci)
			if s.valueLit(first) == -1 {
				s.lastConflict = ci
				return ci
			}
			if !s.enqueue(first, int32(ci)) {
				s.lastConflict = ci
				return ci
			}
		}
	}
	return -1
}

func (s *Solver) bump(v int) {
	s.activity[v] += s.varInc
	if s.activity[v] > 1e100 {
		for i := range s.activity {
			s.activity[i] *= 1e-100
		}
		s.varInc *= 1e-100
	}
}

// analyze performs 1UIP conflict analysis and returns the learned clause and
// the backtrack level. learnt[0] is the asserting literal.
func (s *Solver) analyze(ci int) ([]Lit, int) {
	learnt := []Lit{0}
	seen := make([]bool, s.nvars)
	pathC := 0
	idx := len(s.trail) - 1
	c := &s.clauses[ci]
	p := Lit(-1)
	first := true
	for {
		// The conflict clause is inspected in full. A reason clause's first
		// literal is the literal it propagated; it must not be re-added to
		// the learned clause.
		start := 0
		if !first {
			start = 1
		}
		for _, q := range c.lits[start:] {
			v := q.Var()
			if !seen[v] && s.level[v] > 0 {
				seen[v] = true
				s.bump(v)
				if s.level[v] >= s.decisionLevel() {
					pathC++
				} else {
					learnt = append(learnt, q)
				}
			}
		}
		first = false
		for idx >= 0 && !seen[s.trail[idx].Var()] {
			idx--
		}
		if idx < 0 {
			return nil, 0
		}
		p = s.trail[idx]
		idx--
		seen[p.Var()] = false
		pathC--
		if pathC <= 0 {
			break
		}
		r := s.reason[p.Var()]
		if r < 0 {
			return nil, 0
		}
		c = &s.clauses[r]
	}
	if p < 0 {
		return nil, 0
	}
	learnt[0] = p.Not()
	bt := 0
	if len(learnt) > 1 {
		maxI := 1
		for i := 2; i < len(learnt); i++ {
			if s.level[learnt[i].Var()] > s.level[learnt[maxI].Var()] {
				maxI = i
			}
		}
		learnt[1], learnt[maxI] = learnt[maxI], learnt[1]
		bt = s.level[learnt[1].Var()]
	}
	return learnt, bt
}

// addLearnt installs a learned clause at the current backtrack level and
// returns its index.
func (s *Solver) addLearnt(lits []Lit) int {
	ci := len(s.clauses)
	s.clauses = append(s.clauses, clause{lits: append([]Lit{}, lits...), reason: "learned", learnt: true})
	if len(lits) >= 2 {
		s.watches[lits[0].Not()] = append(s.watches[lits[0].Not()], ci)
		s.watches[lits[1].Not()] = append(s.watches[lits[1].Not()], ci)
	}
	return ci
}

// pickBranchLit selects a literal to set true from an unsatisfied clause.
// Requirement clauses (targets and dependencies) prefer the highest-activity
// candidate, while conflict/at-most-one clauses prefer falsifying the
// lowest-activity candidate. Variables that occur only in satisfied clauses
// are never decided, so unrelated packages stay false.
func (s *Solver) pickBranchLit() Lit {
	for i := range s.clauses {
		c := &s.clauses[i]
		satisfied := false
		for _, l := range c.lits {
			if s.valueLit(l) == 1 {
				satisfied = true
				break
			}
		}
		if satisfied {
			continue
		}
		if c.requirement {
			// Guarded requirement clauses start with their negative guard
			// (¬C). While C is unassigned, satisfying the clause by rejecting
			// C keeps unrelated packages out of the solution; once C is
			// selected (the guard is false), a dependency must be chosen.
			if len(c.lits) > 0 && c.lits[0].Negated() && s.valueLit(c.lits[0]) == 0 {
				return c.lits[0]
			}
			best := Lit(-1)
			bestAct := -1.0
			for _, l := range c.lits {
				if s.valueLit(l) != 0 {
					continue
				}
				v := l.Var()
				if best < 0 || s.activity[v] > bestAct || (s.activity[v] == bestAct && v < best.Var()) {
					best = l
					bestAct = s.activity[v]
				}
			}
			if best >= 0 {
				return best
			}
			// All literals false: a missed conflict. Treat as UNSAT.
			s.ok = false
			s.lastConflict = i
			return Lit(-1)
		}
		// Prefer falsifying a package: a negative literal is satisfied by
		// setting its variable false. Among those, take the least-preferred
		// candidate so the preferred one stays available.
		best := Lit(-1)
		bestAct := math.MaxFloat64
		for _, l := range c.lits {
			if s.valueLit(l) != 0 || !l.Negated() {
				continue
			}
			v := l.Var()
			if best < 0 || s.activity[v] < bestAct || (s.activity[v] == bestAct && v < best.Var()) {
				best = l
				bestAct = s.activity[v]
			}
		}
		if best < 0 {
			// Learned clauses can contain positive literals too.
			for _, l := range c.lits {
				if s.valueLit(l) != 0 {
					continue
				}
				v := l.Var()
				if best < 0 || s.activity[v] < bestAct || (s.activity[v] == bestAct && v < best.Var()) {
					best = l
					bestAct = s.activity[v]
				}
			}
		}
		if best >= 0 {
			return best
		}
		s.ok = false
		s.lastConflict = i
		return Lit(-1)
	}
	return Lit(-1)
}

func (s *Solver) captureExplanation(ci int) {
	if ci < 0 {
		return
	}
	seen := make([]bool, s.nvars)
	reasonSeen := map[string]bool{}
	var walk func(int, int)
	walk = func(idx, depth int) {
		if idx < 0 || depth > 32 || len(s.explanation) >= 8 {
			return
		}
		c := &s.clauses[idx]
		if c.reason != "" && c.reason != "learned" && !reasonSeen[c.reason] {
			reasonSeen[c.reason] = true
			s.explanation = append(s.explanation, c.reason)
		}
		for _, l := range c.lits {
			v := l.Var()
			if seen[v] {
				continue
			}
			seen[v] = true
			if r := s.reason[v]; r >= 0 {
				walk(int(r), depth+1)
			}
		}
	}
	walk(ci, 0)
}
