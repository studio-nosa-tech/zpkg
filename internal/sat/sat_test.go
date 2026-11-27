package sat

import (
	"math/rand"
	"testing"
)

func addAll(t *testing.T, s *Solver, clauses [][]Lit) {
	t.Helper()
	for _, c := range clauses {
		if !s.AddClause(c, "") {
			return
		}
	}
}

func TestUnitPropagation(t *testing.T) {
	s := New()
	a := s.NewVar()
	b := s.NewVar()
	if !s.AddClause([]Lit{Pos(a), Pos(b)}, "") {
		t.Fatal("clause rejected")
	}
	if !s.AddClause([]Lit{Neg(a)}, "") {
		t.Fatal("unit rejected")
	}
	if !s.Solve(nil) {
		t.Fatal("expected SAT")
	}
	m := s.Model()
	if m[a] || !m[b] {
		t.Fatalf("model a=%v b=%v, want false true", m[a], m[b])
	}
}

func TestDirectUnsat(t *testing.T) {
	s := New()
	a := s.NewVar()
	if !s.AddClause([]Lit{Pos(a)}, "") {
		t.Fatal("unit rejected")
	}
	if s.AddClause([]Lit{Neg(a)}, "") {
		t.Fatal("contradictory unit accepted")
	}
	if s.Solve(nil) {
		t.Fatal("expected UNSAT")
	}
}

func TestTautologyIgnored(t *testing.T) {
	s := New()
	a := s.NewVar()
	if !s.AddClause([]Lit{Pos(a), Neg(a)}, "") {
		t.Fatal("tautology rejected")
	}
	if !s.Solve(nil) {
		t.Fatal("expected SAT")
	}
}

func TestRequirementPrefersHighActivity(t *testing.T) {
	s := New()
	a := s.NewVar()
	b := s.NewVar()
	s.SetActivity(a, 10)
	s.SetActivity(b, 1)
	if !s.AddRequirement([]Lit{Pos(a), Pos(b)}, "pick one") {
		t.Fatal("requirement rejected")
	}
	if !s.Solve(nil) {
		t.Fatal("expected SAT")
	}
	if m := s.Model(); !m[a] || m[b] {
		t.Fatalf("model a=%v b=%v, want true false", m[a], m[b])
	}
}

func TestAtMostOneFalsifiesLowActivity(t *testing.T) {
	s := New()
	a := s.NewVar()
	b := s.NewVar()
	s.SetActivity(a, 10)
	s.SetActivity(b, 1)
	if !s.AddClause([]Lit{Neg(a), Neg(b)}, "") {
		t.Fatal("clause rejected")
	}
	if !s.Solve(nil) {
		t.Fatal("expected SAT")
	}
	if m := s.Model(); m[b] {
		t.Fatalf("low-activity candidate b selected: %v", m)
	}
}

func TestPigeonhole(t *testing.T) {
	const pigeons, holes = 4, 3
	s := New()
	vars := make([][]int, pigeons)
	for p := range vars {
		vars[p] = make([]int, holes)
		for h := range vars[p] {
			vars[p][h] = s.NewVar()
		}
	}
	v := func(p, h int) int { return vars[p][h] }
	var all [][]Lit
	add := func(c []Lit) {
		all = append(all, c)
		if !s.AddClause(c, "") {
			t.Fatalf("clause %v rejected", c)
		}
	}
	for p := 0; p < pigeons; p++ {
		clause := make([]Lit, holes)
		for h := 0; h < holes; h++ {
			clause[h] = Pos(v(p, h))
		}
		if !s.AddRequirement(clause, "pigeon") {
			t.Fatal("pigeon clause rejected")
		}
		all = append(all, clause)
		for h1 := 0; h1 < holes; h1++ {
			for h2 := h1 + 1; h2 < holes; h2++ {
				add([]Lit{Neg(v(p, h1)), Neg(v(p, h2))})
			}
		}
	}
	for h := 0; h < holes; h++ {
		for p1 := 0; p1 < pigeons; p1++ {
			for p2 := p1 + 1; p2 < pigeons; p2++ {
				add([]Lit{Neg(v(p1, h)), Neg(v(p2, h))})
			}
		}
	}
	if s.Solve(nil) {
		m := s.Model()
		for i, c := range all {
			sat := false
			for _, l := range c {
				if m[l.Var()] != l.Negated() {
					sat = true
					break
				}
			}
			if !sat {
				t.Fatalf("model violates clause %d: %v (model %v)", i, c, m)
			}
		}
		t.Fatalf("pigeonhole formula is satisfiable: %v", m)
	}
}

func TestAssumptionsAndCore(t *testing.T) {
	s := New()
	a := s.NewVar()
	b := s.NewVar()
	c := s.NewVar()
	d := s.NewVar()
	s.AddClause([]Lit{Neg(a), Pos(b)}, "a implies b")
	s.AddClause([]Lit{Neg(b), Pos(c)}, "b implies c")
	s.AddClause([]Lit{Neg(c)}, "c is impossible")

	if s.Solve([]Lit{Pos(a), Pos(d)}) {
		t.Fatal("expected UNSAT under a")
	}
	core := s.UnsatCore()
	found := false
	for _, l := range core {
		if l == Pos(a) {
			found = true
		}
	}
	if !found {
		t.Fatalf("core %v does not contain a", core)
	}
	if !s.Solve([]Lit{Pos(d)}) {
		t.Fatal("d alone should be satisfiable")
	}
}

func TestAddClauseAfterSolve(t *testing.T) {
	s := New()
	a := s.NewVar()
	b := s.NewVar()
	s.AddClause([]Lit{Pos(a)}, "")
	if !s.Solve(nil) || !s.Model()[a] {
		t.Fatal("a should be true")
	}
	if !s.AddClause([]Lit{Neg(a), Pos(b)}, "a implies b") {
		t.Fatal("clause rejected")
	}
	if !s.Solve(nil) {
		t.Fatal("expected SAT")
	}
	if m := s.Model(); !m[a] || !m[b] {
		t.Fatalf("model a=%v b=%v, want true true", m[a], m[b])
	}
}

func TestDeterministicModel(t *testing.T) {
	build := func() *Solver {
		s := New()
		vars := make([]int, 6)
		for i := range vars {
			vars[i] = s.NewVar()
			s.SetActivity(vars[i], float64(i))
		}
		s.AddRequirement([]Lit{Pos(vars[0]), Pos(vars[1]), Pos(vars[2])}, "target")
		s.AddClause([]Lit{Neg(vars[0]), Neg(vars[3])}, "conflict")
		s.AddClause([]Lit{Neg(vars[1]), Neg(vars[4])}, "conflict")
		s.AddClause([]Lit{Neg(vars[2]), Neg(vars[5])}, "conflict")
		return s
	}
	var first []bool
	for i := 0; i < 5; i++ {
		s := build()
		if !s.Solve(nil) {
			t.Fatal("expected SAT")
		}
		m := s.Model()
		if i == 0 {
			first = append([]bool{}, m...)
			continue
		}
		for j := range m {
			if m[j] != first[j] {
				t.Fatalf("run %d differs at var %d: %v vs %v", i, j, m, first)
			}
		}
	}
}

func TestRandomFormulasAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const nvars = 5
	for seed := 0; seed < 300; seed++ {
		nclauses := 4 + rng.Intn(10)
		clauses := make([][]Lit, 0, nclauses)
		for i := 0; i < nclauses; i++ {
			var c []Lit
			for j := 0; j < 3; j++ {
				v := rng.Intn(nvars)
				if rng.Intn(2) == 0 {
					c = append(c, Pos(v))
				} else {
					c = append(c, Neg(v))
				}
			}
			clauses = append(clauses, c)
		}
		s := New()
		for i := 0; i < nvars; i++ {
			s.NewVar()
		}
		ok := true
		for _, c := range clauses {
			if !s.AddClause(c, "") {
				ok = false
				break
			}
		}
		got := ok && s.Solve(nil)
		want := bruteForce(nvars, clauses)
		if got != want {
			t.Fatalf("seed %d: solver=%v brute=%v clauses=%v", seed, got, want, clauses)
		}
		if got {
			m := s.Model()
			for _, c := range clauses {
				sat := false
				for _, l := range c {
					if m[l.Var()] != l.Negated() {
						sat = true
						break
					}
				}
				if !sat {
					t.Fatalf("seed %d: model %v does not satisfy %v", seed, m, c)
				}
			}
		}
	}
}

func bruteForce(nvars int, clauses [][]Lit) bool {
	for mask := 0; mask < 1<<nvars; mask++ {
		ok := true
		for _, c := range clauses {
			sat := false
			for _, l := range c {
				val := mask&(1<<l.Var()) != 0
				if val != l.Negated() {
					sat = true
					break
				}
			}
			if !sat {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
