package world

import (
	"reflect"
	"testing"
)

func TestAddRemoveRead(t *testing.T) {
	dir := t.TempDir()
	if err := Add(dir, "vim"); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, "git"); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, "vim"); err != nil { // idempotent
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"git", "vim"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
	if err := Remove(dir, "vim"); err != nil {
		t.Fatal(err)
	}
	got, _ = Read(dir)
	if want := []string{"git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Remove = %v, want %v", got, want)
	}
}

func TestReadMissing(t *testing.T) {
	names, err := Read(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty world, got %v", names)
	}
}
