// Package version compares package versions.
//
// ZIGNOS versions are "pkgver" optionally followed by "-rN" (recipe
// revision). Comparison is segment-wise: "1.10" is newer than "1.9",
// trailing zeros are insignificant ("1.0" == "1.0.0"), and a higher
// revision beats the same pkgver.
package version

import (
	"strconv"
	"strings"
)

// Parse splits "1.2.3-r4" into ("1.2.3", 4). A missing revision is 0.
func Parse(s string) (pkgver string, revision int) {
	if i := strings.LastIndex(s, "-r"); i >= 0 {
		if n, err := strconv.Atoi(s[i+2:]); err == nil && n >= 0 {
			return s[:i], n
		}
	}
	return s, 0
}

// String renders a pkgver and revision back into the canonical form.
func String(pkgver string, revision int) string {
	if revision > 0 {
		return pkgver + "-r" + strconv.Itoa(revision)
	}
	return pkgver
}

// Compare returns -1 when a is older than b, 0 when equal and 1 when newer.
func Compare(a, b string) int {
	av, ar := Parse(a)
	bv, br := Parse(b)
	if c := comparePkgver(av, bv); c != 0 {
		return c
	}
	switch {
	case ar < br:
		return -1
	case ar > br:
		return 1
	}
	return 0
}

type segment struct {
	num bool
	n   int
	s   string
}

func segments(v string) []segment {
	var out []segment
	var cur strings.Builder
	curNum := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		if curNum {
			n, _ := strconv.Atoi(cur.String())
			out = append(out, segment{num: true, n: n})
		} else {
			out = append(out, segment{s: cur.String()})
		}
		cur.Reset()
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			if cur.Len() > 0 && !curNum {
				flush()
			}
			curNum = true
			cur.WriteRune(r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			if cur.Len() > 0 && curNum {
				flush()
			}
			curNum = false
			cur.WriteRune(r)
		default:
			// separators: . _ - + etc.
			flush()
		}
	}
	flush()
	return out
}

// preRelease reports whether an alphabetic segment marks a version that
// sorts before the plain release (1.0_rc1 < 1.0 but 1.0_p1 > 1.0).
func preRelease(s string) bool {
	switch s {
	case "alpha", "beta", "pre", "rc":
		return true
	}
	return false
}

func comparePkgver(a, b string) int {
	as := segments(a)
	bs := segments(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y segment
		var hasX, hasY bool
		if i < len(as) {
			x, hasX = as[i], true
		}
		if i < len(bs) {
			y, hasY = bs[i], true
		}
		switch {
		case !hasY:
			// a has more segments: trailing zeros are insignificant.
			if x.num && x.n == 0 {
				continue
			}
			if !x.num && preRelease(x.s) {
				return -1
			}
			return 1
		case !hasX:
			if y.num && y.n == 0 {
				continue
			}
			if !y.num && preRelease(y.s) {
				return 1
			}
			return -1
		case x.num && y.num:
			if x.n != y.n {
				if x.n < y.n {
					return -1
				}
				return 1
			}
		case !x.num && !y.num:
			if x.s != y.s {
				if x.s < y.s {
					return -1
				}
				return 1
			}
		case x.num:
			// number vs word: pre-release words sort older, others newer.
			if preRelease(y.s) {
				return 1
			}
			return -1
		default:
			if preRelease(x.s) {
				return -1
			}
			return 1
		}
	}
	return 0
}
