package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/options"
)

// cmdUse shows or edits USE flags.
//
//	zpkg use                          global flags
//	zpkg use foo -bar                 add foo, remove bar (global)
//	zpkg use --package vim            effective flags for vim
//	zpkg use --package vim gtk -X     edit vim's line in package.use
func cmdUse(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var pkg string
	var flags []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--package":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: zpkg use --package <pkg> [flag|-flag]...")
			}
			i++
			pkg = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return fmt.Errorf("unknown option %q", args[i])
			}
			flags = append(flags, args[i])
		}
	}

	if pkg == "" {
		if len(flags) == 0 {
			fmt.Printf("%s\n", strings.Join(cfg.Options.Use, " "))
			return nil
		}
		set := map[string]bool{}
		for _, f := range cfg.Options.Use {
			set[f] = true
		}
		for _, f := range flags {
			applyFlag(set, f)
		}
		if err := options.Save(cfg.Root, sortedKeys(set)); err != nil {
			return err
		}
		fmt.Printf("USE: %s\n", strings.Join(sortedKeys(set), " "))
		return nil
	}

	if len(flags) == 0 {
		eff := cfg.Options.ForPackage(pkg)
		fmt.Printf("%s: %s\n", pkg, strings.Join(eff.Use, " "))
		if tok := cfg.Options.PackageTokens(pkg); len(tok) > 0 {
			fmt.Printf("tokens: %s\n", strings.Join(tok, " "))
		}
		return nil
	}

	// Edit the package's token list (enable "flag", disable "-flag").
	tokens := map[string]string{}
	for _, t := range cfg.Options.PackageTokens(pkg) {
		tokens[strings.TrimPrefix(t, "-")] = t
	}
	for _, f := range flags {
		tokens[strings.TrimPrefix(f, "-")] = f
	}
	names := make([]string, 0, len(tokens))
	for n := range tokens {
		names = append(names, n)
	}
	sort.Strings(names)
	vals := make([]string, 0, len(names))
	for _, n := range names {
		vals = append(vals, tokens[n])
	}
	if err := options.SavePackageUse(cfg.Root, pkg, vals); err != nil {
		return err
	}
	o, err := options.Load(cfg.Root)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", pkg, strings.Join(o.ForPackage(pkg).Use, " "))
	return nil
}

func applyFlag(set map[string]bool, f string) {
	f = strings.TrimSpace(f)
	if f == "" {
		return
	}
	if strings.HasPrefix(f, "-") {
		delete(set, strings.TrimPrefix(f, "-"))
		return
	}
	set[f] = true
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
