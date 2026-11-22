package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/studio-nosa-tech/zpkg/internal/config"
	"github.com/studio-nosa-tech/zpkg/internal/options"
)

const videoCardsUsage = `Usage:
  zpkg video-cards                show VIDEO_CARDS
  zpkg video-cards <card>...      enable cards (iris, virgl, amdgpu, ...)
  zpkg video-cards -<card>...     disable cards
  zpkg video-cards --clear        clear the selection

VIDEO_CARDS is written to VIDEO_CARDS= in /etc/zpkg/make.conf and passed to
recipes as ZPKG_VIDEO_CARDS. Mesa maps entries to Gallium drivers:

  iris      Intel Gen8+ (Skylake and newer)     crocus  Intel Gen4-7
  i915      Intel Gen3                          virgl   QEMU/virtio-gpu
  softpipe  software fallback (always built)    radeonsi/r600/llvmpipe
                                                  require LLVM (not yet)

After changing the selection, rebuild the consumers:
  zpkg install -f mesa
`

// cmdVideoCards shows or edits the global VIDEO_CARDS selection.
func cmdVideoCards(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(videoCardsUsage)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	selected := map[string]bool{}
	for _, c := range cfg.Options.VideoCards {
		selected[c] = true
	}
	if len(args) == 0 {
		if len(cfg.Options.VideoCards) == 0 {
			fmt.Println("VIDEO_CARDS: (unset; Mesa defaults to virgl softpipe)")
		} else {
			fmt.Printf("VIDEO_CARDS: %s\n", strings.Join(cfg.Options.VideoCards, " "))
		}
		return nil
	}

	for _, a := range args {
		switch {
		case a == "--clear":
			selected = map[string]bool{}
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("unknown option %q", a)
		case strings.HasPrefix(a, "-"):
			card := strings.ToLower(strings.TrimPrefix(a, "-"))
			if card == "" {
				return fmt.Errorf("empty card name")
			}
			delete(selected, card)
		default:
			selected[strings.ToLower(a)] = true
		}
	}
	cards := sortedKeys(selected)
	if err := options.SaveVideoCards(cfg.Root, cards); err != nil {
		return err
	}
	if len(cards) == 0 {
		fmt.Println("VIDEO_CARDS cleared (Mesa defaults to virgl softpipe)")
	} else {
		fmt.Printf("VIDEO_CARDS: %s\n", strings.Join(cards, " "))
	}
	if _, ok := os.LookupEnv("ZPKG_VIDEO_CARDS"); ok {
		fmt.Println("note: ZPKG_VIDEO_CARDS is set and overrides make.conf for this shell")
	}
	fmt.Println("rebuild consumers with: zpkg install -f mesa")
	return nil
}
