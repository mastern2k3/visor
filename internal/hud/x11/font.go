package x11

import (
	"errors"
	"os"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
	"github.com/jezek/xgbutil/xgraphics"
)

// findFont locates a monospaced TrueType font on the system. We don't
// require a specific font — first match wins. None of these paths are
// hot reads; we open + parse once at dock startup.
var fontCandidates = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/liberation2/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoMono-Regular.ttf",
}

func loadFont() (*truetype.Font, error) {
	for _, p := range fontCandidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		ft, perr := xgraphics.ParseFont(f)
		f.Close()
		if perr == nil {
			return ft, nil
		}
	}
	return nil, errors.New("no mono TTF font found on system (tried DejaVu / Liberation / Noto)")
}
