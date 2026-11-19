package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/options"
	"github.com/studio-nosa-tech/zpkg/internal/pkgdb"
	"github.com/studio-nosa-tech/zpkg/internal/upgrade"
)

const channelUsage = `Usage:
  zpkg channel                 show the effective channel and available trees
  zpkg channel <name>          set CHANNEL= in /etc/zpkg/make.conf
                               (stable, testing or unstable)
`

// cmdChannel shows or persists the release channel. It loads the config
// leniently so an invalid value can be repaired with this command.
func cmdChannel(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(channelUsage)
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: zpkg channel [stable|testing|unstable]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	makeConf := filepath.Join(cfg.Root, "etc", "zpkg", "make.conf")
	if len(args) == 1 {
		ch := strings.ToLower(strings.TrimSpace(args[0]))
		if !config.ValidChannel(ch) {
			return fmt.Errorf("invalid release channel %q (use stable, testing or unstable)", args[0])
		}
		if err := options.SetVar(makeConf, "CHANNEL", ch); err != nil {
			return err
		}
		fmt.Printf("Channel set to %s in %s\n", ch, makeConf)
		if !isInstalledChannel(cfg.RecipeRoot, ch) {
			fmt.Fprintf(os.Stderr, "zpkg: warning: channel %s is not installed under %s (run \"zpkg sync\")\n", ch, cfg.RecipeRoot)
		}
		return nil
	}

	fmt.Printf("channel: %s (%s)\n", cfg.Channel, channelSourceLabel(cfg.ChannelSource))
	if prev := upgrade.ReadAppliedChannel(pkgdb.DB{Dir: cfg.DB}); prev != "" {
		fmt.Printf("last applied: %s\n", prev)
	}
	available := config.AvailableChannels(cfg.RecipeRoot)
	if len(available) > 0 {
		fmt.Printf("installed: %s\n", strings.Join(available, ", "))
	} else if _, err := os.Stat(filepath.Join(cfg.RecipeRoot, "core")); err == nil {
		fmt.Printf("installed: stable (legacy flat tree)\n")
	} else {
		fmt.Println("installed: none")
	}
	fmt.Println("set with: zpkg channel stable|testing|unstable")
	return nil
}

func isInstalledChannel(recipeRoot, ch string) bool {
	_, err := os.Stat(filepath.Join(recipeRoot, ch, "core"))
	return err == nil
}

func channelSourceLabel(src string) string {
	switch src {
	case "env":
		return "ZPKG_CHANNEL"
	case "make.conf":
		return "/etc/zpkg/make.conf"
	case "flag":
		return "--channel"
	default:
		return "default"
	}
}
