package dep

import (
	"reflect"
	"testing"
)

func TestOrderDepsFirst(t *testing.T) {
	deps := map[string][]string{
		"glibc":    {},
		"binutils": {"glibc"},
		"gcc":      {"binutils", "glibc"},
	}
	load := func(n string) ([]string, error) { return deps[n], nil }

	got, err := Order([]string{"gcc"}, load)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"glibc", "binutils", "gcc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCycleDetected(t *testing.T) {
	deps := map[string][]string{"a": {"b"}, "b": {"a"}}
	load := func(n string) ([]string, error) { return deps[n], nil }

	if _, err := Order([]string{"a"}, load); err == nil {
		t.Fatal("expected cycle error")
	}
}
