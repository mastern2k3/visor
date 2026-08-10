// Package render produces backend-agnostic tab images consumed by the
// x11 and wlr HUD backends.
package render

import (
	"fmt"
	"os"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
)

// fontCandidates is the fallback search order when fontconfig isn't available
// (e.g. on a stripped-down system without `fc-match`). First match wins.
var fontCandidates = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/liberation2/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoMono-Regular.ttf",
}

// FontCandidates returns the ordered list of fallback font paths that LoadFont
// tries when fontconfig isn't available. Exposed for diagnostic logging.
func FontCandidates() []string { return fontCandidates }

// LoadFont resolves a monospaced TrueType font on the system.
//
// Resolution order:
//  1. fontconfig: `fc-match -f "%{file}\n" "monospace:fontformat=TrueType"`.
//     This is the standard Linux mechanism and works on every distro that
//     ships fontconfig (Debian/Ubuntu, Fedora, Arch, NixOS, etc.). The Nix
//     store paths that confuse hardcoded lookups are transparent here.
//  2. Hardcoded fallbacks under /usr/share/fonts (see fontCandidates) for
//     systems without `fc-match` on PATH.
//
// Returns the parsed font or an error describing both attempts.
func LoadFont() (*truetype.Font, error) {
	p, err := FontPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read font %s: %w", p, err)
	}
	ft, err := truetype.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("parse font %s: %w", p, err)
	}
	return ft, nil
}
