// Package render produces backend-agnostic tongue images consumed by the
// x11 and wlr HUD backends.
package render

import (
	"errors"
	"io"
	"os"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
)

// fontCandidates is the search order for a monospaced TrueType font on the
// system. First match wins; we open + parse once at backend startup.
var fontCandidates = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/liberation2/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoMono-Regular.ttf",
}

// LoadFont returns the first system-installed mono TTF that parses.
// Returns an error if none of the candidate paths are readable.
func LoadFont() (*truetype.Font, error) {
	for _, p := range fontCandidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		ft, perr := parseFont(f)
		f.Close()
		if perr == nil {
			return ft, nil
		}
	}
	return nil, errors.New("no mono TTF font found on system (tried DejaVu / Liberation / Noto)")
}

func parseFont(r io.Reader) (*truetype.Font, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return truetype.Parse(b)
}
