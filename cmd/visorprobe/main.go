// Command visorprobe is a throwaway PoC that answers whether the x11 HUD
// backend can adopt per-pixel alpha (32-bit ARGB visual + XShape) on this
// machine's WM/compositor combination, before we commit to it in the design.
//
// It is NOT part of the shipping product. Delete it once the question is
// settled.
//
// Run:  go run ./cmd/visorprobe
//
// It prints a capability report, then maps five override-redirect dock windows
// down the right edge of the screen so the differences can be judged by eye:
//
//	V1  rounded capsule, alpha, NO own shadow, NO XShape
//	    -> does alpha work at all, and what does picom's own dock shadow do
//	       to a rounded shape when it only knows the rectangular bounding box?
//	V2  V1 + our own blurred shadow rendered into the buffer
//	    -> confirms/denies the predicted double shadow
//	V3  V2 + XShape BOUNDING region clipped to the capsule rect
//	    -> does picom then shadow the shape instead of the box, and how badly
//	       does a non-antialiased bounding region chew the rounded corners?
//	V4  V2 + XShape INPUT region only (bounding untouched)
//	    -> do clicks on the transparent shadow padding fall through to the
//	       desktop? Click each variant; only V4 should stay silent.
//	V5  control: today's flat opaque 10px-wide depth-24 rect
//
// Then it wobbles V1..V4 leftward at 30Hz for a few seconds to check that
// moving an ARGB window keeps up with picom (backend=glx, vsync=false here).
package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"time"

	"github.com/fogleman/gg"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/xevent"
	"golang.org/x/image/font/basicfont"
)

// Geometry mirrors the proposed design so the probe shows real sizes:
// a 18px-wide capsule 44px tall, with 5px of shadow padding on the left,
// top and bottom. The capsule's right edge sits flush with the screen edge,
// so the padding on the right is unnecessary.
const (
	shadowPad = 5
	capsuleW  = 18
	contentH  = 44
	bufW      = shadowPad + capsuleW // 23
	bufH      = shadowPad*2 + contentH
	radius    = 10
	rowPitch  = bufH + 6
)

func main() {
	X, err := xgbutil.NewConn()
	if err != nil {
		log.Fatalf("cannot connect to X: %v", err)
	}
	defer X.Conn().Close()

	fmt.Println("=== visorprobe: x11 alpha capability report ===")

	_ = reportCompositor(X)
	visual, depth32ok := reportVisual(X)
	shapeOK := reportShape(X)

	if !depth32ok {
		fmt.Println("\nNo 32-bit TrueColor visual. Alpha is impossible here; the")
		fmt.Println("design must fall back to opaque squared rendering. Stopping.")
		os.Exit(1)
	}

	mon := screenGeom(X)
	fmt.Printf("\nscreen: %dx%d at +%d+%d\n", mon.w, mon.h, mon.x, mon.y)

	variants := []struct {
		name     string
		ownShad  bool
		bounding bool
		input    bool
		hex      string
	}{
		{"V1 alpha only", false, false, false, "#ff4d43"},
		{"V2 +own shadow", true, false, false, "#ffa826"},
		{"V3 +bounding", true, true, false, "#3d4653"},
		{"V4 +input only", true, false, true, "#343a45"},
	}

	top := mon.y + 80
	var wins []xproto.Window

	for i, v := range variants {
		img := drawCapsule(v.hex, v.ownShad, fmt.Sprintf("%d", i+1))
		w, err := newARGBWindow(X, visual, mon.x+mon.w-bufW, top+i*rowPitch, bufW, bufH)
		if err != nil {
			log.Fatalf("%s: create window: %v", v.name, err)
		}
		if err := paint(X, w, img, 32); err != nil {
			log.Fatalf("%s: paint: %v", v.name, err)
		}
		if shapeOK {
			// The capsule occupies x=shadowPad.. , y=shadowPad..shadowPad+contentH.
			capRect := []xproto.Rectangle{{
				X: shadowPad, Y: shadowPad, Width: capsuleW, Height: contentH,
			}}
			if v.bounding {
				if err := setShape(X, w, shape.SkBounding, capRect); err != nil {
					log.Printf("%s: set bounding shape: %v", v.name, err)
				}
			}
			if v.input {
				if err := setShape(X, w, shape.SkInput, capRect); err != nil {
					log.Printf("%s: set input shape: %v", v.name, err)
				}
			}
		}
		wins = append(wins, w)
		fmt.Printf("mapped %-16s at y=%d  win=0x%x\n", v.name, top+i*rowPitch, w)
	}

	// V5: the control — today's rendering. Flat opaque 10px rect, root depth.
	ctl, err := newRootDepthWindow(X, mon.x+mon.w-10, top+4*rowPitch, 10, 36)
	if err != nil {
		log.Fatalf("V5 control: %v", err)
	}
	ctlImg := image.NewRGBA(image.Rect(0, 0, 10, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 10; x++ {
			ctlImg.SetRGBA(x, y, color.RGBA{0x88, 0xc0, 0xd0, 0xff})
		}
	}
	if err := paint(X, ctl, ctlImg, X.Screen().RootDepth); err != nil {
		log.Fatalf("V5 paint: %v", err)
	}
	fmt.Printf("mapped %-16s at y=%d  win=0x%x\n", "V5 control", top+4*rowPitch, ctl)

	// Catcher: a magenta window sitting directly BEHIND V4, lowered below it.
	// This turns the input-region test from "we saw no click" (which is also
	// what not-clicking looks like) into a positive signal: a click on V4's
	// transparent padding should be reported by the catcher, proving the click
	// passed through rather than being swallowed.
	catcher, err := newRootDepthWindow(X, mon.x+mon.w-bufW, top+3*rowPitch, bufW, bufH)
	if err != nil {
		log.Fatalf("catcher: %v", err)
	}
	catchImg := image.NewRGBA(image.Rect(0, 0, bufW, bufH))
	for y := 0; y < bufH; y++ {
		for x := 0; x < bufW; x++ {
			catchImg.SetRGBA(x, y, color.RGBA{0xc0, 0x30, 0xc0, 0xff})
		}
	}
	if err := paint(X, catcher, catchImg, X.Screen().RootDepth); err != nil {
		log.Fatalf("catcher paint: %v", err)
	}
	xproto.ConfigureWindow(X.Conn(), catcher,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeBelow})
	xevent.ButtonPressFun(func(_ *xgbutil.XUtil, e xevent.ButtonPressEvent) {
		fmt.Printf("[CATCHER] click PASSED THROUGH V4 at (%d,%d) -- input shape works\n",
			e.EventX, e.EventY)
	}).Connect(X, catcher)
	fmt.Printf("mapped %-16s at y=%d  win=0x%x (magenta, behind V4)\n",
		"catcher", top+3*rowPitch, catcher)

	fmt.Println(`
--- what to look at, top to bottom on the right edge ---
 V1 red     : alpha working? is picom's shadow a RECTANGLE around a rounded
              capsule (predicted, and wrong-looking)?
 V2 amber   : two shadows visible? (ours + picom's, offset -7,-7)
 V3 grey    : did the shadow snap to the capsule shape, and are the rounded
              corners now hard-edged/aliased?
 V4 dk grey : looks like V2; the difference is clicks.
 V5 cyan    : today's flat 10px tab, for comparison.

--- click test ---
 Click the transparent padding to the LEFT of each capsule.
 V1..V3 should report a button press below. V4 should NOT (input shape) --
 the click should reach whatever is behind it instead.

--- also try ---
 pkill picom   (then rerun) -> do the transparent regions go BLACK?
                               that is what the fallback in the design is for.

Ctrl-C to quit. Wobble test starts in 3s.`)

	go func() {
		time.Sleep(3 * time.Second)
		fmt.Println("\n[wobble] moving V1..V4 leftward at 30Hz for 6s -- watch for tearing,")
		fmt.Println("[wobble] shadow lag, or the compositor failing to keep up.")
		start := time.Now()
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for now := range tick.C {
			el := now.Sub(start).Seconds()
			if el > 6 {
				fmt.Println("[wobble] done")
				return
			}
			off := int(math.Round(4 * (1 - math.Cos(el/0.9*2*math.Pi)) / 2))
			for i, w := range wins {
				xproto.ConfigureWindow(X.Conn(), w,
					xproto.ConfigWindowX,
					[]uint32{uint32(int32(mon.x + mon.w - bufW - off))})
				_ = i
			}
		}
	}()

	xevent.ButtonPressFun(func(_ *xgbutil.XUtil, e xevent.ButtonPressEvent) {
		fmt.Printf("[click] win=0x%x button=%d at (%d,%d) in-window\n",
			e.Event, e.Detail, e.EventX, e.EventY)
	}).Connect(X, wins[0])
	for _, w := range wins[1:] {
		xevent.ButtonPressFun(func(_ *xgbutil.XUtil, e xevent.ButtonPressEvent) {
			fmt.Printf("[click] win=0x%x button=%d at (%d,%d) in-window\n",
				e.Event, e.Detail, e.EventX, e.EventY)
		}).Connect(X, w)
	}

	xevent.Main(X)
}

// ---------------------------------------------------------------- reporting

func reportCompositor(X *xgbutil.XUtil) bool {
	// _NET_WM_CM_S<screen> having a selection owner is the conventional
	// "a compositing manager is running" probe. The design's fallback logic
	// depends on this being true under picom -- verify, don't assume.
	name := fmt.Sprintf("_NET_WM_CM_S%d", X.Conn().DefaultScreen)
	atom, err := xproto.InternAtom(X.Conn(), false, uint16(len(name)), name).Reply()
	if err != nil {
		fmt.Printf("  compositor: InternAtom failed: %v\n", err)
		return false
	}
	owner, err := xproto.GetSelectionOwner(X.Conn(), atom.Atom).Reply()
	if err != nil {
		fmt.Printf("  compositor: GetSelectionOwner failed: %v\n", err)
		return false
	}
	if owner.Owner == 0 {
		fmt.Printf("  compositor: %s has NO owner -> detection would say 'no compositor'\n", name)
		return false
	}
	fmt.Printf("  compositor: %s owned by 0x%x -> detected OK\n", name, owner.Owner)
	return true
}

func reportVisual(X *xgbutil.XUtil) (xproto.Visualid, bool) {
	fmt.Printf("  root depth: %d\n", X.Screen().RootDepth)
	for _, d := range X.Screen().AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				fmt.Printf("  argb visual: found depth-32 TrueColor visual 0x%x "+
					"(masks r=%#x g=%#x b=%#x)\n",
					v.VisualId, v.RedMask, v.GreenMask, v.BlueMask)
				return v.VisualId, true
			}
		}
	}
	fmt.Println("  argb visual: NONE (no depth-32 TrueColor visual)")
	return 0, false
}

func reportShape(X *xgbutil.XUtil) bool {
	if err := shape.Init(X.Conn()); err != nil {
		fmt.Printf("  shape ext: unavailable: %v\n", err)
		return false
	}
	v, err := shape.QueryVersion(X.Conn()).Reply()
	if err != nil {
		fmt.Printf("  shape ext: QueryVersion failed: %v\n", err)
		return false
	}
	fmt.Printf("  shape ext: v%d.%d available\n", v.MajorVersion, v.MinorVersion)
	return true
}

type geom struct{ x, y, w, h int }

func screenGeom(X *xgbutil.XUtil) geom {
	s := X.Screen()
	return geom{0, 0, int(s.WidthInPixels), int(s.HeightInPixels)}
}

// ---------------------------------------------------------------- windows

// newARGBWindow creates an override-redirect window on a depth-32 visual.
// Depth-32 windows REQUIRE an explicit BorderPixel and Colormap; inheriting
// the root's causes BadMatch. That constraint is the main reason xgbutil's
// xgraphics helper cannot be reused here -- it hardcodes RootDepth.
func newARGBWindow(X *xgbutil.XUtil, visual xproto.Visualid, x, y, w, h int) (xproto.Window, error) {
	cmap, err := xproto.NewColormapId(X.Conn())
	if err != nil {
		return 0, err
	}
	if err := xproto.CreateColormapChecked(X.Conn(), xproto.ColormapAllocNone,
		cmap, X.RootWin(), visual).Check(); err != nil {
		return 0, fmt.Errorf("create colormap: %w", err)
	}
	win, err := xproto.NewWindowId(X.Conn())
	if err != nil {
		return 0, err
	}
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	vals := []uint32{
		0, // BackPixel: fully transparent
		0, // BorderPixel: required for depth 32
		1, // OverrideRedirect: the WM must not manage or tile this
		uint32(xproto.EventMaskExposure | xproto.EventMaskButtonPress),
		uint32(cmap),
	}
	if err := xproto.CreateWindowChecked(X.Conn(), 32, win, X.RootWin(),
		int16(x), int16(y), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, visual, mask, vals).Check(); err != nil {
		return 0, fmt.Errorf("create window: %w", err)
	}
	decorate(X, win)
	if err := xproto.MapWindowChecked(X.Conn(), win).Check(); err != nil {
		return 0, fmt.Errorf("map: %w", err)
	}
	return win, nil
}

func newRootDepthWindow(X *xgbutil.XUtil, x, y, w, h int) (xproto.Window, error) {
	win, err := xproto.NewWindowId(X.Conn())
	if err != nil {
		return 0, err
	}
	mask := uint32(xproto.CwBackPixel | xproto.CwOverrideRedirect | xproto.CwEventMask)
	vals := []uint32{0, 1, uint32(xproto.EventMaskExposure | xproto.EventMaskButtonPress)}
	if err := xproto.CreateWindowChecked(X.Conn(), X.Screen().RootDepth, win,
		X.RootWin(), int16(x), int16(y), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, X.Screen().RootVisual, mask, vals).Check(); err != nil {
		return 0, err
	}
	decorate(X, win)
	if err := xproto.MapWindowChecked(X.Conn(), win).Check(); err != nil {
		return 0, err
	}
	return win, nil
}

// decorate sets the same EWMH hints the real x11 backend uses. The dock type
// matters here: picom's wintypes config gives dock windows a shadow, which is
// the interaction this probe exists to investigate.
func decorate(X *xgbutil.XUtil, win xproto.Window) {
	_ = ewmh.WmWindowTypeSet(X, win, []string{"_NET_WM_WINDOW_TYPE_DOCK"})
	_ = ewmh.WmStateSet(X, win, []string{"_NET_WM_STATE_ABOVE", "_NET_WM_STATE_STICKY"})
	_ = ewmh.WmDesktopSet(X, win, ^uint(0))
	_ = ewmh.WmNameSet(X, win, "visorprobe")
}

func setShape(X *xgbutil.XUtil, win xproto.Window, kind shape.Kind, rects []xproto.Rectangle) error {
	return shape.RectanglesChecked(X.Conn(), shape.SoSet, kind,
		xproto.ClipOrderingUnsorted, win, 0, 0, rects).Check()
}

// paint uploads img to a pixmap of the given depth and installs it as the
// window's background, then clears to show it. Background-pixmap rather than
// CopyArea-on-Expose keeps the probe short; the real backend already uses the
// same approach.
func paint(X *xgbutil.XUtil, win xproto.Window, img *image.RGBA, depth byte) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	pix, err := xproto.NewPixmapId(X.Conn())
	if err != nil {
		return err
	}
	if err := xproto.CreatePixmapChecked(X.Conn(), depth, pix,
		xproto.Drawable(win), uint16(w), uint16(h)).Check(); err != nil {
		return fmt.Errorf("create pixmap depth %d: %w", depth, err)
	}
	gc, err := xproto.NewGcontextId(X.Conn())
	if err != nil {
		return err
	}
	if err := xproto.CreateGCChecked(X.Conn(), gc, xproto.Drawable(pix),
		0, nil).Check(); err != nil {
		return fmt.Errorf("create gc: %w", err)
	}

	// X wants ZPixmap BGRA on little-endian. Go's image/RGBA is already
	// alpha-premultiplied, which is what a composited ARGB visual expects.
	buf := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			o := (y*w + x) * 4
			buf[o+0] = c.B
			buf[o+1] = c.G
			buf[o+2] = c.R
			buf[o+3] = c.A
		}
	}
	if err := xproto.PutImageChecked(X.Conn(), xproto.ImageFormatZPixmap,
		xproto.Drawable(pix), gc, uint16(w), uint16(h), 0, 0, 0, depth, buf).Check(); err != nil {
		return fmt.Errorf("put image: %w", err)
	}
	if err := xproto.ChangeWindowAttributesChecked(X.Conn(), win,
		xproto.CwBackPixmap, []uint32{uint32(pix)}).Check(); err != nil {
		return fmt.Errorf("set bg pixmap: %w", err)
	}
	xproto.ClearArea(X.Conn(), false, win, 0, 0, uint16(w), uint16(h))
	return nil
}

// ---------------------------------------------------------------- drawing

// drawCapsule renders the proposed capsule with gg: left-rounded pill, three
// stop vertical gradient, specular hairline along the top, and optionally our
// own blurred drop shadow. This is also a smoke test that gg gives us the
// anti-aliased result the hand-rolled image/draw layer cannot.
func drawCapsule(hex string, ownShadow bool, glyph string) *image.RGBA {
	dc := gg.NewContext(bufW, bufH)

	// The capsule spans x=shadowPad..bufW (right edge runs off the screen edge,
	// so only the left corners are rounded) and y=shadowPad..shadowPad+contentH.
	cx, cy := float64(shadowPad), float64(shadowPad)
	cw, ch := float64(capsuleW), float64(contentH)

	if ownShadow {
		sh := gg.NewContext(bufW, bufH)
		sh.DrawRoundedRectangle(cx, cy+1, cw+radius, ch, radius)
		sh.SetRGBA(0, 0, 0, 0.55)
		sh.Fill()
		blurred := boxBlurAlpha(sh.Image().(*image.RGBA), 3)
		dc.DrawImage(blurred, 0, 0)
	}

	base := parseHex(hex)
	top := lighten(base, 0.22)
	bot := darken(base, 0.14)

	grad := gg.NewLinearGradient(0, cy, 0, cy+ch)
	grad.AddColorStop(0, top)
	grad.AddColorStop(0.62, base)
	grad.AddColorStop(1, bot)

	// radius is added to the width so the right-hand corners fall outside the
	// buffer and stay square, matching a capsule flush to the screen edge.
	dc.DrawRoundedRectangle(cx, cy, cw+radius, ch, radius)
	dc.SetFillStyle(grad)
	dc.Fill()

	// 1px specular hairline inset along the top edge.
	dc.DrawRoundedRectangle(cx+0.5, cy+0.5, cw+radius, ch-1, radius)
	dc.SetRGBA(1, 1, 1, 0.30)
	dc.SetLineWidth(1)
	dc.Stroke()

	dc.SetFontFace(basicfont.Face7x13)
	if luminance(base) > 140 {
		dc.SetRGBA(0.06, 0.08, 0.11, 0.85)
	} else {
		dc.SetRGBA(0.90, 0.93, 0.96, 0.9)
	}
	dc.DrawStringAnchored(glyph, cx+cw/2, cy+ch/2, 0.5, 0.4)

	return dc.Image().(*image.RGBA)
}

// boxBlurAlpha runs three box-blur passes (a decent gaussian approximation)
// over a premultiplied RGBA image. Cheap enough at 23x54 that the 30Hz loop
// will not notice; a real implementation would use a proper stack blur.
func boxBlurAlpha(src *image.RGBA, r int) *image.RGBA {
	cur := src
	for pass := 0; pass < 3; pass++ {
		cur = boxBlurOnce(cur, r)
	}
	return cur
}

func boxBlurOnce(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa, n int
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					px, py := x+dx, y+dy
					if px < 0 || py < 0 || px >= w || py >= h {
						continue
					}
					c := src.RGBAAt(px, py)
					sr += int(c.R)
					sg += int(c.G)
					sb += int(c.B)
					sa += int(c.A)
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n),
			})
		}
	}
	return dst
}

func parseHex(s string) color.RGBA {
	var r, g, b uint8
	fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return color.RGBA{r, g, b, 0xff}
}

func lighten(c color.RGBA, f float64) color.RGBA {
	return color.RGBA{
		uint8(math.Min(255, float64(c.R)+(255-float64(c.R))*f)),
		uint8(math.Min(255, float64(c.G)+(255-float64(c.G))*f)),
		uint8(math.Min(255, float64(c.B)+(255-float64(c.B))*f)),
		c.A,
	}
}

func darken(c color.RGBA, f float64) color.RGBA {
	return color.RGBA{
		uint8(float64(c.R) * (1 - f)),
		uint8(float64(c.G) * (1 - f)),
		uint8(float64(c.B) * (1 - f)),
		c.A,
	}
}

func luminance(c color.RGBA) int {
	return (int(c.R)*299 + int(c.G)*587 + int(c.B)*114) / 1000
}
