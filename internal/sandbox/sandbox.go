// Package sandbox builds the bubblewrap command line used to isolate recipe
// build scripts. The sandbox exposes the installed system read-only, gives
// the package's work directory read-write access, provides fresh /proc and
// /dev, and drops all capabilities except the small set recipes need.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Mode is the configured sandbox policy.
type Mode string

const (
	// ModeAuto uses bubblewrap when available and otherwise builds directly
	// with a warning.
	ModeAuto Mode = "auto"
	// ModeRequire refuses to build without bubblewrap.
	ModeRequire Mode = "require"
	// ModeOff always builds directly.
	ModeOff Mode = "off"
)

// ParseMode validates a SANDBOX / ZPKG_SANDBOX value. Empty means auto.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeAuto):
		return ModeAuto, nil
	case string(ModeRequire):
		return ModeRequire, nil
	case string(ModeOff):
		return ModeOff, nil
	default:
		return "", fmt.Errorf("invalid sandbox mode %q (use auto, require or off)", s)
	}
}

// Resolve locates bwrap according to mode. It returns the executable path and
// whether a sandbox will be used. A missing bwrap is only an error in
// require mode; auto callers decide whether to warn.
func Resolve(mode Mode, bwrapPath string) (string, bool, error) {
	if mode == ModeOff {
		return "", false, nil
	}
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}
	p, err := exec.LookPath(bwrapPath)
	if err == nil {
		return p, true, nil
	}
	if mode == ModeRequire {
		return "", false, fmt.Errorf("bubblewrap (bwrap) not found: install bubblewrap or set ZPKG_SANDBOX=off")
	}
	return "", false, nil
}

// Check verifies that bwrap can actually create a sandbox in this
// environment. A plain chroot is not pivot_root-able, so bwrap exists but
// fails at startup; callers in auto mode can then fall back with a warning.
func Check(bwrapPath string) error {
	cmd := exec.Command(bwrapPath, "--ro-bind", "/", "/", "--", "/bin/true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("bubblewrap cannot create a sandbox: %w (%s)", err, msg)
		}
		return fmt.Errorf("bubblewrap cannot create a sandbox: %w", err)
	}
	return nil
}

// Options describes one isolated build.
type Options struct {
	BwrapPath   string
	WorkDir     string // read-write inside the sandbox
	SourceDir   string
	RecipeDir   string // read-only, re-bound in case /home or /root is masked
	CacheDir    string // read-only, same reason
	BuildScript string
	Env         []string // KEY=VALUE entries passed with --setenv
	Network     bool
	ProbeRoot   string // test hook: stat masks under this prefix instead of /
}

func exists(probeRoot, p string) bool {
	full := p
	if probeRoot != "" {
		full = filepath.Join(probeRoot, strings.TrimPrefix(p, "/"))
	}
	_, err := os.Lstat(full)
	return err == nil
}

// Args returns the bubblewrap argv (without the bwrap executable itself).
func Args(o Options) []string {
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--hostname", "zignos-build",
	}
	if !o.Network {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--clearenv")
	for _, kv := range o.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		args = append(args, "--setenv", k, v)
	}

	// The whole installed system is visible but immutable.
	args = append(args, "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev")

	// Private writable scratch space. /var/lib also hides the package DB and
	// service state; /home and /root hide user secrets.
	for _, d := range []string{
		"/tmp", "/run", "/var/tmp", "/var/lib", "/var/log", "/var/spool",
		"/root", "/home", "/mnt", "/media", "/srv",
	} {
		if exists(o.ProbeRoot, d) {
			args = append(args, "--tmpfs", d)
		}
	}
	for _, d := range []string{
		"/etc/ssh", "/etc/ssl/private", "/etc/zpkg", "/etc/pki",
		"/etc/security", "/etc/NetworkManager/system-connections",
		"/etc/wpa_supplicant",
	} {
		if exists(o.ProbeRoot, d) {
			args = append(args, "--tmpfs", d)
		}
	}
	for _, f := range []string{
		"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/doas.conf",
		"/etc/machine-id",
	} {
		if exists(o.ProbeRoot, f) {
			args = append(args, "--ro-bind", "/dev/null", f)
		}
	}

	// Re-expose the recipe and cache if they live under a masked directory
	// (for example a repository in /home). They stay read-only.
	if o.RecipeDir != "" {
		args = append(args, "--ro-bind", o.RecipeDir, o.RecipeDir)
	}
	if o.CacheDir != "" && o.CacheDir != o.WorkDir {
		args = append(args, "--ro-bind", o.CacheDir, o.CacheDir)
	}
	args = append(args, "--bind", o.WorkDir, o.WorkDir)
	args = append(args, "--chdir", o.SourceDir)

	// Keep only what package builds need: ownership for doas/sudo, setuid
	// bits, and chroot for zignos-mkinitramfs. No CAP_SYS_ADMIN, so the
	// read-only root cannot be remounted.
	args = append(args,
		"--cap-drop", "ALL",
		"--cap-add", "CAP_CHOWN",
		"--cap-add", "CAP_FOWNER",
		"--cap-add", "CAP_DAC_OVERRIDE",
		"--cap-add", "CAP_FSETID",
		"--cap-add", "CAP_SYS_CHROOT",
	)
	args = append(args, "--", "/bin/bash", o.BuildScript)
	return args
}

// EnvOptions describes the sanitized environment for a build.
type EnvOptions struct {
	Root      string // target root; "/" inside a sandbox
	WorkDir   string
	CacheDir  string
	Destroot  string
	SourceDir string
	RecipeDir string
	Name      string
	Version   string
	Use       string
	CFlags    string
	MakeOpts  string
	Network   bool
	Sandboxed bool
}

// Env builds the minimal environment passed to the build. Host secrets and
// compiler settings are not inherited; proxy variables are the one exception
// because network-enabled builds may need them.
func Env(o EnvOptions) []string {
	env := []string{
		"PATH=/usr/bin:/usr/sbin:/bin:/sbin",
		"HOME=" + path.Join(o.WorkDir, "home"),
		"TMPDIR=/tmp",
		"SHELL=/bin/bash",
		"TERM=dumb",
		"USER=root",
		"LOGNAME=root",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"GOCACHE=" + path.Join(o.WorkDir, "gocache"),
		"GOPATH=" + path.Join(o.WorkDir, "gopath"),
		"GOTOOLCHAIN=local",
		"ZPKG_NAME=" + o.Name,
		"ZPKG_VERSION=" + o.Version,
		"ZPKG_RECIPE_DIR=" + o.RecipeDir,
		"ZPKG_ROOT=" + o.Root,
		"ZPKG_CACHE=" + o.CacheDir,
		"ZPKG_DESTROOT=" + o.Destroot,
		"ZPKG_SRC=" + o.SourceDir,
		"ZPKG_WORKDIR=" + o.WorkDir,
		"ZPKG_USE=" + o.Use,
		"ZPKG_CFLAGS=" + o.CFlags,
		"ZPKG_MAKEOPTS=" + o.MakeOpts,
	}
	if o.Sandboxed {
		env = append(env, "ZPKG_SANDBOX=bwrap")
	} else {
		env = append(env, "ZPKG_SANDBOX=none")
	}
	if o.Network {
		env = append(env, "ZPKG_BUILD_NET=1")
	} else {
		env = append(env, "ZPKG_BUILD_NET=0", "GOPROXY=off")
	}
	for _, k := range []string{
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// PrepareWork creates the writable HOME/GOCACHE/GOPATH directories.
func PrepareWork(work string) error {
	for _, d := range []string{"home", "gocache", "gopath"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o755); err != nil {
			return err
		}
	}
	return nil
}
