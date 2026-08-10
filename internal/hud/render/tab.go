package render

import (
	"image"
	"image/color"
	"math"
	"time"

	"github.com/fogleman/gg"
)

// Geometry, shared by both backends. Backends import these; they never
// redeclare the literals.
const (
	// CapsuleW is the width of the capsule that is *visible at rest*, i.e. the
	// width of the strip the user sees when nothing is protruding.
	CapsuleW = 18
	// ContentH is the height of the capsule and the expanded panel.
	ContentH = 44
	// ShadowPad is transparent padding reserved for the drop shadow on the
	// left, top and bottom. There is none on the right: that edge is flush
	// with (or past) the screen edge.
	ShadowPad = 5

	// MaxProtrusion is the furthest a tab is ever shifted away from the screen
	// edge: an attention=needs tab sits AlertProtrusion out, and a working tab
	// wobbles up to WobbleAmp further. Defined once here; the wlr backend's
	// surface overflow is an alias of it.
	//
	// Plain literal, not a derived expression: it must satisfy
	// MaxProtrusion >= AlertProtrusion + round(WobbleAmp) (the animation code
	// applies the wobble via math.Round, not truncation), and
	// TestMaxProtrusion_CoversActualWobblePeak in tab_test.go enforces that
	// invariant directly, independent of how this literal is spelled. Update
	// this value by hand if AlertProtrusion or WobbleAmp change; the test
	// fails loudly if the new value under-provisions.
	MaxProtrusion = 12
	// CapsuleDrawW is how wide the capsule is actually *drawn*. It is
	// MaxProtrusion wider than the visible width so that the extra hangs off
	// the screen edge at rest: protruding or wobbling then reveals more capsule
	// instead of sliding the capsule away from the edge and leaving a gap.
	// Without this the capsule looked like it was "hanging in the air with a
	// margin to the right", because everything past it is transparent now.
	CapsuleDrawW = CapsuleW + MaxProtrusion

	// ExpandedW is the panel's own width.
	ExpandedW = 300

	// BufW/BufH are the rendered buffer dimensions. Note the buffer is NOT
	// symmetric between the two orientations: the pad sits beyond the capsule
	// in x11 (TabRight=false) and beyond the panel in wlr (TabRight=true), so
	// the region panelRect hands back is ExpandedW wide in the former and
	// ShadowPad+ExpandedW wide in the latter.
	BufW = ShadowPad + CapsuleDrawW + ExpandedW
	BufH = ShadowPad*2 + ContentH

	// Radius is the corner radius of the capsule and panel. Only the leading
	// (left) corners are visibly rounded; the shape is drawn Radius wider than
	// the element so its right corners fall outside it, and each shape is
	// clipped to its own rect so that overhang cannot escape into a neighbour.
	Radius = 10

	// RowPitch is the vertical distance between adjacent tabs. It equals BufH
	// so each tab's shadow lives inside its own buffer and no neighbour clips
	// it — which matters because x11 gives every tab a separate window.
	RowPitch = BufH

	// AlertProtrusion: an attention=needs tab sits this many px further from
	// the screen edge, so it is distinguishable by shape alone. Chosen >
	// WobbleAmp so a needs tab is unambiguously further out than any working
	// tab at its wobble peak.
	AlertProtrusion = 8
	// Wobble animation for working tabs.
	WobbleAmp    = 4.0
	WobblePeriod = 0.9

	// Work-bar: a segmented strip along the capsule's bottom inside edge,
	// replacing the old stacked dots which cramped at this width.
	workSegs   = 3
	workBarH   = 2
	workInset  = 3
	workGap    = 2
	workBottom = 3 // px from the capsule's bottom edge to the bar

	// Text layout inside the panel.
	textPad      = 12 // x offset from the panel's inner edge to the text
	textRightPad = 8  // reserved between text and the panel's far edge
	lineGap      = 2  // vertical gap between the name and meta lines
)

// TabState is everything the renderer needs. Colour is resolved from the
// state fields via the Palette, so backends no longer pass a packed colour.
type TabState struct {
	// Objective state, used for both colour and the panel's state words.
	Activity  string // "working" | "waiting" | "unknown"
	Attention string // "ack" | "needs" | "dismissed"
	Waiting   string // "" | "user" | "permission"

	Glyph string // 1-2 chars centred in the capsule
	Name  string // panel line 1
	Path  string // panel line 2 tail (already $HOME-abbreviated)

	// Elapsed is time in the current state, derived from StateSince by the
	// caller so the renderer stays free of clocks.
	Elapsed time.Duration

	Expanded bool
	// TabRight puts the capsule on the buffer's right edge instead of its
	// left. False (x11): capsule at x=ShadowPad, panel extends rightward.
	// True (wlr): capsule at the right edge, panel extends leftward.
	TabRight bool
	// Shadow enables visor's own drop shadow. Disabled by config when the
	// user prefers their compositor's shadow, or none.
	Shadow bool
	// Square draws every shape with corner radius 0 instead of Radius. It is
	// the x11 no-compositor fallback: without a compositing manager the window
	// has no alpha to blend against, so a rounded, antialiased corner arrives
	// on screen as a partially black one. Opt-in; the default is rounded.
	Square bool

	BackgroundRunning int
	BackgroundOutcome string // "" | "done" | "failed"

	// HaloPhase in [0,1) drives the permission pulse. Ignored for states
	// whose StateColors.Glow is false.
	HaloPhase float64
}

// TabImage is the rendered output plus the metadata both backends need.
type TabImage struct {
	RGBA     *image.RGBA
	Overflow bool // Name was wider than the panel could show
}

// capsuleX returns the buffer x of the capsule's left edge. The capsule is
// CapsuleDrawW wide from here, so its drawn right edge is at BufW for TabRight
// and at ShadowPad+CapsuleDrawW otherwise.
func capsuleX(tabRight bool) int {
	if tabRight {
		return BufW - CapsuleDrawW
	}
	return ShadowPad
}

// capsuleRect is the region the capsule is allowed to paint into. It is the
// *drawn* width, not the visible-at-rest width — clipping to CapsuleW would
// throw away the MaxProtrusion overhang that keeps the capsule welded to the
// screen edge while it protrudes.
func capsuleRect(tabRight bool) image.Rectangle {
	x := capsuleX(tabRight)
	return image.Rect(x, ShadowPad, x+CapsuleDrawW, ShadowPad+ContentH)
}

// panelRect is the region that is opaque only when expanded. It butts the
// capsule's drawn edge, so it moves with CapsuleDrawW.
func panelRect(tabRight bool) image.Rectangle {
	if tabRight {
		return image.Rect(0, ShadowPad, BufW-CapsuleDrawW, ShadowPad+ContentH)
	}
	return image.Rect(ShadowPad+CapsuleDrawW, ShadowPad, BufW, ShadowPad+ContentH)
}

// cornerRadius is the radius every shape in this tab draws with: Radius
// normally, 0 when the state asks for squared corners. See TabState.Square.
func cornerRadius(s TabState) float64 {
	if s.Square {
		return 0
	}
	return Radius
}

// workBarY is the buffer y of the work bar's top edge.
func workBarY() int {
	return ShadowPad + ContentH - workBottom - workBarH
}

// workSegX is the buffer x of segment i's left edge.
func workSegX(i int, tabRight bool) int {
	return capsuleX(tabRight) + workInset + i*(workSegW()+workGap)
}

// workSegW divides the *visible-at-rest* CapsuleW, not CapsuleDrawW: the bar
// has to be readable at rest, when everything past CapsuleW is off screen.
func workSegW() int {
	avail := CapsuleW - 2*workInset - (workSegs-1)*workGap
	return avail / workSegs
}

// clipTo runs fn with all drawing clipped to r.
//
// gg's Pop() deliberately does *not* restore the clip mask — it copies the
// current (post-Push) mask back over the saved one — so Push/Pop alone leaks a
// clip into everything drawn afterwards. ResetClip is therefore explicit.
// Only one clip is ever active at a time here, so resetting rather than
// restoring is correct; nesting clipTo would need a different approach.
func clipTo(dc *gg.Context, r image.Rectangle, fn func()) {
	dc.Push()
	dc.DrawRectangle(float64(r.Min.X), float64(r.Min.Y), float64(r.Dx()), float64(r.Dy()))
	dc.Clip()
	fn()
	dc.ResetClip()
	dc.Pop()
}

// DrawTab renders one tab into a BufW x BufH RGBA buffer.
//
// Layout (x11, TabRight=false):
//
//	0             .. ShadowPad      transparent shadow padding
//	ShadowPad     .. +CapsuleDrawW  the capsule (always opaque). Only its
//	                                first CapsuleW px are on screen at rest;
//	                                the rest hangs past the screen edge.
//	+CapsuleDrawW .. BufW           the panel (opaque only when Expanded)
//
// Corner rounding rule: round the leading (left) edge of the outermost visible
// element and never an edge that butts another element; the right edge is
// always square. The capsule's left corners are always rounded — it is the
// leading edge when collapsed in both backends, and when expanded in wlr it
// reads as seated on the panel. The panel's left corners are rounded only for
// TabRight (wlr), where the panel's left edge is the leading edge; in x11 the
// panel's left edge butts the capsule, so it stays square or a rounded notch
// appears between the two.
//
// `f` may be nil, in which case all text is skipped and Overflow is false.
func DrawTab(s TabState, f *Faces, p Palette) TabImage {
	sc := p.For(s.Activity, s.Attention, s.Waiting)
	dc := gg.NewContext(BufW, BufH)

	cx := float64(capsuleX(s.TabRight))
	cy := float64(ShadowPad)
	ch := float64(ContentH)
	rad := cornerRadius(s)

	// --- panel (drawn first so the capsule overlaps its edge) ---------------
	if s.Expanded {
		pr := panelRect(s.TabRight)
		px, pw := float64(pr.Min.X), float64(pr.Dx())
		if s.Shadow {
			drawShadow(dc, px, cy, pw, ch, rad)
		}
		grad := gg.NewLinearGradient(0, cy, 0, cy+ch)
		grad.AddColorStop(0, rgbaOf(p.PanelTop))
		grad.AddColorStop(1, rgbaOf(p.PanelBot))
		clipTo(dc, pr, func() {
			// TabRight: left corners round, right corners pushed outside the
			// clip by the +Radius overhang. Otherwise both edges are square.
			if s.TabRight {
				dc.DrawRoundedRectangle(px, cy, pw+rad, ch, rad)
			} else {
				dc.DrawRectangle(px, cy, pw, ch)
			}
			dc.SetFillStyle(grad)
			dc.Fill()

			if s.TabRight {
				dc.DrawRoundedRectangle(px+0.5, cy+0.5, pw+rad-1, ch-1, rad)
			} else {
				dc.DrawRectangle(px+0.5, cy+0.5, pw-1, ch-1)
			}
			dc.SetColor(rgbaOf(p.PanelBorder))
			dc.SetLineWidth(1)
			dc.Stroke()
		})
	}

	// --- capsule ----------------------------------------------------------
	// Shadow and halo stay CapsuleW wide, not CapsuleDrawW: the capsule's
	// trailing edge is welded to the screen edge, so a shadow or glow there
	// would be invisible in x11 and would only push a dark band into the panel.
	if s.Shadow {
		drawShadow(dc, cx, cy, float64(CapsuleW), ch, rad)
	}
	if sc.Glow {
		drawHalo(dc, cx, cy, float64(CapsuleW), ch, sc.Halo, s.HaloPhase, rad)
	}

	grad := gg.NewLinearGradient(0, cy, 0, cy+ch)
	grad.AddColorStop(0, rgbaOf(sc.Top))
	grad.AddColorStop(0.62, rgbaOf(sc.Base))
	grad.AddColorStop(1, rgbaOf(sc.Bot))
	clipTo(dc, capsuleRect(s.TabRight), func() {
		dc.DrawRoundedRectangle(cx, cy, float64(CapsuleDrawW)+rad, ch, rad)
		dc.SetFillStyle(grad)
		dc.Fill()

		// Specular hairline inset along the top and leading edges. It is what
		// makes the capsule read as an object rather than a painted stripe.
		dc.DrawRoundedRectangle(cx+0.5, cy+0.5, float64(CapsuleDrawW)+rad-1, ch-1, rad)
		dc.SetRGBA(1, 1, 1, 0.30)
		dc.SetLineWidth(1)
		dc.Stroke()
	})

	// --- work bar ---------------------------------------------------------
	drawWorkBar(dc, s, p)

	overflow := false
	if f != nil {
		// --- glyph --------------------------------------------------------
		if s.Glyph != "" {
			clipTo(dc, capsuleRect(s.TabRight), func() {
				dc.SetFontFace(f.Glyph)
				dc.SetColor(rgbaOf(p.GlyphFG(sc.Base)))
				// Centred in the visible-at-rest CapsuleW, not in CapsuleDrawW:
				// the overhang is off screen, so centring on it would put the
				// glyph half off the edge. Same reasoning as the work bar.
				dc.DrawStringAnchored(s.Glyph, cx+float64(CapsuleW)/2, cy+ch/2, 0.5, 0.4)
			})
		}

		// --- panel text ---------------------------------------------------
		if s.Expanded {
			overflow = drawPanelText(dc, s, f, p)
		}
	}

	// gg hands back the same *image.RGBA it has been drawing into, so this is
	// built once, after every draw call above.
	return TabImage{RGBA: dc.Image().(*image.RGBA), Overflow: overflow}
}

// drawPanelText renders the two panel lines and reports whether the name
// overflowed the available width. Text is clipped to the panel so a long name
// can never bleed into the capsule.
func drawPanelText(dc *gg.Context, s TabState, f *Faces, p Palette) (overflow bool) {
	pr := panelRect(s.TabRight)

	textX := float64(pr.Min.X + textPad)
	// The panel's far edge is its Max.X in both orientations: for x11 that is
	// the buffer edge, for wlr it is where the capsule begins.
	limit := float64(pr.Max.X - textRightPad)

	// Vertical layout: centre the two-line block in the content height.
	nm := f.Name.Metrics()
	mm := f.Meta.Metrics()
	nameH := float64(nm.Ascent.Ceil() + nm.Descent.Ceil())
	metaH := float64(mm.Ascent.Ceil() + mm.Descent.Ceil())
	blockH := nameH + lineGap + metaH
	top := float64(ShadowPad) + (float64(ContentH)-blockH)/2
	nameBase := top + float64(nm.Ascent.Ceil())
	metaBase := nameBase + float64(nm.Descent.Ceil()) + lineGap + float64(mm.Ascent.Ceil())

	clipTo(dc, pr, func() {
		// Line 1: project name.
		dc.SetFontFace(f.Name)
		dc.SetColor(rgbaOf(p.PanelName))
		dc.DrawString(s.Name, textX, nameBase)
		nw, _ := dc.MeasureString(s.Name)
		overflow = textX+nw > limit

		// Line 2: state words (tinted) · elapsed · path.
		sc := p.For(s.Activity, s.Attention, s.Waiting)
		dc.SetFontFace(f.Meta)
		x := textX
		words := StateWords(s.Activity, s.Attention, s.Waiting)
		dc.SetColor(rgbaOf(sc.Base))
		dc.DrawString(words, x, metaBase)
		w, _ := dc.MeasureString(words)
		x += w

		sep := " · "
		dc.SetColor(rgbaOf(p.PanelMeta))
		dc.DrawString(sep, x, metaBase)
		w, _ = dc.MeasureString(sep)
		x += w

		el := ElapsedString(s.Elapsed)
		dc.SetColor(rgbaOf(p.PanelElapsed))
		dc.DrawString(el, x, metaBase)
		w, _ = dc.MeasureString(el)
		x += w

		if s.Path != "" {
			dc.SetColor(rgbaOf(p.PanelMeta))
			dc.DrawString(sep+s.Path, x, metaBase)
		}
	})
	return overflow
}

// drawWorkBar paints the segmented background-work indicator. Running work
// fills that many segments; otherwise a single segment carries the outcome.
// Nothing is drawn when there is neither.
//
// Segments are plain rectangles: at workSegW x workBarH (2x2 px) a corner
// radius would cover each pixel only partially, so the segment colour would
// arrive on screen blended with the capsule gradient underneath instead of as
// itself.
func drawWorkBar(dc *gg.Context, s TabState, p Palette) {
	var fill uint32
	n := 0
	switch {
	case s.BackgroundRunning > 0:
		n = min(s.BackgroundRunning, workSegs)
		fill = p.WorkRunning
	case s.BackgroundOutcome == "done":
		n, fill = 1, p.WorkDone
	case s.BackgroundOutcome == "failed":
		n, fill = 1, p.WorkFailed
	default:
		return
	}
	y := float64(workBarY())
	segW := float64(workSegW())
	for i := 0; i < workSegs; i++ {
		c := p.WorkOff
		if i < n {
			c = fill
		}
		dc.DrawRectangle(float64(workSegX(i, s.TabRight)), y, segW, workBarH)
		dc.SetColor(rgbaOf(c))
		dc.Fill()
	}
}

// drawShadow paints a blurred dark shape offset 1px down, behind whatever is
// drawn next. The blur is a three-pass box blur, a good enough gaussian
// approximation at this radius.
//
// The shape is exactly w wide — it does NOT take the +Radius overhang the
// capsule and panel fills use. Those need the overhang to keep their right
// corners square; the shadow does not, because its right corners sit under the
// caster's own opaque pixels and are invisible either way. Giving it the
// overhang instead pushed a dark band ~Radius+blur px past the capsule, which
// in the x11 orientation lands on the panel as a shadow cast by nothing.
//
// Allocating a full-buffer context per call is deliberate and not a hot path:
// tab rendering is event-driven (state change, hover, or a once-per-second
// elapsed tick on an expanded tab). The 30Hz animation loop only moves
// windows — it never re-renders — so there is nothing here to optimise.
func drawShadow(dc *gg.Context, x, y, w, h, rad float64) {
	sh := gg.NewContext(BufW, BufH)
	sh.DrawRoundedRectangle(x, y+1, w, h, rad)
	sh.SetRGBA(0, 0, 0, 0.55)
	sh.Fill()
	dc.DrawImage(boxBlur(sh.Image().(*image.RGBA), 3, 3), 0, 0)
}

// drawHalo paints a soft coloured glow around the capsule, pulsing with phase.
// Only used for the permission state.
//
// Like drawShadow, the shape carries no +Radius overhang: it extends 2px past
// the capsule on every side and nothing more, so the glow cannot wash the
// panel's leading edge in the x11 orientation.
func drawHalo(dc *gg.Context, x, y, w, h float64, halo uint32, phase, rad float64) {
	// (1-cos)/2 maps phase to [0,1] with zero derivative at the endpoints, so
	// the pulse eases rather than snapping at its extremes.
	t := (1 - math.Cos(phase*2*math.Pi)) / 2
	alpha := 0.25 + 0.35*t
	c := rgbaOf(halo)
	g := gg.NewContext(BufW, BufH)
	g.DrawRoundedRectangle(x-2, y-2, w+4, h+4, rad+2)
	g.SetRGBA(float64(c.R)/255, float64(c.G)/255, float64(c.B)/255, alpha)
	g.Fill()
	dc.DrawImage(boxBlur(g.Image().(*image.RGBA), 4, 2), 0, 0)
}

// rgbaOf converts a packed 0xRRGGBB to an opaque color.RGBA.
func rgbaOf(c uint32) color.RGBA {
	return color.RGBA{
		R: uint8((c >> 16) & 0xff),
		G: uint8((c >> 8) & 0xff),
		B: uint8(c & 0xff),
		A: 0xff,
	}
}
