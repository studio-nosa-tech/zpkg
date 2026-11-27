// Binary package repositories. A repository is a directory (local path) or
// a URL base containing, for every built package:
//
//	index.json                       the repository index (signed in schema 2)
//	index.json.sig                   detached Ed25519 signature
//	keys/<keyid>.pub                 the repository public key
//	<name>-<version>[-rN].pkg.tar.gz  the binary package
//	<...>.manifest                   the file list sidecar
//	<...>.meta.json                  name/version/deps sidecar
//
// Packages may live at the repository root or under packages/. zpkg can
// generate and sign the index ("zpkg mkindex") and can install from a
// repository instead of building from source (ZPKG_BINREPO / BINREPO= in
// /etc/zpkg/binrepo.conf). Signed indexes are required by default; a
// development repository can opt out with ZPKG_ALLOW_UNSIGNED=1.
package build

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgid"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/security"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

// BinMeta is the sidecar metadata of one binary package. It lets a
// repository index be built from binary packages alone, without the recipe
// tree.
type BinMeta struct {
	Schema   int      `json:"schema"`
	Name     string   `json:"name"`
	Slot     string   `json:"slot,omitempty"` // empty means slot 0 (legacy)
	Version  string   `json:"version"`
	Revision int      `json:"revision"`
	UseHash  string   `json:"usehash"` // options hash, "" when no options were set
	Arch     string   `json:"arch"`
	Channel  string   `json:"channel,omitempty"`
	Depends  []string `json:"depends,omitempty"`

	Sandbox       string `json:"sandbox,omitempty"`
	Network       string `json:"network,omitempty"`
	BuildEnvHash  string `json:"build_env_hash,omitempty"`
	BuildEnvCount int    `json:"build_env_count,omitempty"`

	// Reproducibility provenance for a source build.
	RecipeDigest    string `json:"recipe_digest,omitempty"`
	SourceDateEpoch int64  `json:"source_date_epoch,omitempty"`
	ArtifactSHA256  string `json:"artifact_sha256,omitempty"`
	ArtifactSize    int64  `json:"artifact_size,omitempty"`
	ManifestSHA256  string `json:"manifest_sha256,omitempty"`
	LogSHA256       string `json:"log_sha256,omitempty"`
}

const binMetaSchema = 1

func binMetaName(base string) string { return base + ".meta.json" }

// writeBinMeta writes the sidecar next to a packed binary package. Recipe
// deps are captured so the index (and therefore consumers) can record them
// without the recipe tree.
func writeBinMeta(dir, base string, r *recipe.Recipe, optsHash, channel string, res *Result) error {
	m := BinMeta{
		Schema:   binMetaSchema,
		Name:     r.Name,
		Slot:     r.Slot,
		Version:  r.Version,
		Revision: r.Revision,
		UseHash:  optsHash,
		Arch:     runtime.GOARCH,
		Channel:  channel,
		Depends:  append([]string{}, r.Depends...),
	}
	if res != nil {
		m.Sandbox = res.Sandbox
		if res.Sandbox != "" {
			if res.Network {
				m.Network = "on"
			} else {
				m.Network = "off"
			}
		}
		m.BuildEnvHash = res.BuildEnvHash
		m.BuildEnvCount = res.BuildEnvCount
		m.RecipeDigest = res.RecipeDigest
		m.SourceDateEpoch = res.SourceDateEpoch
		m.ArtifactSHA256 = res.ArtifactSHA256
		m.ArtifactSize = res.ArtifactSize
		m.ManifestSHA256 = res.ManifestSHA256
		m.LogSHA256 = res.LogSHA256
	}
	if m.RecipeDigest == "" {
		m.RecipeDigest = recipeDigestFor(r)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, binMetaName(base)), append(data, '\n'), 0o644)
}

// IndexEntry is one binary package in a repository index.
type IndexEntry struct {
	Name           string   `json:"name"`
	Slot           string   `json:"slot,omitempty"` // empty means slot 0
	Version        string   `json:"version"`
	Revision       int      `json:"revision"`
	UseHash        string   `json:"usehash"`
	Arch           string   `json:"arch"`
	Channel        string   `json:"channel,omitempty"`
	RecipeDigest   string   `json:"recipe_digest,omitempty"`
	Sha256         string   `json:"sha256"`
	ManifestSha256 string   `json:"manifest_sha256,omitempty"`
	Size           int64    `json:"size"`
	Filename       string   `json:"filename"`
	Depends        []string `json:"depends,omitempty"`
}

// IndexFile is the repository index itself. Schema 2 adds the anti-rollback
// generation, expiry and the key that signed the exact bytes on disk.
type IndexFile struct {
	Schema     int          `json:"schema"`
	Generated  string       `json:"generated"`
	Generation int64        `json:"generation"`
	Expires    string       `json:"expires,omitempty"`
	KeyID      string       `json:"keyid,omitempty"`
	Arch       string       `json:"arch"`
	Packages   []IndexEntry `json:"packages"`
}

const indexSchema = 2

// IndexOptions controls MakeIndexWithOptions.
type IndexOptions struct {
	Generation  int64     // 0 = previous index generation + 1
	Expires     time.Time // zero = now + 90 days
	SignKeyPath string    // "" = write an unsigned index (development)
}

// MakeIndex scans dir for packed binary packages and writes an unsigned
// index.json using an automatically incremented generation.
func MakeIndex(dir string) (int, error) {
	return MakeIndexWithOptions(dir, IndexOptions{})
}

// MakeIndexWithOptions scans dir (and dir/packages when present), writes
// index.json, and signs it when SignKeyPath is set. Signing also publishes
// the public key at <dir>/keys/<keyid>.pub.
func MakeIndexWithOptions(dir string, opts IndexOptions) (int, error) {
	files, err := collectPackages(dir)
	if err != nil {
		return 0, err
	}
	generation := opts.Generation
	if generation == 0 {
		generation = previousIndexGeneration(dir) + 1
	}
	if generation <= 0 {
		generation = 1
	}
	expires := opts.Expires
	if expires.IsZero() {
		expires = time.Now().Add(90 * 24 * time.Hour)
	}
	idx := IndexFile{
		Schema:     indexSchema,
		Generated:  time.Now().UTC().Format(time.RFC3339),
		Generation: generation,
		Expires:    expires.UTC().Format(time.RFC3339),
		Arch:       runtime.GOARCH,
	}
	var priv ed25519.PrivateKey
	var keyID string
	if opts.SignKeyPath != "" {
		if within(dir, opts.SignKeyPath) {
			return 0, fmt.Errorf("refusing to sign with a private key inside the repository (%s)", opts.SignKeyPath)
		}
		priv, keyID, err = sign.LoadPrivateKey(opts.SignKeyPath)
		if err != nil {
			return 0, err
		}
		idx.KeyID = keyID
	}

	for _, f := range files {
		entry, err := indexEntry(dir, f, priv != nil)
		if err != nil {
			return 0, err
		}
		idx.Packages = append(idx.Packages, entry)
	}
	sort.Slice(idx.Packages, func(i, j int) bool {
		a, b := idx.Packages[i], idx.Packages[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.UseHash < b.UseHash
	})
	if err := validateIndex(&idx, priv != nil); err != nil {
		return 0, err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, err
	}
	data = append(data, '\n')

	indexPath := filepath.Join(dir, "index.json")
	if priv != nil {
		sig, id, err := sign.SignBytes(priv, data)
		if err != nil {
			return 0, err
		}
		if err := writeFileAtomic(indexPath, data, 0o644); err != nil {
			return 0, err
		}
		if err := writeFileAtomic(indexPath+".sig", sig, 0o644); err != nil {
			return 0, err
		}
		pub, _ := priv.Public().(ed25519.PublicKey)
		pubPEM, err := sign.EncodePublicKey(pub)
		if err != nil {
			return 0, err
		}
		keysDir := filepath.Join(dir, "keys")
		if err := os.MkdirAll(keysDir, 0o755); err != nil {
			return 0, err
		}
		if err := writeFileAtomic(filepath.Join(keysDir, id+".pub"), pubPEM, 0o644); err != nil {
			return 0, err
		}
	} else {
		if err := writeFileAtomic(indexPath, data, 0o644); err != nil {
			return 0, err
		}
		// An unsigned rebuild must not leave a signature for the old index
		// bytes behind: clients would (correctly) reject the stale pair.
		if err := os.Remove(indexPath + ".sig"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}
	return len(idx.Packages), nil
}

type pkgFile struct {
	rel  string // slash-separated path relative to the repository root
	full string
}

func collectPackages(dir string) ([]pkgFile, error) {
	var out []pkgFile
	add := func(base, prefix string) error {
		items, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, it := range items {
			if it.IsDir() || !strings.HasSuffix(it.Name(), ".pkg.tar.gz") {
				continue
			}
			rel := it.Name()
			if prefix != "" {
				rel = prefix + "/" + rel
			}
			out = append(out, pkgFile{rel: rel, full: filepath.Join(base, it.Name())})
		}
		return nil
	}
	if err := add(dir, ""); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(filepath.Join(dir, "packages")); err == nil && fi.IsDir() {
		if err := add(filepath.Join(dir, "packages"), "packages"); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

func indexEntry(dir string, f pkgFile, signed bool) (IndexEntry, error) {
	sum, err := hash.File(f.full)
	if err != nil {
		return IndexEntry{}, fmt.Errorf("%s: %w", f.rel, err)
	}
	info, err := os.Stat(f.full)
	if err != nil {
		return IndexEntry{}, err
	}
	base := strings.TrimSuffix(f.full, ".pkg.tar.gz")
	manifestPath := base + ".manifest"
	manifestSum, err := hash.File(manifestPath)
	if err != nil {
		if signed {
			return IndexEntry{}, fmt.Errorf("%s: missing manifest sidecar: %w", f.rel, err)
		}
		manifestSum = ""
	}
	entry := IndexEntry{
		Name:           strings.TrimSuffix(filepath.Base(f.rel), ".pkg.tar.gz"),
		Filename:       f.rel,
		Sha256:         sum,
		ManifestSha256: manifestSum,
		Size:           info.Size(),
		Arch:           runtime.GOARCH,
	}
	// Prefer the sidecar metadata; fall back to the file name.
	var meta BinMeta
	if data, err := os.ReadFile(binMetaName(base)); err == nil {
		if json.Unmarshal(data, &meta) == nil && meta.Name != "" {
			entry.Name = meta.Name
			entry.Slot = meta.Slot
			entry.Version = meta.Version
			entry.Revision = meta.Revision
			entry.UseHash = meta.UseHash
			entry.Arch = meta.Arch
			entry.Channel = meta.Channel
			entry.RecipeDigest = meta.RecipeDigest
			entry.Depends = meta.Depends
		}
	}
	return entry, nil
}

func previousIndexGeneration(dir string) int64 {
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return 0
	}
	var idx IndexFile
	if json.Unmarshal(data, &idx) != nil {
		return 0
	}
	return idx.Generation
}

// LoadRepoBinpkg returns a Result restored from a binary package repository,
// or (nil, nil) when there is no repository configured, no matching package,
// or the repository is unreachable. Signature, rollback, expiry and digest
// failures are hard errors: they must never silently fall back to a source
// build.
func LoadRepoBinpkg(r *recipe.Recipe, cfg config.Config) (*Result, error) {
	if os.Getenv("ZPKG_NO_BINPKG") == "1" {
		return nil, nil
	}
	if cfg.BinRepo == "" {
		return nil, nil
	}
	return loadRepoBinpkgImpl(r, cfg.BinRepo, cfg.Cache, OptionsHash(cfg, r), cfg)
}

// fetchRepoFileVerified returns the local path of a repository file,
// downloading it fresh when the cache does not match wantSHA. Every file with
// a digest is verified before use.
func fetchRepoFileVerified(repo, name, cacheDir, wantSHA string) (string, error) {
	if err := safeRepoFilename(name); err != nil {
		return "", err
	}
	if isURL(repo) {
		cacheName := filepath.Base(name)
		cached := filepath.Join(cacheDir, cacheName)
		if wantSHA != "" {
			if sum, err := hash.File(cached); err == nil && sum == wantSHA {
				return cached, nil
			}
		}
		p, err := source.FetchFresh(repo+"/"+name+"#"+cacheName, cacheDir)
		if err != nil {
			return "", err
		}
		if wantSHA != "" {
			sum, err := hash.File(p)
			if err != nil {
				return "", err
			}
			if sum != wantSHA {
				return "", fmt.Errorf("%s: sha256 mismatch (index %s, downloaded %s)", name, wantSHA, sum)
			}
		}
		return p, nil
	}
	p := filepath.Join(repo, filepath.FromSlash(name))
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	if wantSHA != "" {
		sum, err := hash.File(p)
		if err != nil {
			return "", err
		}
		if sum != wantSHA {
			return "", fmt.Errorf("%s: sha256 mismatch (index %s, actual %s)", name, wantSHA, sum)
		}
	}
	return p, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func readRepoIndex(repo, cacheDir string) (indexData, sigData []byte, available bool, err error) {
	if isURL(repo) {
		idxPath, err := source.FetchFresh(repo+"/index.json", cacheDir)
		if err != nil {
			return nil, nil, false, nil // unreachable repository: source build
		}
		indexData, err = os.ReadFile(idxPath)
		if err != nil {
			return nil, nil, false, err
		}
		sigPath, err := source.FetchFresh(repo+"/index.json.sig", cacheDir)
		if err != nil {
			if source.IsNotFound(err) {
				return indexData, nil, true, nil
			}
			return nil, nil, false, err
		}
		sigData, err = os.ReadFile(sigPath)
		if err != nil {
			return nil, nil, false, err
		}
		return indexData, sigData, true, nil
	}
	indexData, err = os.ReadFile(filepath.Join(repo, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	sigData, err = os.ReadFile(filepath.Join(repo, "index.json.sig"))
	if err != nil {
		if os.IsNotExist(err) {
			return indexData, nil, true, nil
		}
		return nil, nil, false, err
	}
	return indexData, sigData, true, nil
}

func verifyIndex(data, sigData []byte, repo string, cfg config.Config) (*IndexFile, error) {
	signed := len(sigData) > 0
	var signer string
	if !signed {
		if !cfg.AllowUnsigned {
			return nil, fmt.Errorf("repository %s: index.json is not signed (import the repository key with \"zpkg key import <pubkey>\", or set ZPKG_ALLOW_UNSIGNED=1 for a development repository)", normalizeRepo(repo))
		}
	} else {
		keys, err := sign.LoadKeyring(cfg.TrustedKeys)
		if err != nil {
			return nil, err
		}
		if keys.Len() == 0 {
			return nil, fmt.Errorf("repository %s: no trusted keys in %s (use \"zpkg key import <pubkey>\")", normalizeRepo(repo), cfg.TrustedKeys)
		}
		signer, err = sign.VerifyBytes(data, sigData, keys)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", normalizeRepo(repo), err)
		}
	}
	var idx IndexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("repository %s: invalid index.json: %w", normalizeRepo(repo), err)
	}
	if err := validateIndex(&idx, signed); err != nil {
		return nil, fmt.Errorf("repository %s: %w", normalizeRepo(repo), err)
	}
	if signed {
		if idx.KeyID != signer {
			return nil, fmt.Errorf("repository %s: index key ID %s does not match signature key %s", normalizeRepo(repo), idx.KeyID, signer)
		}
		expires, err := time.Parse(time.RFC3339, idx.Expires)
		if err != nil {
			return nil, fmt.Errorf("repository %s: index expiry: %w", normalizeRepo(repo), err)
		}
		if !cfg.AllowExpired && time.Now().After(expires) {
			return nil, fmt.Errorf("repository %s: index expired on %s (ZPKG_ALLOW_EXPIRED=1 overrides for recovery)", normalizeRepo(repo), expires.UTC().Format(time.RFC3339))
		}
		if cfg.DB != "" {
			key := security.Key("binrepo", normalizeRepo(repo))
			entry := security.Entry{
				Kind:       "binrepo",
				Identity:   normalizeRepo(repo),
				KeyID:      signer,
				Generation: idx.Generation,
				SHA256:     sha256Hex(data),
			}
			if err := security.CheckAndUpdate(filepath.Join(cfg.DB, ".security.json"), key, entry); err != nil {
				return nil, fmt.Errorf("repository %s: %w", normalizeRepo(repo), err)
			}
		}
	}
	return &idx, nil
}

func validateIndex(idx *IndexFile, signed bool) error {
	if signed {
		if idx.Schema != indexSchema {
			return fmt.Errorf("unsupported index schema %d", idx.Schema)
		}
		if idx.Arch == "" {
			return fmt.Errorf("index arch is missing")
		}
		if idx.Generation <= 0 {
			return fmt.Errorf("index generation must be positive")
		}
		if !validSHA(idx.KeyID) {
			return fmt.Errorf("invalid index key ID")
		}
		if _, err := time.Parse(time.RFC3339, idx.Expires); err != nil {
			return fmt.Errorf("index expiry: %w", err)
		}
	}
	if idx.Arch != "" && idx.Arch != runtime.GOARCH {
		return fmt.Errorf("index arch %q does not match %s", idx.Arch, runtime.GOARCH)
	}
	seen := map[string]bool{}
	for _, e := range idx.Packages {
		if e.Name == "" {
			return fmt.Errorf("index entry with empty name")
		}
		if e.Slot != "" && !pkgid.ValidSlot(e.Slot) {
			return fmt.Errorf("%s: invalid slot %q", e.Filename, e.Slot)
		}
		key := pkgid.Key(e.Name, e.Slot) + "\x00" + e.Version + "\x00" +
			strconv.Itoa(e.Revision) + "\x00" + e.UseHash + "\x00" + e.Arch + "\x00" + e.Channel
		if seen[key] {
			return fmt.Errorf("%s: duplicate package entry for %s", e.Filename, pkgid.Key(e.Name, e.Slot))
		}
		seen[key] = true
		if err := safeRepoFilename(e.Filename); err != nil {
			return err
		}
		if !validSHA(e.Sha256) {
			return fmt.Errorf("%s: invalid sha256", e.Filename)
		}
		if signed {
			if e.Version == "" {
				return fmt.Errorf("%s: missing version", e.Filename)
			}
			if e.Arch == "" {
				return fmt.Errorf("%s: missing arch", e.Filename)
			}
			if !validSHA(e.ManifestSha256) {
				return fmt.Errorf("%s: missing manifest_sha256", e.Filename)
			}
		}
	}
	return nil
}

func loadRepoBinpkgImpl(r *recipe.Recipe, binRepo, cacheDir, optsHash string, cfg config.Config) (*Result, error) {
	indexData, sigData, available, err := readRepoIndex(binRepo, cacheDir)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, nil
	}
	idx, err := verifyIndex(indexData, sigData, binRepo, cfg)
	if err != nil {
		return nil, err
	}
	wantDigest := recipeDigestFor(r)
	for _, e := range idx.Packages {
		if pkgid.Key(e.Name, e.Slot) != pkgid.Key(r.Name, r.Slot) ||
			(e.Arch != "" && e.Arch != runtime.GOARCH) ||
			recipeKey(e.Version, e.Revision) != r.FullVersion() || e.UseHash != optsHash {
			continue
		}
		// New indexes carry the channel and recipe digest. Legacy indexes
		// have neither, so they remain usable while channels are still
		// identical, but a mismatching entry is never accepted.
		if e.Channel != "" && cfg.Channel != "" && e.Channel != cfg.Channel {
			continue
		}
		if wantDigest != "" && e.RecipeDigest != "" && e.RecipeDigest != wantDigest {
			continue
		}
		pkgPath, err := fetchRepoFileVerified(binRepo, e.Filename, cacheDir, e.Sha256)
		if err != nil {
			return nil, err
		}
		manName := baseOf(e.Filename) + ".manifest"
		if idx.KeyID != "" && e.ManifestSha256 == "" {
			return nil, fmt.Errorf("%s: signed index has no manifest_sha256", e.Filename)
		}
		manPath, err := fetchRepoFileVerified(binRepo, manName, cacheDir, e.ManifestSha256)
		if err != nil {
			return nil, err
		}
		entries, err := manifest.Read(manPath)
		if err != nil {
			return nil, err
		}
		tmp := filepath.Join(cacheDir, "build", PackageBase(r, optsHash), "repo")
		if err := os.RemoveAll(tmp); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			return nil, err
		}
		if err := source.Extract(pkgPath, tmp); err != nil {
			return nil, err
		}
		return &Result{
			Recipe:            r,
			Destroot:          tmp,
			Entries:           entries,
			OriginKind:        "binrepo",
			OriginRepo:        normalizeRepo(binRepo),
			OriginKeyID:       idx.KeyID,
			OriginGeneration:  idx.Generation,
			OriginIndexSHA256: sha256Hex(indexData),
			OriginPkgSHA256:   e.Sha256,
			ArtifactSHA256:    e.Sha256,
			ArtifactSize:      e.Size,
			ManifestSHA256:    e.ManifestSha256,
		}, nil
	}
	return nil, nil
}

func recipeKey(version string, revision int) string {
	if revision > 0 {
		return fmt.Sprintf("%s-r%d", version, revision)
	}
	return version
}

func baseOf(filename string) string {
	return strings.TrimSuffix(filename, ".pkg.tar.gz")
}

func normalizeRepo(repo string) string {
	if isURL(repo) {
		return strings.TrimRight(repo, "/")
	}
	if abs, err := filepath.Abs(repo); err == nil {
		return abs
	}
	return repo
}

func safeRepoFilename(name string) error {
	if name == "" || strings.Contains(name, `\`) || strings.Contains(name, ":") || path.IsAbs(name) {
		return fmt.Errorf("unsafe repository path %q", name)
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe repository path %q", name)
	}
	return nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

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

func writeFileAtomic(file string, data []byte, perm os.FileMode) error {
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
