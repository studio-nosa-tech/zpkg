// Package source fetches, verifies and extracts recipe sources.
package source

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
)

// NotFoundError means the server answered 404. Callers that look for an
// optional file (a detached signature, for example) can distinguish it from
// a network failure.
type NotFoundError struct{ URL string }

func (e *NotFoundError) Error() string { return e.URL + ": not found" }

// IsNotFound reports whether err is a 404 from a source download.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

func cacheName(u *url.URL) (string, error) {
	name := u.Fragment
	if name == "" {
		name = filepath.Base(u.Path)
	}
	if name == "" || name == "/" || name == "." {
		return "", fmt.Errorf("cannot derive file name from %q", u.String())
	}
	return name, nil
}

// Fetch returns the cached file path for a source URL, downloading it on
// first use. A URL fragment overrides the cache file name — GitHub tag
// archives have useless basenames like "0.64.1.tar.gz", so recipes write
// .../0.64.1.tar.gz#openrc-0.64.1.tar.gz.
func Fetch(rawurl, cacheDir string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", fmt.Errorf("bad source URL %q: %w", rawurl, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported source scheme %q in %q", u.Scheme, rawurl)
	}
	name, err := cacheName(u)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".part"
	if err := download(rawurl, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// FetchFresh always downloads rawurl, replacing the cached copy. Repository
// indexes, detached signatures and recipe tarballs must not be served from a
// stale cache: a new signed index has to be seen on the next command.
func FetchFresh(rawurl, cacheDir string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", fmt.Errorf("bad source URL %q: %w", rawurl, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported source scheme %q in %q", u.Scheme, rawurl)
	}
	name, err := cacheName(u)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, name)
	f, err := os.CreateTemp(cacheDir, "."+filepath.Base(name)+".part-")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := download(rawurl, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func download(rawurl, dst string) error {
	resp, err := http.Get(rawurl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{URL: rawurl}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", rawurl, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// Verify checks each file's sha256 against the expected hex sums, by index.
func Verify(files, sums []string) error {
	for i, want := range sums {
		got, err := hash.File(files[i])
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: sha256 mismatch (want %s, got %s)", filepath.Base(files[i]), want, got)
		}
	}
	return nil
}

// Extract unpacks an archive into dest with tar, which also covers the
// compressed formats (gz, bz2, xz, ...). zstd archives are decompressed
// separately with a large window: LLVM's official tarball uses a 1 GiB
// window, which zstd's default 128 MiB decoder limit rejects.
func Extract(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// GNU tar reads "D:/..." as a remote archive ("Cannot connect to D:");
		// --force-local keeps it local on a Windows dev host. It must precede
		// -f, which consumes the next argument as the archive name. Paths are
		// converted to forward slashes because a native process spawning the
		// MSYS tar mangles backslash paths. Linux, the real target, never
		// needs any of this.
		archive = filepath.ToSlash(archive)
		dest = filepath.ToSlash(dest)
	}
	if isZstd(archive) {
		return extractZstd(archive, dest)
	}

	args := []string{}
	if runtime.GOOS == "windows" {
		args = append(args, "--force-local")
	}
	args = append(args, "-xf", archive, "-C", dest)
	cmd := exec.Command("tar", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isZstd(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.zst") || strings.HasSuffix(lower, ".tzst")
}

func extractZstd(archive, dest string) error {
	zstd := exec.Command("zstd", "-dc", "--long=30", "--memory=1024MB", archive)
	zstd.Stderr = os.Stderr
	out, err := zstd.StdoutPipe()
	if err != nil {
		return err
	}

	args := []string{}
	if runtime.GOOS == "windows" {
		args = append(args, "--force-local")
	}
	args = append(args, "-xf", "-", "-C", dest)
	tarCmd := exec.Command("tar", args...)
	tarCmd.Stdin = out
	tarCmd.Stderr = os.Stderr

	if err := zstd.Start(); err != nil {
		return fmt.Errorf("zstd: %w (install zstd to extract %s)", err, filepath.Base(archive))
	}
	if err := tarCmd.Start(); err != nil {
		_ = zstd.Process.Kill()
		_ = zstd.Wait()
		return err
	}
	tarErr := tarCmd.Wait()
	zstdErr := zstd.Wait()
	if tarErr != nil {
		return tarErr
	}
	return zstdErr
}
