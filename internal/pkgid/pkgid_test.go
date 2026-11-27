package pkgid

import "testing"

func TestKeySplit(t *testing.T) {
	tests := []struct {
		name, slot, key string
	}{
		{"foo", "0", "foo"},
		{"foo", "", "foo"},
		{"foo", "14", "foo@14"},
		{"foo-bar", "1.1", "foo-bar@1.1"},
	}
	for _, tt := range tests {
		if got := Key(tt.name, tt.slot); got != tt.key {
			t.Errorf("Key(%q, %q) = %q, want %q", tt.name, tt.slot, got, tt.key)
		}
		n, s := Split(tt.key)
		if n != tt.name {
			t.Errorf("Split(%q) name = %q, want %q", tt.key, n, tt.name)
		}
		wantSlot := tt.slot
		if wantSlot == "" {
			wantSlot = "0"
		}
		if s != wantSlot {
			t.Errorf("Split(%q) slot = %q, want %q", tt.key, s, wantSlot)
		}
	}
}

func TestSplitLastAt(t *testing.T) {
	name, slot := Split("foo@bar@14")
	if name != "foo@bar" || slot != "14" {
		t.Fatalf("Split = %q, %q", name, slot)
	}
}

func TestValidSlot(t *testing.T) {
	for _, ok := range []string{"0", "14", "1.1", "py3_12", "gcc-14", "a+b"} {
		if !ValidSlot(ok) {
			t.Errorf("ValidSlot(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a@b", "a b", "../x"} {
		if ValidSlot(bad) {
			t.Errorf("ValidSlot(%q) = true", bad)
		}
	}
}
