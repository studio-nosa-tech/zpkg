// Package recipesig signs a recipe tree as a whole. The signed object is a
// manifest of every regular file (path, sha256, size, mode), so the same
// verification works for a directory, a recipe tarball, or the tree bundled
// in a stage3 tarball.
package recipesig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
)

const (
	// ManifestName is the unsigned file list at the recipe tree root.
	ManifestName = "recipes.manifest"
	// SignatureName is the detached signature of ManifestName.
	SignatureName = "recipes.manifest.sig"
	// Schema is the manifest schema version.
	Schema = 1
)

// ErrUnsigned means the tree has no manifest or signature. Callers may allow
// this explicitly for development trees.
var ErrUnsigned = errors.New("recipe tree is not signed")

// File is one regular file in a recipe tree.
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

// Manifest is the signed description of a recipe tree.
type Manifest struct {
	Schema     int    `json:"schema"`
	Generation int64  `json:"generation"`
	Generated  string `json:"generated"`
	Expires    string `json:"expires"`
	KeyID      string `json:"keyid"`
	Files      []File `json:"files"`
}

// Verified is a manifest whose signature and file contents were checked.
type Verified struct {
	Manifest *Manifest
	SHA256   string // sha256 of the manifest file bytes
}

// Build inventories root. Symlinks and special files are rejected: a signed
// tree must not be able to change meaning depending on where it is unpacked.
func Build(root string) (*Manifest, error) {
	m := &Manifest{Schema: Schema}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ManifestName || rel == SignatureName {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are not allowed in a signed recipe tree", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		sum, err := hash.File(p)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, File{
			Path:   rel,
			SHA256: sum,
			Size:   info.Size(),
			Mode:   fmt.Sprintf("%04o", info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

// Sign builds root's manifest, writes it, and writes its detached signature.
// generation 0 means "previous manifest generation + 1" (or 1 for a new
// tree); expires zero means 90 days from now.
func Sign(root, keyPath string, generation int64, expires time.Time) (*Manifest, error) {
	priv, keyID, err := sign.LoadPrivateKey(keyPath)
	if err != nil {
		return nil, err
	}
	if within(root, keyPath) {
		return nil, fmt.Errorf("refusing to sign with a private key inside the recipe tree (%s)", keyPath)
	}
	if generation == 0 {
		generation = previousGeneration(root) + 1
	}
	if generation <= 0 {
		return nil, fmt.Errorf("generation must be positive")
	}
	if expires.IsZero() {
		expires = time.Now().Add(90 * 24 * time.Hour)
	}
	m, err := Build(root)
	if err != nil {
		return nil, err
	}
	m.Generation = generation
	m.Generated = time.Now().UTC().Format(time.RFC3339)
	m.Expires = expires.UTC().Format(time.RFC3339)
	m.KeyID = keyID
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := writeAtomic(filepath.Join(root, ManifestName), data, 0o644); err != nil {
		return nil, err
	}
	sig, _, err := sign.SignBytes(priv, data)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(root, SignatureName), sig, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

// Verify checks the signature, generation metadata, expiry, and every file in
// root. If both manifest and signature are absent, allowUnsigned returns
// (nil, nil); a manifest with a missing/invalid signature is always an error.
func Verify(root string, keys *sign.Keyring, allowUnsigned, allowExpired bool) (*Verified, error) {
	manifestPath := filepath.Join(root, ManifestName)
	sigPath := filepath.Join(root, SignatureName)
	mData, mErr := os.ReadFile(manifestPath)
	sData, sErr := os.ReadFile(sigPath)
	mMissing := os.IsNotExist(mErr)
	sMissing := os.IsNotExist(sErr)
	if mErr != nil && !mMissing {
		return nil, mErr
	}
	if sErr != nil && !sMissing {
		return nil, sErr
	}
	if mMissing && sMissing {
		if allowUnsigned {
			return nil, nil
		}
		return nil, ErrUnsigned
	}
	if mMissing || sMissing {
		return nil, fmt.Errorf("incomplete recipe signature: manifest present=%t, signature present=%t", !mMissing, !sMissing)
	}

	signer, err := sign.VerifyBytes(mData, sData, keys)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(mData, &m); err != nil {
		return nil, fmt.Errorf("invalid recipe manifest: %w", err)
	}
	if m.Schema != Schema {
		return nil, fmt.Errorf("unsupported recipe manifest schema %d", m.Schema)
	}
	if m.KeyID != signer {
		return nil, fmt.Errorf("recipe manifest key ID %s does not match signature key %s", m.KeyID, signer)
	}
	if m.Generation <= 0 {
		return nil, fmt.Errorf("recipe manifest generation must be positive")
	}
	expires, err := time.Parse(time.RFC3339, m.Expires)
	if err != nil {
		return nil, fmt.Errorf("recipe manifest expiry: %w", err)
	}
	if !allowExpired && time.Now().After(expires) {
		return nil, fmt.Errorf("recipe manifest expired on %s (ZPKG_ALLOW_EXPIRED=1 overrides for recovery)", expires.UTC().Format(time.RFC3339))
	}
	if err := verifyTree(root, &m); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(mData)
	return &Verified{Manifest: &m, SHA256: hex.EncodeToString(sum[:])}, nil
}

// ReadManifest parses the manifest without verifying it. It is used only for
// provenance display after a successful sync.
func ReadManifest(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func verifyTree(root string, m *Manifest) error {
	want := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		if err := checkRelPath(f.Path); err != nil {
			return fmt.Errorf("recipe manifest: %w", err)
		}
		if _, dup := want[f.Path]; dup {
			return fmt.Errorf("recipe manifest lists %s twice", f.Path)
		}
		want[f.Path] = f
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ManifestName || rel == SignatureName {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are not allowed in a signed recipe tree", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		wf, ok := want[rel]
		if !ok {
			return fmt.Errorf("recipe file %s is not covered by the signed manifest", rel)
		}
		if info.Size() != wf.Size {
			return fmt.Errorf("%s: size %d, manifest says %d", rel, info.Size(), wf.Size)
		}
		sum, err := hash.File(p)
		if err != nil {
			return err
		}
		if sum != wf.SHA256 {
			return fmt.Errorf("%s: sha256 mismatch (manifest %s, actual %s)", rel, wf.SHA256, sum)
		}
		if runtime.GOOS != "windows" {
			mode := fmt.Sprintf("%04o", info.Mode().Perm())
			if mode != wf.Mode {
				return fmt.Errorf("%s: mode %s, manifest says %s", rel, mode, wf.Mode)
			}
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	for p := range want {
		if !seen[p] {
			return fmt.Errorf("recipe file %s is missing", p)
		}
	}
	return nil
}

func checkRelPath(p string) error {
	if p == "" || strings.Contains(p, `\`) || path.IsAbs(p) || strings.Contains(p, ":") {
		return fmt.Errorf("unsafe path %q", p)
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe path %q", p)
	}
	return nil
}

func previousGeneration(root string) int64 {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return 0
	}
	var m Manifest
	if json.Unmarshal(data, &m) != nil {
		return 0
	}
	return m.Generation
}

// within reports whether file is inside root (after making both absolute).
func within(root, file string) bool {
	r, err1 := filepath.Abs(root)
	f, err2 := filepath.Abs(file)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(r, f)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeAtomic(file string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(file)
	f, err := os.CreateTemp(dir, "."+filepath.Base(file)+".tmp-")
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
	if err := f.Chmod(perm); err != nil {
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
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	ok = true
	return nil
}
