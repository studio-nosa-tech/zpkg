package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/studio-nosa-tech/zpkg/internal/build"
	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/recipesig"
	"github.com/studio-nosa-tech/zpkg/internal/sign"
)

const keyUsage = `Usage:
  zpkg key generate --private <path> [--public <path>]
  zpkg key import <pubkey>...
  zpkg key list
  zpkg key remove <keyid|prefix>
`

const signUsage = `Usage:
  zpkg sign --key <private> <file>...
  zpkg verify <file>...
  zpkg sign-recipes --key <private> [--generation N] [--expires RFC3339|90d] [dir]
`

func cmdKey(args []string) error {
	if len(args) == 0 {
		fmt.Print(keyUsage)
		return fmt.Errorf("key: missing subcommand")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(keyUsage)
		return nil
	}
	switch args[0] {
	case "generate":
		return cmdKeyGenerate(args[1:])
	case "import":
		return cmdKeyImport(args[1:])
	case "list":
		return cmdKeyList(args[1:])
	case "remove":
		return cmdKeyRemove(args[1:])
	default:
		return fmt.Errorf("unknown key subcommand %q", args[0])
	}
}

func cmdKeyGenerate(args []string) error {
	var priv, pub string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--private" || strings.HasPrefix(a, "--private="):
			v, err := optValue(args, &i, "--private")
			if err != nil {
				return err
			}
			priv = v
		case a == "--public" || strings.HasPrefix(a, "--public="):
			v, err := optValue(args, &i, "--public")
			if err != nil {
				return err
			}
			pub = v
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if priv == "" {
		return fmt.Errorf("usage: zpkg key generate --private <path> [--public <path>]")
	}
	if pub == "" {
		pub = strings.TrimSuffix(priv, filepath.Ext(priv)) + ".pub"
	}
	kp, err := sign.WriteKeypair(priv, pub)
	if err != nil {
		return err
	}
	fmt.Printf("generated Ed25519 key pair\n")
	fmt.Printf("  private: %s (0600)\n", priv)
	fmt.Printf("  public:  %s\n", pub)
	fmt.Printf("  keyid:   %s\n", kp.KeyID)
	fmt.Println("Keep the private key offline. Import the public key with: zpkg key import", pub)
	return nil
}

func cmdKeyImport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zpkg key import <pubkey>...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, p := range args {
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("unknown option %q", p)
		}
		id, dest, err := sign.ImportPublicKey(cfg.TrustedKeys, p)
		if err != nil {
			return err
		}
		fmt.Printf("imported %s\n", id)
		fmt.Printf("  path: %s\n", dest)
	}
	fmt.Println("Compare the key ID with the repository's published fingerprint before trusting it.")
	return nil
}

func cmdKeyList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: zpkg key list")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	kr, err := sign.LoadKeyring(cfg.TrustedKeys)
	if err != nil {
		return err
	}
	if kr.Len() == 0 {
		fmt.Printf("no trusted keys in %s\n", cfg.TrustedKeys)
		return nil
	}
	for _, info := range kr.List() {
		fmt.Printf("%s  %s\n", info.ID, info.Path)
	}
	return nil
}

func cmdKeyRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zpkg key remove <keyid|prefix>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, id := range args {
		removed, err := sign.RemovePublicKey(cfg.TrustedKeys, id)
		if err != nil {
			return err
		}
		fmt.Printf("removed %s\n", removed)
	}
	return nil
}

func cmdSign(args []string) error {
	var key string
	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--key" || strings.HasPrefix(a, "--key="):
			v, err := optValue(args, &i, "--key")
			if err != nil {
				return err
			}
			key = v
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		default:
			files = append(files, a)
		}
	}
	if key == "" || len(files) == 0 {
		return fmt.Errorf("usage: zpkg sign --key <private> <file>...")
	}
	for _, f := range files {
		id, err := sign.SignFile(key, f)
		if err != nil {
			return err
		}
		fmt.Printf("signed %s -> %s (key %s)\n", f, sign.SignaturePath(f), id)
	}
	return nil
}

func cmdVerify(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zpkg verify <file>...")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	kr, err := sign.LoadKeyring(cfg.TrustedKeys)
	if err != nil {
		return err
	}
	for _, f := range args {
		id, err := sign.VerifyFile(f, kr)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		fmt.Printf("%s: signature OK (key %s)\n", f, id)
	}
	return nil
}

func cmdSignRecipes(args []string) error {
	var key, dir string
	var generation int64
	var expires time.Time
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--key" || strings.HasPrefix(a, "--key="):
			v, err := optValue(args, &i, "--key")
			if err != nil {
				return err
			}
			key = v
		case a == "--generation" || strings.HasPrefix(a, "--generation="):
			v, err := optValue(args, &i, "--generation")
			if err != nil {
				return err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("--generation must be a positive integer")
			}
			generation = n
		case a == "--expires" || strings.HasPrefix(a, "--expires="):
			v, err := optValue(args, &i, "--expires")
			if err != nil {
				return err
			}
			if expires, err = parseExpiry(v); err != nil {
				return err
			}
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q", a)
		default:
			if dir != "" {
				return fmt.Errorf("multiple recipe trees given")
			}
			dir = a
		}
	}
	if key == "" {
		return fmt.Errorf("usage: zpkg sign-recipes --key <private> [--generation N] [--expires ...] [dir]")
	}
	if dir == "" {
		dir = "."
	}
	m, err := recipesig.Sign(dir, key, generation, expires)
	if err != nil {
		return err
	}
	fmt.Printf("signed %d recipe files in %s\n", len(m.Files), dir)
	fmt.Printf("  generation: %d\n", m.Generation)
	fmt.Printf("  expires:    %s\n", m.Expires)
	fmt.Printf("  keyid:      %s\n", m.KeyID)
	return nil
}

// parseIndexArgs parses the flags shared by "mkindex".
func parseIndexArgs(args []string, defaultDir string) (string, build.IndexOptions, error) {
	dir := defaultDir
	var opts build.IndexOptions
	dirSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--key" || strings.HasPrefix(a, "--key="):
			v, err := optValue(args, &i, "--key")
			if err != nil {
				return "", opts, err
			}
			opts.SignKeyPath = v
		case a == "--generation" || strings.HasPrefix(a, "--generation="):
			v, err := optValue(args, &i, "--generation")
			if err != nil {
				return "", opts, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return "", opts, fmt.Errorf("--generation must be a positive integer")
			}
			opts.Generation = n
		case a == "--expires" || strings.HasPrefix(a, "--expires="):
			v, err := optValue(args, &i, "--expires")
			if err != nil {
				return "", opts, err
			}
			if opts.Expires, err = parseExpiry(v); err != nil {
				return "", opts, err
			}
		case strings.HasPrefix(a, "-"):
			return "", opts, fmt.Errorf("unknown option %q", a)
		default:
			if dirSet {
				return "", opts, fmt.Errorf("multiple repository directories given")
			}
			dir = a
			dirSet = true
		}
	}
	return dir, opts, nil
}

func printIndexSummary(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return
	}
	var idx build.IndexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return
	}
	fmt.Printf("index: %s\n", filepath.Join(dir, "index.json"))
	fmt.Printf("  schema:     %d\n", idx.Schema)
	fmt.Printf("  generation: %d\n", idx.Generation)
	fmt.Printf("  expires:    %s\n", idx.Expires)
	if idx.KeyID != "" {
		fmt.Printf("  keyid:      %s\n", idx.KeyID)
		fmt.Printf("  public key: %s\n", filepath.Join(dir, "keys", idx.KeyID+".pub"))
	}
}

func optValue(args []string, i *int, name string) (string, error) {
	a := args[*i]
	if strings.HasPrefix(a, name+"=") {
		return strings.TrimPrefix(a, name+"="), nil
	}
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s needs a value", name)
	}
	*i = *i + 1
	return args[*i], nil
}

func parseExpiry(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty expiry")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("invalid expiry %q", s)
		}
		return time.Now().Add(time.Duration(n) * 24 * time.Hour), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	return time.Time{}, fmt.Errorf("invalid expiry %q (use RFC3339, 90d or 720h)", s)
}
