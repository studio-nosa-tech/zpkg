// Package build runs recipe build scripts into a destroot.
package build

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/manifest"
	"github.com/studio-nosa-tech/zpkg/internal/recipe"
	"github.com/studio-nosa-tech/zpkg/internal/source"
)

// pkgBase is the cache key for a recipe: name-version, plus -rN when the
// recipe has a revision, plus a short options hash when USE/CFLAGS/MAKEOPTS
// are set. Baking both in keeps builds with different inputs apart.
func pkgBase(r *recipe.Recipe, optsHash string) string {
	base := r.Name + "-" + r.Version
	if r.Revision > 0 {
		base += "-r" + strconv.Itoa(r.Revision)
	}
	if optsHash != "" {
		base += "-" + optsHash
	}
	return base
}

// Result describes one completed build.
type Result struct {
	Recipe   *recipe.Recipe
	Destroot string
	Entries  []manifest.Entry
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
func PackBinpkg(r *recipe.Recipe, res *Result, cacheDir, optsHash string) (string, error) {
	dir := filepath.Join(cacheDir, "binpkgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := pkgBase(r, optsHash)
	pkgPath := filepath.Join(dir, base+".pkg.tar.gz")

	f, err := os.Create(pkgPath)
	if err != nil {
		return "", err
	}
	gzw := gzip.NewWriter(f)
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

	if err := manifest.Write(filepath.Join(dir, base+".manifest"), res.Entries); err != nil {
		return "", err
	}
	return pkgPath, nil
}

// LoadBinpkg returns a Result restored from a cached binpkg, or nil when
// none exists (or ZPKG_NO_BINPKG=1 forces a source build).
func LoadBinpkg(r *recipe.Recipe, cfg config.Config) *Result {
	if os.Getenv("ZPKG_NO_BINPKG") == "1" {
		return nil
	}
	dir := filepath.Join(cfg.Cache, "binpkgs")
	base := pkgBase(r, cfg.Options.Hash())
	entries, err := manifest.Read(filepath.Join(dir, base+".manifest"))
	if err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, base+".pkg.tar.gz")); err != nil {
		return nil
	}
	tmp := filepath.Join(cfg.Cache, "build", r.Name, "binpkg")
	if err := os.RemoveAll(tmp); err != nil {
		return nil
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil
	}
	if err := source.Extract(filepath.Join(dir, base+".pkg.tar.gz"), tmp); err != nil {
		return nil
	}
	return &Result{Recipe: r, Destroot: tmp, Entries: entries}
}

// Run fetches, verifies and extracts sources, then executes the build script
// with ZPKG_DESTROOT pointing at a fresh destroot. On success it returns the
// destroot inventory.
func Run(r *recipe.Recipe, cfg config.Config) (*Result, error) {
	work := filepath.Join(cfg.Cache, "build", r.Name)
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

	if len(r.Sources) > 0 {
		var files []string
		for _, u := range r.Sources {
			f, err := source.Fetch(u, cfg.Cache)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
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

	cmd := exec.Command("bash", r.Build)
	cmd.Dir = src
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"ZPKG_NAME="+r.Name,
		"ZPKG_VERSION="+r.Version,
		"ZPKG_RECIPE_DIR="+r.Dir,
		"ZPKG_ROOT="+cfg.Root,
		"ZPKG_CACHE="+cfg.Cache,
		"ZPKG_DESTROOT="+destroot,
		"ZPKG_SRC="+src,
		"ZPKG_WORKDIR="+work,
		"ZPKG_USE="+strings.Join(cfg.Options.Use, " "),
		"ZPKG_CFLAGS="+cfg.Options.CFlags,
		"ZPKG_MAKEOPTS="+cfg.Options.MakeOpts,
	)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: build script failed: %w", r.Name, err)
	}

	entries, err := manifest.FromRoot(destroot)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: build script produced no files in destroot", r.Name)
	}
	return &Result{Recipe: r, Destroot: destroot, Entries: entries}, nil
}
