// Package render produces backend-agnostic tab images consumed by the
// x11 and wlr HUD backends.
package render

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

// FontCandidates returns the ordered list of fallback font paths that FontPath
// tries when fontconfig isn't available. Exposed for diagnostic logging.
func FontCandidates() []string { return fontCandidates }
