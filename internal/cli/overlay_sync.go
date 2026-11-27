package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/overlay"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
	"github.com/studio-nosa-tech/zpkg/internal/security"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

type overlayStats struct {
	Packages   int
	Categories []string
	Shadowed   []string
	Commit     string
	Unchanged  bool
	Signed     bool
	Generation int64
	KeyID      string
}

// syncOverlay fetches one overlay into $ZPKG_ROOT/var/lib/zpkg/overlays/<name>.
// The staged tree is validated and signature-checked before the atomic swap.
func syncOverlay(cfg config.Config, spec overlay.Spec, dryRun bool) (overlayStats, error) {
	if !overlay.ValidName(spec.Name) {
		return overlayStats{}, fmt.Errorf("invalid overlay name %q", spec.Name)
	}
	from := strings.TrimSpace(spec.URL)
	if from == "" {
		return overlayStats{}, fmt.Errorf("overlay %s has no URL", spec.Name)
	}
	dest := overlay.TreeDir(cfg.Root, spec.Name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return overlayStats{}, err
	}

	if isGitSource(from) {
		return syncGitOverlay(cfg, spec, dest, dryRun)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".zignos-overlay-sync-")
	if err != nil {
		return overlayStats{}, err
	}
	defer os.RemoveAll(tmp)
	staging := filepath.Join(tmp, "overlay")

	var srcRoot string
	if fi, err := os.Stat(from); err == nil && fi.IsDir() {
		srcRoot, err = findOverlayRoot(from)
		if err != nil {
			return overlayStats{}, err
		}
	} else if isHTTP(from) {
		phase("Downloading %s", from)
		archive, err := source.FetchFresh(from, cfg.Cache)
		if err != nil {
			return overlayStats{}, err
		}
		extractDir := filepath.Join(tmp, "extract")
		if err := source.Extract(archive, extractDir); err != nil {
			return overlayStats{}, err
		}
		srcRoot, err = findOverlayRoot(extractDir)
		if err != nil {
			return overlayStats{}, err
		}
	} else {
		return overlayStats{}, fmt.Errorf("%s: overlay source must be a git URL, an http(s) tarball or a directory", from)
	}
	if err := copyTree(srcRoot, staging); err != nil {
		return overlayStats{}, err
	}
	return installOverlayTree(cfg, spec, staging, dest, "", dryRun)
}

func syncGitOverlay(cfg config.Config, spec overlay.Spec, dest string, dryRun bool) (overlayStats, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return overlayStats{}, fmt.Errorf("git is required for a git overlay; install it with \"zpkg install git\"")
	}
	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}
	remoteCommit, err := gitRemoteCommit(git, spec.URL, branch)
	if err != nil {
		return overlayStats{}, err
	}
	commitPath := dest + ".commit"
	if readOverlayCommit(commitPath) == remoteCommit {
		if stats, err := validateOverlayTree(dest); err == nil {
			stats.Unchanged = true
			stats.Commit = remoteCommit
			stats.Signed, stats.Generation, stats.KeyID = overlaySignature(dest)
			return stats, nil
		}
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".zignos-overlay-git-")
	if err != nil {
		return overlayStats{}, err
	}
	defer os.RemoveAll(tmp)
	cloneDir := filepath.Join(tmp, "repo")
	phase("Fetching %s (branch %s)", spec.URL, branch)
	if _, err := gitCommand(git, "clone", "--depth", "1", "--single-branch",
		"--branch", branch, spec.URL, cloneDir); err != nil {
		return overlayStats{}, err
	}
	srcRoot, err := findOverlayRoot(cloneDir)
	if err != nil {
		return overlayStats{}, err
	}
	staging := filepath.Join(tmp, "overlay")
	if err := copyTree(srcRoot, staging); err != nil {
		return overlayStats{}, err
	}
	return installOverlayTree(cfg, spec, staging, dest, remoteCommit, dryRun)
}

func installOverlayTree(cfg config.Config, spec overlay.Spec, staging, dest, commit string, dryRun bool) (overlayStats, error) {
	stats, err := validateOverlayTree(staging)
	if err != nil {
		return stats, err
	}
	keys, err := sign.LoadKeyring(cfg.TrustedKeys)
	if err != nil {
		return stats, err
	}
	verified, err := recipesig.Verify(staging, keys, spec.AllowUnsigned || cfg.AllowUnsigned, cfg.AllowExpired)
	if err != nil {
		return stats, err
	}
	if verified == nil && !dryRun {
		fmt.Fprintln(os.Stderr, "zpkg: warning: overlay is unsigned (allowed by ALLOW_UNSIGNED)")
	}
	if verified != nil {
		stats.Signed = true
		stats.Generation = verified.Manifest.Generation
		stats.KeyID = verified.Manifest.KeyID
	}
	stats.Commit = commit
	stats.Shadowed = shadowedKeys(cfg, staging)
	if dryRun {
		return stats, nil
	}

	var store *security.Store
	var statePath, secKey string
	if verified != nil && cfg.DB != "" {
		statePath = filepath.Join(cfg.DB, ".security.json")
		store, err = security.Load(statePath)
		if err != nil {
			return stats, err
		}
		secKey = security.Key("overlay", spec.Name)
		if err := store.Check(secKey, verified.Manifest.Generation, verified.SHA256); err != nil {
			return stats, err
		}
	}
	if err := swapTree(staging, dest); err != nil {
		return stats, err
	}
	if store != nil {
		store.Update(secKey, security.Entry{
			Kind:       "overlay",
			Identity:   spec.Name,
			KeyID:      verified.Manifest.KeyID,
			Generation: verified.Manifest.Generation,
			SHA256:     verified.SHA256,
		})
		if err := store.Save(statePath); err != nil {
			return stats, err
		}
	}
	if commit != "" {
		if err := writeOverlayCommit(dest+".commit", spec.URL, spec.Branch, commit); err != nil {
			fmt.Fprintln(os.Stderr, "zpkg: warning: record overlay commit:", err)
		}
	} else {
		_ = os.Remove(dest + ".commit")
	}
	return stats, nil
}

// validateOverlayTree checks metadata, category dirs and every recipe. A
// zero-package skeleton is valid, which lets the official repository start
// empty and grow over time.
func validateOverlayTree(dir string) (overlayStats, error) {
	meta, err := overlay.ReadMeta(dir)
	if err != nil {
		return overlayStats{}, err
	}
	repos := []string{dir}
	if len(meta.Categories) > 0 {
		repos = nil
		for _, cat := range meta.Categories {
			catDir := filepath.Join(dir, cat)
			if !isDirPath(catDir) {
				return overlayStats{}, fmt.Errorf("%s: category %q is missing", dir, cat)
			}
			repos = append(repos, catDir)
		}
	}
	seen := map[string]string{}
	for _, repo := range repos {
		rs, err := recipe.ScanRepo(repo)
		if err != nil {
			return overlayStats{}, err
		}
		for _, r := range rs {
			k := r.Key()
			if prev, ok := seen[k]; ok {
				return overlayStats{}, fmt.Errorf("overlay %s: duplicate package %s in %s and %s", dir, k, prev, repo)
			}
			seen[k] = repo
		}
	}
	return overlayStats{Packages: len(seen), Categories: append([]string{}, meta.Categories...)}, nil
}

func shadowedKeys(cfg config.Config, staging string) []string {
	base, err := recipe.ScanRepos(cfg.BaseRepos)
	if err != nil {
		return nil
	}
	baseKeys := make(map[string]bool, len(base))
	for _, r := range base {
		baseKeys[r.Key()] = true
	}
	meta, err := overlay.ReadMeta(staging)
	if err != nil {
		return nil
	}
	repos := []string{staging}
	if len(meta.Categories) > 0 {
		repos = nil
		for _, cat := range meta.Categories {
			repos = append(repos, filepath.Join(staging, cat))
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, repo := range repos {
		rs, err := recipe.ScanRepo(repo)
		if err != nil {
			continue
		}
		for _, r := range rs {
			if baseKeys[r.Key()] && !seen[r.Key()] {
				seen[r.Key()] = true
				out = append(out, r.Key())
			}
		}
	}
	sort.Strings(out)
	return out
}

// findOverlayRoot accepts a repo checkout root, a GitHub tarball's top-level
// directory, or a flat overlay root.
func findOverlayRoot(path string) (string, error) {
	if isOverlayRoot(path) {
		return path, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(path, e.Name())
		if isOverlayRoot(child) {
			return child, nil
		}
	}
	return "", fmt.Errorf("%s: no overlay.conf or package directory found", path)
}

func isOverlayRoot(dir string) bool {
	if fileExists(filepath.Join(dir, overlay.MetaFile)) {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if fileExists(filepath.Join(child, "version")) || isDirPath(filepath.Join(child, "versions")) {
			return true
		}
	}
	return false
}

func overlaySignature(dir string) (signed bool, generation int64, keyID string) {
	m, err := recipesig.ReadManifest(dir)
	if err != nil || m.KeyID == "" {
		return false, 0, ""
	}
	return true, m.Generation, m.KeyID
}

func isDirPath(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func readOverlayCommit(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "commit=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "commit="))
		}
	}
	return ""
}

func writeOverlayCommit(path, remote, branch, commit string) error {
	if branch == "" {
		branch = "main"
	}
	body := fmt.Sprintf("# generated by zpkg overlay\nremote=%s\nbranch=%s\ncommit=%s\n", remote, branch, commit)
	return os.WriteFile(path, []byte(body), 0o644)
}
