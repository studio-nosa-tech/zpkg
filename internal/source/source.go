// Package source fetches, verifies and extracts recipe sources.
package source

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/studio-nosa-tech/zpkg/internal/hash"
)

// Fetch returns the cached file path for a source URL, downloading it on
// first use.
func Fetch(rawurl, cacheDir string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", fmt.Errorf("bad source URL %q: %w", rawurl, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported source scheme %q in %q", u.Scheme, rawurl)
	}
	name := filepath.Base(u.Path)
	if name == "" || name == "/" || name == "." {
		return "", fmt.Errorf("cannot derive file name from %q", rawurl)
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

func download(rawurl, dst string) error {
	resp, err := http.Get(rawurl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
// compressed formats (gz, bz2, xz, ...).
func Extract(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	args := []string{}
	if runtime.GOOS == "windows" {
		// GNU tar reads "D:/..." as a remote archive ("Cannot connect to D:");
		// --force-local keeps it local on a Windows dev host. It must precede
		// -f, which consumes the next argument as the archive name. Paths are
		// converted to forward slashes because a native process spawning the
		// MSYS tar mangles backslash paths. Linux, the real target, never
		// needs any of this.
		args = append(args, "--force-local")
		archive = filepath.ToSlash(archive)
		dest = filepath.ToSlash(dest)
	}
	args = append(args, "-xf", archive, "-C", dest)
	cmd := exec.Command("tar", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
