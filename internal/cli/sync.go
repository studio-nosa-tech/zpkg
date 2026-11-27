package cli

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
	"github.com/studio-nosa-tech/zpkg/internal/security"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

const syncUsage = `Usage:
  zpkg sync [--from <dir|tarball|URL>] [--dest <dir>] [--git] [--branch <name>]
            [-n] [--allow-unsigned]

Replace the recipe tree as a whole. The source may be:
  - a stage3 tarball (only usr/share/zignos/recipes is extracted)
  - a recipe-only tarball/directory
  - a directory containing usr/share/zignos/recipes
  - a git repository (--git, or RECIPEGIT=1 in /etc/zpkg/sync.conf)

The tree may have release channels (stable/, testing/, unstable/, each with
core/ and main/) or the legacy flat layout (core/ and main/ at the top).
--from is required unless ZPKG_RECIPE_URL or RECIPEURL in
/etc/zpkg/sync.conf is set. The new tree is validated (core and main) and
its recipes.manifest signature is verified against the trusted keys before
the old tree is replaced. --allow-unsigned accepts a tree with no signature
at all (development only; an invalid signature is always rejected).

In git mode the commit recorded in <dest>/.zpkg-git is compared with the
remote branch (git ls-remote). The tree is cloned and replaced only when the
commit changed; an up-to-date tree is left untouched.
`

type syncOptions struct {
	from          string
	dest          string
	dryRun        bool
	allowUnsigned bool
	git           bool
	branch        string
}

type categoryCounts struct {
	core    int
	main    int
	desktop int
}

func (c categoryCounts) total() int { return c.core + c.main + c.desktop }

// syncStats is per channel. Legacy is true for a flat core/main tree
// installed before release channels existed.
type syncStats struct {
	channels  map[string]categoryCounts
	legacy    bool
	unchanged bool
	commit    string
}

func (s syncStats) total() int {
	n := 0
	for _, c := range s.channels {
		n += c.total()
	}
	return n
}

// summary renders the validation result for "zpkg sync".
func (s syncStats) summary() string {
	if s.legacy {
		c := s.channels["legacy"]
		return fmt.Sprintf("%d recipes (legacy flat: core %d, main %d)", c.total(), c.core, c.main)
	}
	var parts []string
	for _, ch := range config.Channels {
		c, ok := s.channels[ch]
		if !ok {
			continue
		}
		part := fmt.Sprintf("%s %d/%d", ch, c.core, c.main)
		if c.desktop > 0 {
			part += fmt.Sprintf("/%d", c.desktop)
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("%d recipes (%s)", s.total(), strings.Join(parts, ", "))
}

func parseSyncArgs(args []string) (syncOptions, error) {
	var o syncOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" || a == "--dry-run":
			o.dryRun = true
		case a == "--allow-unsigned":
			o.allowUnsigned = true
		case a == "--git":
			o.git = true
		case a == "--branch":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--branch needs an argument")
			}
			i++
			o.branch = args[i]
		case a == "--from" || a == "--dest":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs an argument", a)
			}
			i++
			if a == "--from" {
				o.from = args[i]
			} else {
				o.dest = args[i]
			}
		case strings.HasPrefix(a, "--from="):
			o.from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "--dest="):
			o.dest = strings.TrimPrefix(a, "--dest=")
		case strings.HasPrefix(a, "--branch="):
			o.branch = strings.TrimPrefix(a, "--branch=")
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q", a)
		default:
			if o.from != "" {
				return o, fmt.Errorf("multiple recipe sources given (%q and %q)", o.from, a)
			}
			o.from = a
		}
	}
	return o, nil
}

// cmdSync replaces the shipped recipe tree with a newer one. Unlike
// "upgrade" (which applies the current recipes to installed packages), this
// is the only command that changes the recipe data itself.
func cmdSync(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(syncUsage)
		return nil
	}
	o, err := parseSyncArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	from := o.from
	if from == "" {
		from = cfg.RecipeURL
	}
	if from == "" {
		return fmt.Errorf("no recipe source: pass --from <dir|tarball|URL> or set ZPKG_RECIPE_URL / RECIPEURL in /etc/zpkg/sync.conf")
	}
	dest := o.dest
	if dest == "" {
		dest = filepath.Join(cfg.Root, "usr", "share", "zignos", "recipes")
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	branch := o.branch
	if branch == "" {
		branch = cfg.RecipeBranch
	}
	allowUnsigned := o.allowUnsigned || cfg.RecipeAllowUnsigned
	phase("Syncing recipes from %s", from)
	var stats syncStats
	if o.git || cfg.RecipeGit || isGitSource(from) {
		stats, err = syncGitRecipes(cfg, from, branch, dest, allowUnsigned, o.dryRun)
	} else {
		stats, err = syncRecipes(cfg, from, dest, allowUnsigned, o.dryRun)
	}
	if err != nil {
		return err
	}
	phase("Recipe tree: %s", stats.summary())
	if stats.unchanged {
		phase("Recipe tree is up to date (%s)", shortCommit(stats.commit))
		return nil
	}
	if o.dryRun {
		fmt.Printf("Would update %s\n", dest)
		return nil
	}
	if m, err := recipesig.ReadManifest(dest); err == nil && m.KeyID != "" {
		phase("Recipe signature: generation %d, key %s", m.Generation, m.KeyID)
	}
	phase("Updated %s", dest)
	return nil
}

// syncRecipes stages the source tree in a sibling temporary directory and
// swaps it with dest only after validation and signature verification. A
// failed copy/extraction/verification never touches the working tree.
func syncRecipes(cfg config.Config, from, dest string, allowUnsigned, dryRun bool) (syncStats, error) {
	parent := filepath.Dir(dest)
	tmp, err := os.MkdirTemp(parent, ".zignos-recipes-sync-")
	if err != nil {
		return syncStats{}, err
	}
	defer os.RemoveAll(tmp)

	extractDir := filepath.Join(tmp, "extract")

	var srcRoot string
	if isHTTP(from) {
		phase("Downloading %s", from)
		archive, err := source.FetchFresh(from, cfg.Cache)
		if err != nil {
			return syncStats{}, err
		}
		if err := extractRecipeArchive(archive, extractDir); err != nil {
			return syncStats{}, err
		}
		srcRoot, err = findRecipeTreeDeep(extractDir)
		if err != nil {
			return syncStats{}, err
		}
	} else {
		fi, err := os.Stat(from)
		if err != nil {
			return syncStats{}, err
		}
		if fi.IsDir() {
			srcRoot, err = findRecipeTree(from)
			if err != nil {
				srcRoot, err = findRecipeTreeDeep(from)
			}
			if err != nil {
				return syncStats{}, err
			}
		} else {
			if err := extractRecipeArchive(from, extractDir); err != nil {
				return syncStats{}, err
			}
			srcRoot, err = findRecipeTreeDeep(extractDir)
			if err != nil {
				return syncStats{}, err
			}
		}
	}

	return installRecipeTree(cfg, srcRoot, dest, allowUnsigned, dryRun, nil)
}

// installRecipeTree copies a resolved source tree into a staging directory,
// validates it, and atomically swaps it with dest. A non-nil stamp records
// the git source in .zpkg-git inside the new tree.
func installRecipeTree(cfg config.Config, srcRoot, dest string, allowUnsigned, dryRun bool, stamp *gitStamp) (syncStats, error) {
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".zignos-recipes-sync-")
	if err != nil {
		return syncStats{}, err
	}
	defer os.RemoveAll(tmp)
	staging := filepath.Join(tmp, "recipes")
	if err := copyTree(srcRoot, staging); err != nil {
		return syncStats{}, err
	}
	if stamp != nil {
		if err := writeGitStamp(staging, *stamp); err != nil {
			return syncStats{}, err
		}
	}
	stats, err := validateRecipeTree(cfg, staging, dest, allowUnsigned, dryRun)
	if err != nil {
		return stats, err
	}
	if dryRun {
		return stats, nil
	}

	// Move the old tree aside inside tmp (same filesystem), then install
	// the staged tree. The deferred RemoveAll cleans up the backup.
	if err := swapTree(staging, dest); err != nil {
		return stats, err
	}
	return stats, nil
}

// swapTree replaces dest with staging using a sibling backup so a failed
// rename puts the old tree back. The caller keeps the temporary parent alive
// until this returns and cleans up the backup afterwards.
func swapTree(staging, dest string) error {
	backup := filepath.Join(filepath.Dir(staging), "old")
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("move old tree aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, dest); err != nil {
		// Best effort: put the old tree back so the system stays usable.
		if _, statErr := os.Lstat(backup); statErr == nil {
			_ = os.Rename(backup, dest)
		}
		return fmt.Errorf("install new tree: %w", err)
	}
	return nil
}

// validateRecipeTree scans the staged tree and verifies its recipe manifest.
func validateRecipeTree(cfg config.Config, staging, dest string, allowUnsigned, dryRun bool) (syncStats, error) {
	stats, err := scanRecipeTree(staging)
	if err != nil {
		return stats, err
	}
	keys, err := sign.LoadKeyring(cfg.TrustedKeys)
	if err != nil {
		return stats, err
	}
	verified, err := recipesig.Verify(staging, keys, allowUnsigned || cfg.AllowUnsigned, cfg.AllowExpired)
	if err != nil {
		return stats, err
	}
	if verified == nil {
		fmt.Fprintln(os.Stderr, "zpkg: warning: recipe tree is unsigned (allowed by --allow-unsigned / ZPKG_ALLOW_UNSIGNED)")
		return stats, nil
	}
	if cfg.DB == "" {
		return stats, nil
	}
	statePath := filepath.Join(cfg.DB, ".security.json")
	key := security.Key("recipes", dest)
	store, err := security.Load(statePath)
	if err != nil {
		return stats, err
	}
	if err := store.Check(key, verified.Manifest.Generation, verified.SHA256); err != nil {
		return stats, err
	}
	if !dryRun {
		store.Update(key, security.Entry{
			Kind:       "recipes",
			Identity:   dest,
			KeyID:      verified.Manifest.KeyID,
			Generation: verified.Manifest.Generation,
			SHA256:     verified.SHA256,
		})
		if err := store.Save(statePath); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// hasCategories reports a flat legacy tree: core/ and main/ directly below.
func hasCategories(dir string) bool {
	for _, cat := range []string{"core", "main"} {
		fi, err := os.Stat(filepath.Join(dir, cat))
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// isRecipeTree accepts either a channel tree (any of stable/testing/unstable
// with core/ and main/) or the legacy flat layout.
func isRecipeTree(dir string) bool {
	if hasCategories(dir) {
		return true
	}
	for _, ch := range config.Channels {
		if hasCategories(filepath.Join(dir, ch)) {
			return true
		}
	}
	return false
}

// findRecipeTree recognizes the two layouts users normally have: a recipe
// checkout (channel dirs or legacy core/main at the root) and an unpacked
// stage3 (the tree under usr/share/zignos/recipes).
func findRecipeTree(path string) (string, error) {
	for _, candidate := range []string{
		path,
		filepath.Join(path, "usr", "share", "zignos", "recipes"),
	} {
		if isRecipeTree(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: no recipe tree (core/ + main/, or stable|testing|unstable with core/ + main/) found", path)
}

// findRecipeTreeDeep handles archive roots with an arbitrary top-level
// directory (for example a GitHub "repo-main" tarball).
func findRecipeTreeDeep(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		if rel != "." && strings.Count(rel, string(os.PathSeparator)) > 5 {
			return filepath.SkipDir
		}
		if isRecipeTree(path) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s: no recipe tree (core/ + main/, or stable|testing|unstable with core/ + main/) found", root)
	}
	return found, nil
}

// countCategories counts recipe directories (those with a version file).
func countCategories(root string) (categoryCounts, error) {
	var c categoryCounts
	for _, cat := range []string{"core", "main", "desktop"} {
		dir := filepath.Join(root, cat)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) && cat == "desktop" {
			continue
		}
		if err != nil {
			return c, fmt.Errorf("%s: %w", dir, err)
		}
		n := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "version")); err == nil {
				n++
			}
		}
		switch cat {
		case "core":
			c.core = n
		case "main":
			c.main = n
		case "desktop":
			c.desktop = n
		}
	}
	return c, nil
}

// scanRecipeTree validates a channel tree or a legacy flat tree and counts
// recipes per category.
func scanRecipeTree(root string) (syncStats, error) {
	flat := hasCategories(root)
	var chans []string
	for _, ch := range config.Channels {
		if fi, err := os.Stat(filepath.Join(root, ch)); err == nil && fi.IsDir() {
			chans = append(chans, ch)
		}
	}
	if flat && len(chans) > 0 {
		return syncStats{}, fmt.Errorf("%s: mixed recipe tree layout (both core/ and %s/ at the top)", root, chans[0])
	}
	if len(chans) == 0 {
		if !flat {
			return syncStats{}, fmt.Errorf("%s: no recipe tree (core/ + main/, or stable|testing|unstable with core/ + main/)", root)
		}
		c, err := countCategories(root)
		if err != nil {
			return syncStats{}, err
		}
		if c.core == 0 || c.main == 0 {
			return syncStats{}, fmt.Errorf("%s: incomplete recipe tree (core %d, main %d)", root, c.core, c.main)
		}
		return syncStats{channels: map[string]categoryCounts{"legacy": c}, legacy: true}, nil
	}
	stats := syncStats{channels: map[string]categoryCounts{}}
	for _, ch := range chans {
		chRoot := filepath.Join(root, ch)
		if !hasCategories(chRoot) {
			return syncStats{}, fmt.Errorf("%s: channel %s needs core/ and main/", root, ch)
		}
		c, err := countCategories(chRoot)
		if err != nil {
			return syncStats{}, err
		}
		if c.core == 0 || c.main == 0 {
			return syncStats{}, fmt.Errorf("%s: channel %s is incomplete (core %d, main %d)", root, ch, c.core, c.main)
		}
		stats.channels[ch] = c
	}
	return stats, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode().Perm())
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// tarArgs keeps the Windows tar invocation local and slash-separated, the
// same workaround used by source.Extract.
func tarArgs(archive string, rest ...string) []string {
	args := []string{}
	if runtime.GOOS == "windows" {
		args = append(args, "--force-local")
		archive = filepath.ToSlash(archive)
		rest = append([]string{}, rest...)
		for i := range rest {
			rest[i] = filepath.ToSlash(rest[i])
		}
	}
	return append(append(args, "-f", archive), rest...)
}

// recipePrefixInArchive finds the top-level directory that contains
// core/<pkg>/version. It lets a full stage3 tarball be treated like a
// recipe-only archive without unpacking the whole root filesystem.
func recipePrefixInArchive(archive string) (string, error) {
	cmd := exec.Command("tar", tarArgs(archive, "-t")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	prefix := ""
	found := false
	for scanner.Scan() {
		if p, ok := recipePrefix(scanner.Text()); ok {
			prefix, found = p, true
			break
		}
	}
	if found || scanner.Err() == nil {
		// A matching member may be followed by tens of thousands of
		// entries in a stage3 tarball; stop tar instead of reading them all.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if !found {
		if scanner.Err() != nil {
			return "", scanner.Err()
		}
		if waitErr != nil {
			return "", fmt.Errorf("read archive %s: %w", archive, waitErr)
		}
		return "", fmt.Errorf("%s: no recipe tree (core/*/version or <channel>/core/*/version) in archive", archive)
	}
	return prefix, nil
}

func recipePrefix(name string) (string, bool) {
	raw := strings.TrimSuffix(filepath.ToSlash(name), "/")
	work := strings.TrimPrefix(raw, "./")
	parts := strings.Split(work, "/")
	for i := 0; i+2 < len(parts); i++ {
		if (parts[i] == "core" || parts[i] == "main") && parts[i+2] == "version" {
			root := i
			if i > 0 && config.ValidChannel(parts[i-1]) {
				// <tree>/<channel>/core/<pkg>/version: extract from the
				// tree (or channel) root, not just the category.
				root = i - 1
			}
			prefix := strings.Join(parts[:root], "/")
			// Keep the archive's "./" spelling so tar matches the
			// member directly; the fallback in extractRecipeArchive
			// covers archives written without it.
			if prefix != "" && strings.HasPrefix(raw, "./") {
				prefix = "./" + prefix
			}
			return prefix, true
		}
	}
	return "", false
}

func extractRecipeArchive(archive, dest string) error {
	prefix, err := recipePrefixInArchive(archive)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// GNU tar and bsdtar disagree about whether a member written as
	// "./usr/..." matches a requested "usr/..." (and vice versa). Try the
	// form reported by the listing, then the "./" form.
	candidates := []string{prefix}
	if prefix != "" {
		candidates = append(candidates, "./"+prefix)
	}
	var lastErr error
	for i, p := range candidates {
		if i > 0 {
			// A failed exact-member extraction can leave empty directories.
			_ = os.RemoveAll(dest)
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		}
		args := tarArgs(archive, "-x", "-C", dest)
		if p != "" {
			args = append(args, p)
		}
		cmd := exec.Command("tar", args...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("extract recipes from %s: %w", archive, lastErr)
}
