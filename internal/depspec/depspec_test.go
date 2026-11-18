package depspec

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw       string
		name      string
		op        Operator
		version   string
		condition string
		negated   bool
		use       int
	}{
		{"foo", "foo", OpAny, "", "", false, 0},
		{">=libgpg-error-1.2", "libgpg-error", OpGE, "1.2", "", false, 0},
		{"<=python3-3.14.10", "python3", OpLE, "3.14.10", "", false, 0},
		{"=foo-1.2-r3", "foo", OpEQ, "1.2-r3", "", false, 0},
		{"ssl?openssl", "openssl", OpAny, "", "ssl", false, 0},
		{"!ssl?gnutls", "gnutls", OpAny, "", "ssl", true, 0},
		{"foo[ssl,-zlib]", "foo", OpAny, "", "", false, 2},
		{"foo[ssl?]", "foo", OpAny, "", "", false, 1},
		{"ssl?foo[ssl?,bar]", "foo", OpAny, "", "ssl", false, 2},
	}
	for _, c := range cases {
		a, err := Parse(c.raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.raw, err)
			continue
		}
		if a.Name != c.name || a.Op != c.op || a.Version != c.version ||
			a.Condition != c.condition || a.Negated != c.negated || len(a.Use) != c.use {
			t.Errorf("Parse(%q) = %+v", c.raw, a)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{"", ">=foo", "?foo", "foo[", "foo[]", "foo[bad flag]", ">=foo-"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", raw)
		}
	}
}

func TestParseProvided(t *testing.T) {
	a, err := ParseProvided("virtual/mailer-2.0")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "virtual/mailer" || a.Version != "2.0" {
		t.Fatalf("got %+v", a)
	}
	a, err = ParseProvided("libfoo.so")
	if err != nil || a.Name != "libfoo.so" || a.Version != "" {
		t.Fatalf("got %+v, %v", a, err)
	}
	if _, err := ParseProvided(">=foo-1"); err == nil {
		t.Fatal("operator in provides accepted")
	}
}

func TestMatches(t *testing.T) {
	a, _ := Parse(">=foo-1.2")
	for v, want := range map[string]bool{
		"1.1": false, "1.2": true, "1.2-r1": true, "1.10": true,
	} {
		if got := a.Matches(v); got != want {
			t.Errorf("%q matches %q = %v, want %v", a.Raw, v, got, want)
		}
	}
	eq, _ := Parse("=foo-1.2")
	if !eq.Matches("1.2-r0") || eq.Matches("1.2-r1") {
		t.Errorf("= constraint must compare revisions: %v %v", eq.Matches("1.2-r0"), eq.Matches("1.2-r1"))
	}
}
