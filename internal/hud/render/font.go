// Package render produces backend-agnostic tab images consumed by the
// x11 and wlr HUD backends.
package render

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

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
	if ft, err := loadViaFontconfig(); err == nil {
		return ft, nil
	}
	for _, p := range fontCandidates {
		if ft, err := loadFromPath(p); err == nil {
			return ft, nil
		}
	}
	return nil, errors.New("no mono TTF font found: fc-match not on PATH (or returned non-TTF) and no fallback paths under /usr/share/fonts matched")
}

// loadViaFontconfig shells out to `fc-match` to get the system's preferred
// monospaced TTF, then parses it. Returns an error if fc-match isn't on
// PATH, doesn't return a TTF, or the file fails to parse.
func loadViaFontconfig() (*truetype.Font, error) {
	out, err := exec.Command("fc-match", "-f", "%{file}\n", "monospace:fontformat=TrueType").Output()
	if err != nil {
		return nil, fmt.Errorf("fc-match: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return nil, errors.New("fc-match returned empty path")
	}
	return loadFromPath(path)
}

func loadFromPath(p string) (*truetype.Font, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseFont(f)
}

func parseFont(r io.Reader) (*truetype.Font, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return truetype.Parse(b)
}
