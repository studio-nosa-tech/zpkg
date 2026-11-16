package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.1", -1},
		{"1.9", "1.10", -1},
		{"1.0", "1.0.1", -1},
		{"1.0.0", "1.0", 0},
		{"1.0-r1", "1.0", 1},
		{"1.0-r10", "1.0-r9", 1},
		{"1.0_rc1", "1.0", -1},
		{"1.0", "1.0_p1", -1},
		{"6.8.2", "6.9.0", -1},
		{"9.2.1355", "9.2.999", 1},
		{"1.9.17p2", "1.9.17", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	pkgver, rev := Parse("1.2.3-r4")
	if pkgver != "1.2.3" || rev != 4 {
		t.Fatalf("Parse = (%q, %d), want (1.2.3, 4)", pkgver, rev)
	}
	pkgver, rev = Parse("1.2.3")
	if pkgver != "1.2.3" || rev != 0 {
		t.Fatalf("Parse = (%q, %d), want (1.2.3, 0)", pkgver, rev)
	}
	if got := String("1.2.3", 4); got != "1.2.3-r4" {
		t.Fatalf("String = %q", got)
	}
	if got := String("1.2.3", 0); got != "1.2.3" {
		t.Fatalf("String = %q", got)
	}
}
