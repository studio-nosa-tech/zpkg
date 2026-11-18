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
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

const syncUsage = `Usage:
  zpkg sync [--from <dir|tarball|URL>] [--dest <dir>] [-n]

Replace the recipe tree as a whole. The source may be:
  - a stage3 tarball (only usr/share/zignos/recipes is extracted)
  - a recipe-only tarball/directory (core/ and main/ at the top)
  - a directory containing usr/share/zignos/recipes

--from is required unless ZPKG_RECIPE_URL or RECIPEURL in
/etc/zpkg/sync.conf is set. The new tree is validated (core and main),
built in a temporary directory next to the target and renamed into place,
so a failed sync leaves the old tree untouched.
`

type syncOptions struct {
	from   string
	dest   string
	dryRun bool
}

type syncStats struct {
	core    int
	main    int
	desktop int
}

func (s syncStats) total() int { return s.core + s.main + s.desktop }

func parseSyncArgs(args []string) (syncOptions, error) {
	var o syncOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" || a == "--dry-run":
			o.dryRun = true
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

	phase("Syncing recipes from %s", from)
	stats, err := syncRecipes(from, dest, cfg.Cache, o.dryRun)
	if err != nil {
		return err
	}
	extra := ""
	if stats.desktop > 0 {
		extra = fmt.Sprintf(", desktop %d", stats.desktop)
	}
	phase("Recipe tree: %d recipes (core %d, main %d%s)",
		stats.total(), stats.core, stats.main, extra)
	if o.dryRun {
		fmt.Printf("Would update %s\n", dest)
		return nil
	}
	phase("Updated %s", dest)
	return nil
}

// syncRecipes stages the source tree in a sibling temporary directory and
// swaps it with dest only after validation. A failed copy/extraction never
// touches the working tree.
func syncRecipes(from, dest, cacheDir string, dryRun bool) (syncStats, error) {
	parent := filepath.Dir(dest)
	tmp, err := os.MkdirTemp(parent, ".zignos-recipes-sync-")
	if err != nil {
		return syncStats{}, err
	}
	defer os.RemoveAll(tmp)

	staging := filepath.Join(tmp, "recipes")
	extractDir := filepath.Join(tmp, "extract")

	var srcRoot string
	if isHTTP(from) {
		phase("Downloading %s", from)
		archive, err := source.Fetch(from, cacheDir)
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

	if err := copyTree(srcRoot, staging); err != nil {
		return syncStats{}, err
	}
	stats, err := scanRecipeTree(staging)
	if err != nil {
		return stats, err
	}
	if dryRun {
		return stats, nil
	}

	// Move the old tree aside inside tmp (same filesystem), then install
	// the staged tree. The deferred RemoveAll cleans up the backup.
	backup := filepath.Join(tmp, "old")
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return stats, fmt.Errorf("move old recipe tree aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return stats, err
	}
	if err := os.Rename(staging, dest); err != nil {
		// Best effort: put the old tree back so the system stays usable.
		if _, statErr := os.Lstat(backup); statErr == nil {
			_ = os.Rename(backup, dest)
		}
		return stats, fmt.Errorf("install new recipe tree: %w", err)
	}
	return stats, nil
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isRecipeTree(dir string) bool {
	for _, cat := range []string{"core", "main"} {
		fi, err := os.Stat(filepath.Join(dir, cat))
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// findRecipeTree recognizes the two layouts users normally have: a recipe
// checkout (core/main at the root) and an unpacked stage3 (the tree under
// usr/share/zignos/recipes).
func findRecipeTree(path string) (string, error) {
	for _, candidate := range []string{
		path,
		filepath.Join(path, "usr", "share", "zignos", "recipes"),
	} {
		if isRecipeTree(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: no recipe tree (core/ and main/) found", path)
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
		return "", fmt.Errorf("%s: no recipe tree (core/ and main/) found", root)
	}
	return found, nil
}

// scanRecipeTree counts recipes (directories with a version file) and
// requires the two mandatory categories.
func scanRecipeTree(root string) (syncStats, error) {
	var s syncStats
	for _, cat := range []string{"core", "main", "desktop"} {
		dir := filepath.Join(root, cat)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) && cat == "desktop" {
			continue
		}
		if err != nil {
			return s, fmt.Errorf("%s: %w", dir, err)
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
			s.core = n
		case "main":
			s.main = n
		case "desktop":
			s.desktop = n
		}
	}
	if s.core == 0 || s.main == 0 {
		return s, fmt.Errorf("%s: incomplete recipe tree (core %d, main %d)", root, s.core, s.main)
	}
	return s, nil
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
		return "", fmt.Errorf("%s: no recipe tree (core/*/version) in archive", archive)
	}
	return prefix, nil
}

func recipePrefix(name string) (string, bool) {
	raw := strings.TrimSuffix(filepath.ToSlash(name), "/")
	work := strings.TrimPrefix(raw, "./")
	parts := strings.Split(work, "/")
	for i := 0; i+2 < len(parts); i++ {
		if (parts[i] == "core" || parts[i] == "main") && parts[i+2] == "version" {
			prefix := strings.Join(parts[:i], "/")
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
