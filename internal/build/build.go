// Package build runs recipe build scripts into a destroot.
package build

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/hash"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/pkgid"
	"github.com/studio-nosa-tech/zpkg/internal/provenance"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/sandbox"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

// PackageBase is the cache key for a recipe: package key (name or
// name@slot), version, -rN when the recipe has a revision, and a short
// options hash when USE/CFLAGS/MAKEOPTS (and, for video recipes, VIDEO_CARDS)
// are set. Baking all of them in keeps builds with different inputs and
// different slots apart. Slot 0 keeps the legacy name-version spelling.
func PackageBase(r *recipe.Recipe, optsHash string) string {
	base := pkgid.Key(r.Name, r.Slot) + "-" + r.Version
	if r.Revision > 0 {
		base += "-r" + strconv.Itoa(r.Revision)
	}
	if optsHash != "" {
		base += "-" + optsHash
	}
	return base
}

// OptionsHash returns the effective build options hash for r. VIDEO_CARDS is
// included only for recipes that declare a video_cards file, so changing the
// GPU selection does not invalidate every other cached package.
func OptionsHash(cfg config.Config, r *recipe.Recipe) string {
	return cfg.Options.ForPackage(r.Name).HashFor(r.UsesVideoCards())
}

// Result describes one completed build.
type Result struct {
	Recipe   *recipe.Recipe
	Destroot string
	Entries  []manifest.Entry

	// Provenance of the artifact that was used. OriginKind is "source",
	// "binpkg" (local cache) or "binrepo".
	OriginKind        string
	OriginRepo        string
	OriginKeyID       string
	OriginGeneration  int64
	OriginIndexSHA256 string
	OriginPkgSHA256   string

	// Build environment provenance for source builds.
	Sandbox       string // "bwrap" or "none"
	Network       bool
	BuildEnvHash  string
	BuildEnvCount int

	// Reproducibility inputs and outputs for source builds.
	RecipeDigest    string
	Sources         []provenance.Source
	SourceDateEpoch int64
	Started         string // RFC3339, UTC
	Finished        string
	LogPath         string
	LogSHA256       string
	ArtifactPath    string
	ArtifactSHA256  string
	ArtifactSize    int64
	ManifestSHA256  string
}

const workPackageMarker = ".zpkg-package"

// CleanWork removes the build work directory (sources, destroot, HOME and
// compiler caches) for one package.
func CleanWork(r *recipe.Recipe, cfg config.Config) error {
	return os.RemoveAll(filepath.Join(cfg.Cache, "build", PackageBase(r, OptionsHash(cfg, r))))
}

// CleanOldWork removes work directories for older builds of the same package.
// The current build is kept: its destroot is still needed for installation.
// Directories are identified by a marker file, so names containing hyphens
// cannot collide with another package.
func CleanOldWork(r *recipe.Recipe, cfg config.Config) error {
	root := filepath.Join(cfg.Cache, "build")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	current := PackageBase(r, OptionsHash(cfg, r))
	for _, e := range entries {
		if !e.IsDir() || e.Name() == current {
			continue
		}
		dir := filepath.Join(root, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, workPackageMarker))
		if err != nil || strings.TrimSpace(string(data)) != pkgid.Key(r.Name, r.Slot) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// CleanOldBinpkgs removes binary packages for older builds of the same
// package. The sidecar .meta.json is authoritative for the package name.
func CleanOldBinpkgs(r *recipe.Recipe, cfg config.Config) error {
	root := filepath.Join(cfg.Cache, "binpkgs")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	current := PackageBase(r, OptionsHash(cfg, r))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".meta.json")
		if base == current {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		var m BinMeta
		if json.Unmarshal(data, &m) != nil || pkgid.Key(m.Name, m.Slot) != pkgid.Key(r.Name, r.Slot) {
			continue
		}
		if err := removeBinpkgFiles(root, base); err != nil {
			return err
		}
	}
	return nil
}

// RemoveCurrentBinpkg removes the binary package for the current recipe.
// It is used when --repro-check forced a pack but BUILD_BINPKG is disabled.
func RemoveCurrentBinpkg(r *recipe.Recipe, cfg config.Config) error {
	return removeBinpkgFiles(filepath.Join(cfg.Cache, "binpkgs"),
		PackageBase(r, OptionsHash(cfg, r)))
}

func removeBinpkgFiles(root, base string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base+".") {
			continue
		}
		if err := os.Remove(filepath.Join(root, e.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RemoveSourceArchives deletes the source tarballs used by one build.
// Non-archive sources (patches, PEM bundles) are kept.
func RemoveSourceArchives(res *Result, cacheDir string) error {
	for _, s := range res.Sources {
		if !isArchive(s.File) {
			continue
		}
		p := filepath.Join(cacheDir, filepath.Base(s.File))
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// isArchive reports whether a source file looks like a tar archive by
// extension. Everything else (patches, PEM bundles, plain text) is copied
// into the source root for the build script.
func isArchive(name string) bool {
	for _, ext := range []string{
		".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".tar.zst", ".tzst",
	} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// PackBinpkg writes a binary package — a gzipped tar of the destroot — and
// a sidecar manifest into cacheDir/binpkgs, named <name>-<version>. This is
// the local equivalent of Portage's buildpkg output.
func PackBinpkg(r *recipe.Recipe, res *Result, cacheDir, optsHash, channel string) (string, error) {
	dir := filepath.Join(cacheDir, "binpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := PackageBase(r, optsHash)
	pkgPath := filepath.Join(dir, base+".pkg.tar.gz")

	epoch := res.SourceDateEpoch
	if epoch <= 0 {
		epoch = provenance.DefaultSourceDateEpoch
	}
	epochTime := time.Unix(epoch, 0).UTC()

	f, err := os.Create(pkgPath)
	if err != nil {
		return "", err
	}
	gzw := gzip.NewWriter(f)
	// The gzip header otherwise embeds the current time.
	gzw.Header.ModTime = epochTime
	tw := tar.NewWriter(gzw)

	for _, e := range res.Entries {
		hdr := &tar.Header{Name: e.Path}
		switch e.Kind {
		case "dir":
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		case "link":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.Link
		default:
			full := filepath.Join(res.Destroot, filepath.FromSlash(e.Path))
			info, err := os.Stat(full)
			if err != nil {
				return "", err
			}
			// FileInfoHeader maps the Go mode to raw tar mode bits,
			// including setuid/setgid/sticky.
			var hdrErr error
			hdr, hdrErr = tar.FileInfoHeader(info, "")
			if hdrErr != nil {
				return "", hdrErr
			}
			hdr.Name = e.Path
			hdr.Typeflag = tar.TypeReg
		}
		// Normalize timestamps; ownership, modes, contents and order stay
		// exactly as the build produced them.
		hdr.ModTime = epochTime
		hdr.AccessTime = time.Time{}
		hdr.ChangeTime = time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeReg {
			in, err := os.Open(filepath.Join(res.Destroot, filepath.FromSlash(e.Path)))
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(tw, in); err != nil {
				in.Close()
				return "", err
			}
			in.Close()
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gzw.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	manifestPath := filepath.Join(dir, base+".manifest")
	if err := manifest.Write(manifestPath, res.Entries); err != nil {
		return "", err
	}
	if err := writeBinMeta(dir, base, r, optsHash, channel, res); err != nil {
		return "", err
	}

	// Record the artifact digests for provenance and SBOM generation.
	info, err := os.Stat(pkgPath)
	if err != nil {
		return "", err
	}
	sum, err := hash.File(pkgPath)
	if err != nil {
		return "", err
	}
	manifestSum, err := hash.File(manifestPath)
	if err != nil {
		return "", err
	}
	res.ArtifactPath = pkgPath
	res.ArtifactSHA256 = sum
	res.ArtifactSize = info.Size()
	res.ManifestSHA256 = manifestSum
	return pkgPath, nil
}

// LoadBinpkg returns a Result restored from a cached binpkg, or nil when
// none exists (or ZPKG_NO_BINPKG=1 forces a source build).
func LoadBinpkg(r *recipe.Recipe, cfg config.Config) *Result {
	if os.Getenv("ZPKG_NO_BINPKG") == "1" {
		return nil
	}
	dir := filepath.Join(cfg.Cache, "binpkgs")
	base := PackageBase(r, OptionsHash(cfg, r))
	data, err := os.ReadFile(filepath.Join(dir, base+".meta.json"))
	if err != nil {
		return nil
	}
	var m BinMeta
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	if pkgid.Key(m.Name, m.Slot) != pkgid.Key(r.Name, r.Slot) {
		return nil
	}
	// The cache key has no channel or recipe-content component. A binpkg is
	// only valid for the exact recipe that produced it; otherwise a channel
	// switch could silently install another channel's build (for example an
	// LTS backport that kept the same version).
	if want := recipeDigestFor(r); want != "" && m.RecipeDigest != want {
		return nil
	}
	// The recipe digest cannot see dependency versions, so the same recipe
	// built on two channels (Qt 6.11 vs 6.12, for example) produces different
	// binaries under the same cache key. Only reuse a binpkg from the
	// selected channel. Legacy entries without a channel predate channel
	// separation and belong to the stable tree.
	if m.Channel != cfg.Channel && !(m.Channel == "" && cfg.Channel == "stable") {
		return nil
	}
	entries, err := manifest.Read(filepath.Join(dir, base+".manifest"))
	if err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, base+".pkg.tar.gz")); err != nil {
		return nil
	}
	tmp := filepath.Join(cfg.Cache, "build", base, "binpkg")
	if err := os.RemoveAll(tmp); err != nil {
		return nil
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil
	}
	if err := source.Extract(filepath.Join(dir, base+".pkg.tar.gz"), tmp); err != nil {
		return nil
	}
	sum, _ := hash.File(filepath.Join(dir, base+".pkg.tar.gz"))
	res := &Result{
		Recipe:          r,
		Destroot:        tmp,
		Entries:         entries,
		OriginKind:      "binpkg",
		OriginRepo:      dir,
		OriginPkgSHA256: sum,
		Sandbox:         m.Sandbox,
		Network:         m.Network == "on",
		BuildEnvHash:    m.BuildEnvHash,
		BuildEnvCount:   m.BuildEnvCount,
		RecipeDigest:    m.RecipeDigest,
		SourceDateEpoch: m.SourceDateEpoch,
		ArtifactSHA256:  m.ArtifactSHA256,
		ArtifactSize:    m.ArtifactSize,
		ManifestSHA256:  m.ManifestSHA256,
		LogSHA256:       m.LogSHA256,
	}
	if res.ArtifactSHA256 == "" {
		res.ArtifactSHA256 = sum
	}
	return res
}

// recipeDigestFor returns the content digest of a recipe tree, or "" when
// the recipe has no directory (unit tests) or cannot be read.
func recipeDigestFor(r *recipe.Recipe) string {
	if r == nil || r.Dir == "" {
		return ""
	}
	d, err := provenance.RecipeDigest(r.Dir)
	if err != nil {
		return ""
	}
	return d
}

// Run fetches, verifies and extracts sources, then executes the build script
// with ZPKG_DESTROOT pointing at a fresh destroot. On success it returns the
// destroot inventory. Build logs, source digests and artifact provenance are
// recorded when enabled.
func Run(r *recipe.Recipe, cfg config.Config) (*Result, error) {
	opts := cfg.Options.ForPackage(r.Name)
	optsHash := opts.HashFor(r.UsesVideoCards())
	base := PackageBase(r, optsHash)
	work := filepath.Join(cfg.Cache, "build", base)
	destroot := filepath.Join(work, "destroot")
	src := filepath.Join(work, "src")
	for _, d := range []string{destroot, src} {
		if err := os.RemoveAll(d); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	var sources []provenance.Source
	if len(r.Sources) > 0 {
		var files []string
		for i, u := range r.Sources {
			f, err := source.Fetch(u, cfg.Cache)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
			sum := ""
			if i < len(r.Checksums) {
				sum = r.Checksums[i]
			}
			size := int64(0)
			if fi, err := os.Stat(f); err == nil {
				size = fi.Size()
			}
			// The fragment is a cache-name hint, not part of the URL.
			if j := strings.IndexByte(u, '#'); j >= 0 {
				u = u[:j]
			}
			sources = append(sources, provenance.Source{
				URL: u, File: filepath.Base(f), SHA256: sum, Size: size,
			})
		}
		if err := source.Verify(files, r.Checksums); err != nil {
			return nil, err
		}
		for _, f := range files {
			// Non-archive sources (patches, PEM bundles, plain text) are
			// placed in the source root for the build script; only tar
			// archives are unpacked.
			if isArchive(f) {
				if err := source.Extract(f, src); err != nil {
					return nil, err
				}
			} else {
				if err := copyFile(f, filepath.Join(src, filepath.Base(f)), 0o644); err != nil {
					return nil, err
				}
			}
		}
	}

	network := cfg.BuildNetwork
	if r.BuildNetwork != nil {
		network = *r.BuildNetwork
	}
	bwrapPath, isolated, err := resolveBuildSandbox(r, cfg)
	if err != nil {
		return nil, err
	}
	if err := sandbox.PrepareWork(work); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(work, workPackageMarker), []byte(pkgid.Key(r.Name, r.Slot)+"\n"), 0o644); err != nil {
		return nil, err
	}
	env := sandbox.Env(sandbox.EnvOptions{
		Root:            cfg.Root,
		WorkDir:         work,
		CacheDir:        cfg.Cache,
		Destroot:        destroot,
		SourceDir:       src,
		RecipeDir:       r.Dir,
		Name:            r.Name,
		Slot:            r.Slot,
		Version:         r.Version,
		Use:             strings.Join(opts.Use, " "),
		VideoCards:      strings.Join(opts.VideoCards, " "),
		CFlags:          opts.CFlags,
		MakeOpts:        opts.MakeOpts,
		Network:         network,
		Sandboxed:       isolated,
		SourceDateEpoch: cfg.SourceDateEpoch,
	})

	digest, digestErr := provenance.RecipeDigest(r.Dir)
	if digestErr != nil {
		fmt.Fprintf(os.Stderr, "zpkg: warning: %s: recipe digest: %v\n", r.Name, digestErr)
	}
	started := time.Now().UTC()

	var cmd *exec.Cmd
	engine := "none"
	if isolated {
		engine = "bwrap"
		cmd = exec.Command(bwrapPath, sandbox.Args(sandbox.Options{
			BwrapPath:   bwrapPath,
			WorkDir:     work,
			SourceDir:   src,
			RecipeDir:   r.Dir,
			CacheDir:    cfg.Cache,
			BuildScript: r.Build,
			Env:         env,
			Network:     network,
		})...)
		// bwrap clears the environment for the child; keeping the bwrap
		// process itself bare avoids leaking host variables to it.
		cmd.Env = []string{}
	} else {
		script, args := sandbox.UmaskScript(r.Build)
		cmd = exec.Command(script, args...)
		cmd.Dir = src
		cmd.Env = env
	}

	logPath := ""
	var logFile *os.File
	if cfg.BuildLog {
		logDir := filepath.Join(cfg.Cache, "logs")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return nil, err
		}
		logPath = filepath.Join(logDir, base+".log")
		logFile, err = os.Create(logPath)
		if err != nil {
			return nil, err
		}
		netState := "off"
		if network {
			netState = "on"
		}
		fmt.Fprintf(logFile, "zpkg build log\npackage: %s\nrecipe: %s\nrecipe_digest: %s\nsource_date_epoch: %d\nsandbox: %s\nnetwork: %s\nstarted: %s\nbuild_script: %s\n---\n",
			base, r.Dir, digest, cfg.SourceDateEpoch, engine, netState,
			started.Format(time.RFC3339), r.Build)
	}
	if logFile != nil {
		mw := io.MultiWriter(os.Stdout, logFile)
		cmd.Stdout, cmd.Stderr = mw, mw
	} else {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	runErr := cmd.Run()
	if logFile != nil {
		if err := logFile.Close(); err != nil && runErr == nil {
			return nil, err
		}
	}
	if runErr != nil {
		hint := ""
		if logPath != "" {
			hint = " (build log: " + logPath + ")"
		}
		if isolated {
			return nil, fmt.Errorf("%s: sandboxed build failed: %w%s (set ZPKG_SANDBOX=off to build without isolation)", r.Name, runErr, hint)
		}
		return nil, fmt.Errorf("%s: build script failed: %w%s", r.Name, runErr, hint)
	}
	finished := time.Now().UTC()

	entries, err := manifest.FromRoot(destroot)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: build script produced no files in destroot", r.Name)
	}
	logSum := ""
	if logPath != "" {
		logSum, _ = hash.File(logPath)
	}
	return &Result{
		Recipe: r, Destroot: destroot, Entries: entries, OriginKind: "source",
		Sandbox: engine, Network: network,
		RecipeDigest:    digest,
		Sources:         sources,
		SourceDateEpoch: cfg.SourceDateEpoch,
		Started:         started.Format(time.RFC3339),
		Finished:        finished.Format(time.RFC3339),
		LogPath:         logPath,
		LogSHA256:       logSum,
	}, nil
}

// resolveBuildSandbox applies the sandbox policy. auto falls back to an
// unsandboxed build with a warning; require refuses to continue.
func resolveBuildSandbox(r *recipe.Recipe, cfg config.Config) (string, bool, error) {
	mode, err := sandbox.ParseMode(cfg.SandboxMode)
	if err != nil {
		return "", false, err
	}
	if runtime.GOOS != "linux" {
		if mode == sandbox.ModeRequire {
			return "", false, fmt.Errorf("build sandbox requires Linux")
		}
		return "", false, nil
	}
	if cfg.Root != "/" {
		if mode == sandbox.ModeRequire {
			return "", false, fmt.Errorf("build sandbox requires running inside the target root (ZPKG_ROOT=/); chroot into it or set ZPKG_SANDBOX=off")
		}
		if mode == sandbox.ModeAuto {
			fmt.Fprintf(os.Stderr, "zpkg: warning: build sandbox requires ZPKG_ROOT=/; building %s without sandbox\n", r.Name)
		}
		return "", false, nil
	}
	path, ok, err := sandbox.Resolve(mode, cfg.BwrapPath)
	if err != nil {
		return "", false, err
	}
	if ok {
		if checkErr := sandbox.Check(path); checkErr != nil {
			if mode == sandbox.ModeAuto {
				fmt.Fprintf(os.Stderr, "zpkg: warning: %v; building %s without sandbox (enter the chroot via zignos-enter.sh or set ZPKG_SANDBOX=off)\n", checkErr, r.Name)
				return "", false, nil
			}
			return "", false, checkErr
		}
	}
	if !ok && mode == sandbox.ModeAuto {
		fmt.Fprintf(os.Stderr, "zpkg: warning: bubblewrap not found; building %s without sandbox (install bubblewrap or set ZPKG_SANDBOX=require)\n", r.Name)
	}
	return path, ok, nil
}
