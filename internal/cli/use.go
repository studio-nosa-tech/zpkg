package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/options"
)

// cmdUse shows the enabled USE flags, or edits them when flags are given.
// A leading '-' disables a flag. The change is written to <root>/etc/zpkg
// /use.conf and affects the next builds only.
func cmdUse(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Printf("%s\n", strings.Join(cfg.Options.Use, " "))
		return nil
	}

	set := map[string]bool{}
	for _, f := range cfg.Options.Use {
		set[f] = true
	}
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			delete(set, strings.TrimPrefix(a, "-"))
			continue
		}
		set[a] = true
	}
	flags := make([]string, 0, len(set))
	for f := range set {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	if err := options.Save(cfg.Root, flags); err != nil {
		return err
	}
	fmt.Printf("USE: %s\n", strings.Join(flags, " "))
	return nil
}
