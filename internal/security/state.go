// Package security stores the last accepted generation and digest for each
// signed distribution source. Comparing a freshly fetched, signed index or
// recipe manifest against this state turns a valid old signature into a
// rejected rollback.
package security

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateSchema is the on-disk schema of the security state file.
const StateSchema = 1

// ErrRollback marks a generation regression or a same-generation content
// change.
var ErrRollback = errors.New("rollback detected")

// Entry records the newest signed object accepted from one source.
type Entry struct {
	Kind       string `json:"kind"`
	Identity   string `json:"identity"`
	KeyID      string `json:"keyid"`
	Generation int64  `json:"generation"`
	SHA256     string `json:"sha256"`
	Updated    string `json:"updated"`
}

// Store is the persistent security state.
type Store struct {
	Schema  int              `json:"schema"`
	Entries map[string]Entry `json:"entries"`
}

// New returns an empty store.
func New() *Store {
	return &Store{Schema: StateSchema, Entries: map[string]Entry{}}
}

// Load reads path. A missing file is an empty store; an unparsable file is an
// error so a damaged state cannot silently disable rollback protection.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Schema != StateSchema {
		return nil, fmt.Errorf("%s: unsupported security state schema %d", path, s.Schema)
	}
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	return &s, nil
}

// Key builds the state key for a source. identity is the repository URL/path
// or the recipe destination directory; it is hashed so arbitrary URLs with
// slashes and colons become a stable map key.
func Key(kind, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// Check rejects a generation older than the last accepted one and a different
// object at the same generation. Re-syncing the exact same object is allowed.
func (s *Store) Check(key string, generation int64, sha string) error {
	prev, ok := s.Entries[key]
	if !ok {
		return nil
	}
	if generation < prev.Generation {
		return fmt.Errorf("%w: generation %d is older than the last accepted %d", ErrRollback, generation, prev.Generation)
	}
	if generation == prev.Generation && sha != prev.SHA256 {
		return fmt.Errorf("%w: generation %d content changed (index/manifest was rewritten without a generation bump)", ErrRollback, generation)
	}
	return nil
}

// Update records the newest accepted object.
func (s *Store) Update(key string, e Entry) {
	if e.Updated == "" {
		e.Updated = time.Now().UTC().Format(time.RFC3339)
	}
	s.Entries[key] = e
}

// Save writes the store atomically.
func (s *Store) Save(path string) error {
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	if s.Schema == 0 {
		s.Schema = StateSchema
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// CheckAndUpdate loads path, applies the rollback check, and saves the new
// entry. Unchanged entries do not cause a write.
func CheckAndUpdate(path, key string, e Entry) error {
	s, err := Load(path)
	if err != nil {
		return err
	}
	if err := s.Check(key, e.Generation, e.SHA256); err != nil {
		return err
	}
	if prev, ok := s.Entries[key]; ok &&
		prev.Generation == e.Generation && prev.SHA256 == e.SHA256 && prev.KeyID == e.KeyID {
		return nil
	}
	s.Update(key, e)
	return s.Save(path)
}
