package render

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/BurntSushi/freetype-go/freetype"
	"github.com/BurntSushi/freetype-go/freetype/truetype"
)

// Shared with both backends. Keep in sync with the x11 backend's window sizing.
const (
	TongueW   = 10  // visible width when collapsed
	TongueH   = 36  // window height
	ExpandedW = 300 // visible width when hovered
	TextPad   = 18  // x where the cwd label begins
	FontPt    = 13.5
	// TextYBaseline is the freetype baseline; picked so the cap height sits
	// centred-ish in TongueH. Empirically matched to the previous x11 layout.
	TextYBaseline = 24

	textRightPad = 8 // px reserved between text right-edge and panel edge for visual breathing room
)

// TongueState is the subset of session data the renderer needs.
type TongueState struct {
	Color uint32 // 0x00RRGGBB; high byte ignored
	Label string // already-resolved display label (Title || DisplayCWD || ID[:8])
}

// TongueImage is the rendered output plus metadata x11/wlr both need.
type TongueImage struct {
	RGBA     *image.RGBA
	Overflow bool // true if Label was wider than the panel could show
}

// DrawTongue produces an ExpandedW-by-TongueH RGBA buffer with a solid
// background and the label rendered starting at TextPad. The returned image
// is fully opaque; the caller decides how to display the collapsed-only
// portion vs the expanded portion.
//
// `font` may be nil — in that case the label is skipped and Overflow is false.
func DrawTongue(s TongueState, font *truetype.Font) TongueImage {
	img := image.NewRGBA(image.Rect(0, 0, ExpandedW, TongueH))
	bg := unpackRGBA(s.Color)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	out := TongueImage{RGBA: img}
	if font == nil || s.Label == "" {
		return out
	}

	fg := contrastFG(bg)
	out.Overflow = drawText(img, font, FontPt, TextPad, TextYBaseline, fg, s.Label)
	return out
}

// drawText renders `text` into img using freetype directly. Returns true if
// the rendered text width exceeded the visible label region (ExpandedW - TextPad - textRightPad).
func drawText(img *image.RGBA, font *truetype.Font, ptSize float64, x, yBaseline int, fg color.Color, text string) (overflow bool) {
	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(font)
	c.SetFontSize(ptSize)
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(&image.Uniform{C: fg})

	pt := freetype.Pt(x, yBaseline)
	end, err := c.DrawString(text, pt)
	if err != nil {
		return false
	}
	// textRightPx is freetype's pen-advance after the final glyph, which differs
	// from a true glyph bounding-box right edge by at most one side-bearing.
	// Close enough for an overflow boolean.
	textRightPx := int(end.X >> 6)
	return textRightPx > (ExpandedW - textRightPad)
}

// unpackRGBA converts a packed 0xRRGGBB to an opaque color.RGBA.
func unpackRGBA(c uint32) color.RGBA {
	return color.RGBA{
		R: uint8((c >> 16) & 0xff),
		G: uint8((c >> 8) & 0xff),
		B: uint8(c & 0xff),
		A: 0xff,
	}
}

// contrastFG returns a foreground colour that reads well against bg.
// Cheap luminance check (Rec. 601 weights): anything bright gets near-black
// text, anything dark gets near-white. The 140 threshold is empirical —
// picked to flip at roughly mid-grey, matched to the previous x11 behavior.
func contrastFG(bg color.RGBA) color.RGBA {
	lum := (int(bg.R)*299 + int(bg.G)*587 + int(bg.B)*114) / 1000
	if lum > 140 {
		return color.RGBA{0x10, 0x14, 0x1c, 0xff}
	}
	return color.RGBA{0xe5, 0xe9, 0xf0, 0xff}
}
