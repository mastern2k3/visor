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
	Color    uint32 // 0x00RRGGBB; high byte ignored
	Label    string // already-resolved display label (Title || DisplayCWD || ID[:8])
	Expanded bool   // true = full opaque panel; false = panel region is transparent
	// TongueRight controls which side of the buffer the opaque "tongue strip"
	// sits on. False (default): leftmost TongueW pixels are the tongue; panel
	// extends rightward. Used by the x11 backend, whose window slides off the
	// right edge of the screen and reveals the leftmost pixels first.
	// True: rightmost TongueW pixels are the tongue; panel extends leftward.
	// Used by the wlr backend, whose right-anchored layer surface puts buffer
	// x=ExpandedW-1 at the screen's right edge.
	TongueRight bool
}

// TongueImage is the rendered output plus metadata x11/wlr both need.
type TongueImage struct {
	RGBA     *image.RGBA
	Overflow bool // true if Label was wider than the panel could show
}

// DrawTongue produces an ExpandedW-by-TongueH RGBA buffer with a solid
// background and the label rendered starting at TextPad.
//
// When s.Expanded is false (collapsed), the panel region is cleared to fully
// transparent RGBA{0,0,0,0} and text rendering is skipped. Which side is the
// "tongue strip" depends on s.TongueRight:
//   - false (default, x11): leftmost TongueW pixels are the tongue; panel is
//     x=TongueW..ExpandedW.
//   - true (wlr): rightmost TongueW pixels are the tongue; panel is
//     x=0..ExpandedW-TongueW.
//
// When s.Expanded is true, the entire buffer is opaque and the label is drawn.
//
// `font` may be nil — in that case the label is skipped and Overflow is false.
func DrawTongue(s TongueState, font *truetype.Font) TongueImage {
	img := image.NewRGBA(image.Rect(0, 0, ExpandedW, TongueH))
	bg := unpackRGBA(s.Color)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	// panelRect is the region that becomes transparent when collapsed.
	var panelRect image.Rectangle
	if s.TongueRight {
		panelRect = image.Rect(0, 0, ExpandedW-TongueW, TongueH)
	} else {
		panelRect = image.Rect(TongueW, 0, ExpandedW, TongueH)
	}

	if !s.Expanded {
		// Clear the panel region to fully transparent so compositors show
		// the desktop through it — Wayland analogue to the x11 window-slide.
		transparent := color.RGBA{0, 0, 0, 0}
		draw.Draw(img, panelRect, &image.Uniform{C: transparent}, image.Point{}, draw.Src)
		return TongueImage{RGBA: img}
	}

	out := TongueImage{RGBA: img}
	if font == nil || s.Label == "" {
		return out
	}

	// Text always sits inside the panel. For TongueRight, panel is on the left:
	// text starts at TextPad and must end before the tongue strip starts.
	// For !TongueRight, text starts at TextPad past the tongue strip.
	fg := contrastFG(bg)
	var textX, rightLimit int
	if s.TongueRight {
		textX = TextPad
		rightLimit = ExpandedW - TongueW - textRightPad
	} else {
		textX = TextPad
		rightLimit = ExpandedW - textRightPad
	}
	out.Overflow = drawText(img, font, FontPt, textX, TextYBaseline, fg, s.Label, rightLimit)
	return out
}

// drawText renders `text` into img using freetype directly. Returns true if
// the rendered text width exceeded rightLimit (pixels from x=0).
func drawText(img *image.RGBA, font *truetype.Font, ptSize float64, x, yBaseline int, fg color.Color, text string, rightLimit int) (overflow bool) {
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
	return textRightPx > rightLimit
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

// ColorFor maps session state (activity, attention, waiting) to the canonical
// 0x00RRGGBB tongue colour shared by all backends. Both x11 and wlr call this
// so the colour scheme never drifts between backends.
func ColorFor(activity, attention, waiting string) uint32 {
	switch {
	case attention == "needs" && waiting == "permission":
		return 0xff7a7a // red — blocked on approval
	case attention == "needs":
		return 0xebcb8b // amber — waiting for user
	case attention == "dismissed":
		return 0x3b414e // dim — silenced
	case activity == "working":
		return 0x88c0d0 // cyan — busy
	default:
		return 0x6b7280 // grey — idle/ack
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
