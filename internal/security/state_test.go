package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestRollbackAndSameGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".security.json")
	key := Key("binrepo", "https://example.invalid/repo")
	e := Entry{Kind: "binrepo", Identity: "https://example.invalid/repo", KeyID: "k", Generation: 5, SHA256: "aaa"}
	if err := CheckAndUpdate(path, key, e); err != nil {
		t.Fatal(err)
	}

	// Re-seeing the exact object is fine.
	if err := CheckAndUpdate(path, key, e); err != nil {
		t.Fatalf("same object rejected: %v", err)
	}
	// An older generation is a rollback.
	old := e
	old.Generation = 4
	if err := CheckAndUpdate(path, key, old); !errors.Is(err, ErrRollback) {
		t.Fatalf("older generation error = %v, want ErrRollback", err)
	}
	// Same generation with different bytes is a rewrite.
	changed := e
	changed.SHA256 = "bbb"
	if err := CheckAndUpdate(path, key, changed); !errors.Is(err, ErrRollback) {
		t.Fatalf("same-generation change error = %v, want ErrRollback", err)
	}
	// A newer generation is accepted.
	newer := e
	newer.Generation = 6
	newer.SHA256 = "ccc"
	if err := CheckAndUpdate(path, key, newer); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Entries[key].Generation; got != 6 {
		t.Fatalf("stored generation = %d, want 6", got)
	}
}

func TestSeparateSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".security.json")
	a := Key("binrepo", "https://a.example/repo")
	b := Key("recipes", "/usr/share/zignos/recipes")
	if err := CheckAndUpdate(path, a, Entry{Kind: "binrepo", Identity: "a", KeyID: "k", Generation: 10, SHA256: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckAndUpdate(path, b, Entry{Kind: "recipes", Identity: "b", KeyID: "k", Generation: 1, SHA256: "b"}); err != nil {
		t.Fatalf("second source rejected: %v", err)
	}
}

func TestLoadRejectsBadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeTestFile(path, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("malformed state accepted")
	}
}
