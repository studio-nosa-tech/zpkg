package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studio-nosa-tech/zpkg/internal/config"
)

func TestParseUpgradeArgs(t *testing.T) {
	o, err := parseUpgradeArgs([]string{"-p", "--newuse", "--prune", "--allow-downgrade",
		"--verify", "--channel", "testing", "foo", "bar"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.pretend || !o.newuse || !o.prune || !o.allowDowngrade || !o.verify {
		t.Fatalf("flags = %+v", o)
	}
	if o.channel != "testing" || len(o.targets) != 2 || o.targets[0] != "foo" {
		t.Fatalf("args = %+v", o)
	}
	if _, err := parseUpgradeArgs([]string{"--channel"}); err == nil {
		t.Fatal("missing --channel argument accepted")
	}
	if _, err := parseUpgradeArgs([]string{"--bogus"}); err == nil {
		t.Fatal("unknown option accepted")
	}
}

func TestChannelCommandSetsMakeConf(t *testing.T) {
	root := t.TempDir()
	for _, ch := range []string{"stable", "testing"} {
		if err := os.MkdirAll(filepath.Join(root, "usr", "share", "zignos", "recipes", ch, "core"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "usr", "share", "zignos", "recipes", ch, "main"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	confDir := filepath.Join(root, "etc", "zpkg")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(confDir, "make.conf")
	if err := os.WriteFile(conf, []byte("# flags\nCFLAGS=-O2\nCHANNEL=stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZPKG_ROOT", root)
	t.Setenv("ZPKG_REPOS", "")
	t.Setenv("ZPKG_CHANNEL", "")

	if err := cmdChannel([]string{"testing"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "CHANNEL=testing") || !strings.Contains(text, "CFLAGS=-O2") || !strings.Contains(text, "# flags") {
		t.Fatalf("make.conf = %q", text)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel != "testing" || len(cfg.Repos) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if err := cmdChannel([]string{"nightly"}); err == nil {
		t.Fatal("invalid channel accepted")
	}
	if err := cmdChannel(nil); err != nil {
		t.Fatal(err)
	}
}
