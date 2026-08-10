package x11

import (
	"image"
	"image/color"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/xevent"
	"github.com/jezek/xgbutil/xgraphics"
	"github.com/jezek/xgbutil/xwindow"

	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
)

// Window dimensions and visibility regions.
//
// Instead of resizing the window between "narrow tab" and "wide panel",
// the window is *always* the full rendered buffer wide. We anchor its right
// edge well past the screen edge so only the leftmost collapsedVisibleW
// pixels are visible. Hover = slide leftward; collapse = slide back. Width
// never changes, so the rendered image stays intact across states.
//
// Layout of the rendered image (window-relative X) — see render.DrawTab:
//
//	0 .. ShadowPad             : transparent shadow padding
//	ShadowPad .. +CapsuleW     : the capsule
//	+CapsuleW .. BufW          : the panel
const (
	bufW = render.BufW
	bufH = render.BufH
	// collapsedVisibleW is how much of the buffer stays on screen at rest:
	// the capsule plus the shadow padding to its left. The panel and the
	// capsule's own width are no longer the same number, so the slide
	// arithmetic can't use capsuleW alone.
	collapsedVisibleW = render.ShadowPad + render.CapsuleW
)

// Animation constants now come from render so both backends share them.
// Working tabs oscillate leftward (never rightward — that would push the
// window past the screen edge) with cosine easing; each tab gets a randomized
// phase so they breathe independently. A tab with attention=needs sits
// alertProtrusion px further left than its collapsed rest position, so the
// user can spot "you need to do something here" by shape alone.
const (
	wobbleAmp       = render.WobbleAmp
	wobblePeriod    = render.WobblePeriod
	alertProtrusion = render.AlertProtrusion
)

type tabOpts struct {
	x, y   int // absolute X / Y on the root (current position)
	rightX int // x coordinate of the screen edge (mon.x + mon.w)
	// color is the window's background pixel, read once at creation — see
	// dock.bgPixel. Tab colour itself lives in the rendered buffer now, so
	// this is not updated when state changes.
	color    uint32 // 0xRRGGBB
	expanded bool

	// argb/visual are copied from the dock's one-time capability detection.
	// argb selects the depth-32 window + XShape input region path; visual is
	// only meaningful when argb is true.
	argb   bool
	visual xproto.Visualid
}

// tab is one X11 window representing one Claude session.
type tab struct {
	X    *xgbutil.XUtil
	win  *xwindow.Window
	opt  tabOpts
	sess sessionView

	// faces/palette/shadow are the render inputs, shared with the dock.
	// faces may be nil if font loading failed — DrawTab then skips all text.
	faces   *render.Faces
	palette render.Palette
	shadow  bool

	// tipFont is the freetype font used by the legacy xgraphics text paths
	// (the overflow tooltip and the help window). render.LoadFont is gone, so
	// the dock parses this one itself; it disappears when those two paths move
	// to gg + render.Faces.
	tipFont *truetype.Font

	// lastState/rendered memoize the last painted TabState so update() can
	// skip a repaint when nothing observable changed. This replaces the old
	// colour/label comparison, which cannot see the new state-derived fields.
	lastState render.TabState
	rendered  bool

	wobblePhase float64
	wobbleStart time.Time

	// overflow is set when the rendered label is wider than the panel can show.
	// When true, hovering the tab also spawns a tooltip window with the
	// full text. Recomputed on every render.
	overflow bool

	// Tooltip resources — non-nil only while shown.
	tooltipWin *xwindow.Window
	tooltipImg *xgraphics.Image

	// clickFn, when non-nil, replaces the default IPC click dispatch.
	// Used by the synthetic help tab to toggle the help window instead.
	clickFn func(button byte)
}

// update repositions and repaints the tab if anything changed.
// Reusing the same X window across updates is much cheaper than
// destroy+create, and avoids brief visual flicker. render() itself is a no-op
// when the rendered state is unchanged.
func (t *tab) update(s sessionView, y int) {
	t.sess = s
	if y != t.opt.y {
		t.opt.y = y
		t.win.Move(t.x(), y)
	}
	t.render()
}

// x returns the X coordinate of the right-anchored tab.
func (t *tab) x() int {
	// Cached on the window — we don't refetch screen geometry every update.
	return t.opt.x
}

// restX returns the collapsed resting position for the tab. Sessions
// needing attention sit further left so they're visible at a glance even
// without inspecting color.
func (t *tab) restX() int {
	base := t.opt.rightX - collapsedVisibleW
	if t.sess.Attention == "needs" {
		return base - alertProtrusion
	}
	return base
}

// tick is called by the dock's animation loop. Working tabs wobble
// leftward; everything else snaps back to rest if it was previously moved.
func (t *tab) tick(now time.Time) {
	if t.opt.expanded {
		return // hover takes priority; nothing to animate
	}
	rest := t.restX()
	if t.sess.Activity != "working" {
		if t.opt.x != rest {
			t.opt.x = rest
			t.win.Move(rest, t.opt.y)
		}
		return
	}
	// Cosine eases naturally — zero velocity at the endpoints, max speed
	// in the middle. (1 - cos)/2 maps to [0, 1] so the offset stays leftward.
	elapsed := now.Sub(t.wobbleStart).Seconds()
	t01 := (1 - math.Cos(elapsed*2*math.Pi/wobblePeriod+t.wobblePhase)) / 2
	offset := -int(math.Round(wobbleAmp * t01))
	newX := rest + offset
	if newX != t.opt.x {
		t.opt.x = newX
		t.win.Move(newX, t.opt.y)
	}
}

func newTab(X *xgbutil.XUtil, mon monitor, opt tabOpts) (*tab, error) {
	opt.rightX = mon.x + mon.w
	opt.x = opt.rightX - collapsedVisibleW

	// The window is always bufW wide; only its X position changes
	// between states. The off-screen-right portion is clipped by X.
	var win *xwindow.Window
	if opt.argb {
		w, err := newARGBWindow(X, opt.visual, opt.x, opt.y, bufW, bufH)
		if err != nil {
			return nil, err
		}
		win = w
	} else {
		w, err := xwindow.Generate(X)
		if err != nil {
			return nil, err
		}
		// Root depth: no alpha, so the background pixel is the best stand-in
		// for the capsule colour until the first pixmap paint lands.
		bgPixel := opt.color & 0x00_ff_ff_ff
		if err := w.CreateChecked(
			X.RootWin(),
			opt.x, opt.y, bufW, bufH,
			xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
			bgPixel,
			1, // override-redirect = true
			uint32(xproto.EventMaskButtonPress|
				xproto.EventMaskEnterWindow|
				xproto.EventMaskLeaveWindow|
				xproto.EventMaskExposure),
		); err != nil {
			return nil, err
		}
		win = w
	}

	// EWMH hints so cooperative WMs still treat it sensibly (dock-type,
	// always-on-top, sticky across workspaces). Override-redirect bypasses
	// most of these, but they're cheap and improve behaviour under WMs
	// that respect them anyway.
	if err := ewmh.WmWindowTypeSet(X, win.Id, []string{"_NET_WM_WINDOW_TYPE_DOCK"}); err != nil {
		win.Destroy()
		return nil, err
	}
	if err := ewmh.WmStateSet(X, win.Id, []string{
		"_NET_WM_STATE_ABOVE",
		"_NET_WM_STATE_STICKY",
		"_NET_WM_STATE_SKIP_TASKBAR",
		"_NET_WM_STATE_SKIP_PAGER",
	}); err != nil {
		win.Destroy()
		return nil, err
	}
	if err := ewmh.WmNameSet(X, win.Id, "visor-tab"); err != nil {
		win.Destroy()
		return nil, err
	}

	t := &tab{
		X:           X,
		win:         win,
		opt:         opt,
		wobblePhase: rand.Float64() * 2 * math.Pi,
		wobbleStart: time.Now(),
	}

	xevent.ButtonPressFun(t.onButton).Connect(X, win.Id)
	xevent.EnterNotifyFun(t.onEnter).Connect(X, win.Id)
	xevent.LeaveNotifyFun(t.onLeave).Connect(X, win.Id)

	win.Map()
	// First render happens once faces/palette are wired in by the dock (they
	// are assigned right after newTab returns). The dock calls render()
	// explicitly for the initial draw.
	return t, nil
}

func (t *tab) destroy() {
	t.hideTooltip()
	if t.win != nil {
		t.win.Destroy()
		t.win = nil
	}
}

// Button codes (xproto.ButtonMask is a mask; the field on the event is byte).
const (
	btnLeft   = 1
	btnMiddle = 2
	btnRight  = 3
)

func (t *tab) onButton(X *xgbutil.XUtil, ev xevent.ButtonPressEvent) {
	if t.clickFn != nil {
		t.clickFn(byte(ev.Detail))
		return
	}
	cmd := ""
	switch ev.Detail {
	case btnLeft:
		cmd = "jump"
	case btnRight:
		cmd = "dismiss"
	case btnMiddle:
		cmd = "ack"
	}
	if cmd == "" || t.sess.ID == "" {
		return
	}
	// Fire-and-forget. We don't want to block the X event loop on socket I/O,
	// so dispatch in a goroutine.
	go func(c, id string) {
		_, err := ipc.Call(paths.Socket(), ipc.Request{Cmd: c, ID: id})
		if err != nil {
			slog.Warn("ipc", "cmd", c, "err", err)
		}
	}(cmd, t.sess.ID)
}

func (t *tab) onEnter(X *xgbutil.XUtil, ev xevent.EnterNotifyEvent) {
	t.setExpanded(true)
}

func (t *tab) onLeave(X *xgbutil.XUtil, ev xevent.LeaveNotifyEvent) {
	// Ignore Leave events caused by entering a child / re-entering inferior.
	// Without this, the panel collapses spuriously when the cursor crosses
	// internal sub-region boundaries (relevant once we add child widgets).
	if ev.Detail == xproto.NotifyDetailInferior {
		return
	}
	t.setExpanded(false)
}

// setExpanded slides the window between its collapsed and expanded
// positions. Width is constant (expandedW); only X changes. The wobble
// animation also reads this state — wobble is suppressed when expanded.
func (t *tab) setExpanded(expand bool) {
	if t.opt.expanded == expand {
		return
	}
	t.opt.expanded = expand
	var newX int
	if expand {
		newX = t.opt.rightX - bufW
	} else {
		newX = t.restX()
	}
	t.opt.x = newX

	// The input region is state-dependent — the panel is only clickable while
	// expanded — so it has to change alongside the move. The ORDER of the two
	// matters, and getting it wrong produces a hover flicker loop:
	//
	// The pointer does not move during the slide, but the window does, so the
	// buffer coordinate under the pointer changes by the full slide distance.
	// Expanding slides 282px left, which puts the pointer over the panel region
	// of the buffer. If the region is still capsule-only at that instant the
	// pointer is outside it, X delivers LeaveNotify, we collapse, the pointer is
	// back over the capsule, EnterNotify fires, and the tab oscillates.
	//
	// The invariant that avoids it: the input region must never be smaller than
	// what is under the pointer mid-transition, i.e. it must be a superset of
	// both the before and after pointer locations. So grow it before moving and
	// shrink it after moving:
	//
	//	expand:   setInputRegion(capsule+panel) -> Move left
	//	collapse: Move right -> setInputRegion(capsule)
	//
	// t.opt.expanded is already assigned above, so inputRects() describes the
	// target state in both branches.
	reshape := func() {
		if !t.opt.argb {
			return
		}
		if err := setInputRegion(t.X, t.win.Id, t.inputRects()); err != nil {
			slog.Warn("x11 tab input shape", "id", t.sess.ID, "err", err)
		}
	}
	if expand {
		reshape()
		t.win.Move(newX, t.opt.y)
	} else {
		t.win.Move(newX, t.opt.y)
		reshape()
	}

	if expand && t.overflow {
		t.showTooltip()
	} else {
		t.hideTooltip()
	}
}

// Tooltip layout constants.
const (
	// tipFontPt matches render.NamePt: the tooltip shows the same string as
	// the panel's name line, so the two should be the same size.
	tipFontPt = render.NamePt

	tipPadX   = 10
	tipPadY   = 5
	tipGapY   = 4 // gap between tooltip and expanded panel
	tipBg     = 0x14_18_22
	tipBorder = 0x33_38_45
)

// showTooltip pops up a small floating window above the expanded panel
// containing the full label. We render once per show; on collapse it's
// destroyed (cheaper than maintaining a hidden window).
func (t *tab) showTooltip() {
	if t.tipFont == nil || t.tooltipWin != nil {
		return
	}
	text := displayLabel(t.sess)
	textW, textH := xgraphics.Extents(t.tipFont, tipFontPt, text)
	w := textW + 2*tipPadX
	h := textH + 2*tipPadY

	// Anchor: right edge aligned with the screen's right edge, so the tooltip
	// reads naturally toward the panel. Sit above the panel; if that would
	// go off-screen, sit below instead.
	x := t.opt.rightX - w - 2
	y := t.opt.y - h - tipGapY
	// Fall back below if it would clip the top of the monitor.
	// (We don't know the screen origin here; assume y >= 0 means OK.)
	if y < 0 {
		y = t.opt.y + bufH + tipGapY
	}

	win, err := xwindow.Generate(t.X)
	if err != nil {
		return
	}
	if err := win.CreateChecked(
		t.X.RootWin(),
		x, y, w, h,
		xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		uint32(tipBg),
		1,
		uint32(xproto.EventMaskExposure),
	); err != nil {
		return
	}
	ewmh.WmWindowTypeSet(t.X, win.Id, []string{"_NET_WM_WINDOW_TYPE_TOOLTIP"})
	ewmh.WmStateSet(t.X, win.Id, []string{
		"_NET_WM_STATE_ABOVE",
		"_NET_WM_STATE_STICKY",
		"_NET_WM_STATE_SKIP_TASKBAR",
		"_NET_WM_STATE_SKIP_PAGER",
	})

	im := xgraphics.New(t.X, image.Rect(0, 0, w, h))
	bg := rgba(tipBg)
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			im.Set(xx, yy, bg)
		}
	}
	// 1-px subtle border via overlapping rectangles of border colour.
	border := rgba(tipBorder)
	for xx := 0; xx < w; xx++ {
		im.Set(xx, 0, border)
		im.Set(xx, h-1, border)
	}
	for yy := 0; yy < h; yy++ {
		im.Set(0, yy, border)
		im.Set(w-1, yy, border)
	}
	_, _, _ = im.Text(tipPadX, tipPadY, color.RGBA{0xe5, 0xe9, 0xf0, 0xff}, tipFontPt, t.tipFont, text)
	im.CreatePixmap()
	im.XDraw()
	im.XSurfaceSet(win.Id)
	win.Map()

	t.tooltipWin = win
	t.tooltipImg = im
}

func (t *tab) hideTooltip() {
	if t.tooltipImg != nil {
		t.tooltipImg.Destroy()
		t.tooltipImg = nil
	}
	if t.tooltipWin != nil {
		t.tooltipWin.Destroy()
		t.tooltipWin = nil
	}
}

// tabState builds the renderer input for the current session state.
//
// Expanded is always true: x11 reveals and hides the panel by sliding the
// window, not by re-rendering it.
//
// Glyph and Elapsed stay zero for now — the daemon does not yet publish
// glyph/state_since (Task 7) and sessionView does not yet carry them (Task 8).
func (t *tab) tabState() render.TabState {
	name := displayLabel(t.sess)
	// displayLabel falls back to DisplayCWD when there is no title, so drop
	// the path in that case rather than printing it twice.
	path := t.sess.DisplayCWD
	if path == name {
		path = ""
	}
	return render.TabState{
		Activity:  t.sess.Activity,
		Attention: t.sess.Attention,
		Waiting:   t.sess.Waiting,
		Name:      name,
		Path:      path,
		Expanded:  true,
		// Both of these hang off alpha. Without a compositor the padding around
		// the capsule is opaque black, so a shadow drawn into it is a black box
		// and an antialiased corner is a black notch — square and shadowless is
		// the only honest rendering.
		Shadow:            t.shadow && t.opt.argb,
		Square:            !t.opt.argb,
		BackgroundRunning: t.sess.BackgroundRunning,
		BackgroundOutcome: t.sess.BackgroundOutcome,
	}
}

// render generates the full expanded buffer (capsule + panel + text) and
// installs it as the window's background pixmap. Called once after faces are
// assigned and whenever the rendered state changes.
func (t *tab) render() {
	st := t.tabState()
	if t.rendered && st == t.lastState {
		return
	}

	rt := render.DrawTab(st, t.faces, t.palette)
	t.overflow = rt.Overflow
	t.lastState = st
	t.rendered = true

	depth := byte(32)
	if !t.opt.argb {
		depth = t.X.Screen().RootDepth
	}
	if err := uploadRGBA(t.X, t.win.Id, rt.RGBA, depth); err != nil {
		// Non-fatal: a tab that fails to paint is better than a dead dock.
		slog.Warn("x11 tab upload", "id", t.sess.ID, "err", err)
		return
	}
	if t.opt.argb {
		if err := setInputRegion(t.X, t.win.Id, t.inputRects()); err != nil {
			// Shape is best-effort; without it the padding merely eats clicks.
			slog.Warn("x11 tab input shape", "id", t.sess.ID, "err", err)
		}
	}
}

// inputRects is the clickable region: never the transparent shadow padding.
// Coordinates are window-local, matching render.DrawTab's x11 layout — the
// capsule starts ShadowPad in from the window's left edge and the panel butts
// straight onto it.
func (t *tab) inputRects() []xproto.Rectangle {
	r := []xproto.Rectangle{{
		X: render.ShadowPad, Y: render.ShadowPad,
		Width: render.CapsuleW, Height: render.ContentH,
	}}
	if t.opt.expanded {
		r = append(r, xproto.Rectangle{
			X: render.ShadowPad + render.CapsuleW, Y: render.ShadowPad,
			Width: render.ExpandedW - render.CapsuleW, Height: render.ContentH,
		})
	}
	return r
}

// displayLabel picks what to show inside the expanded tab.
// Prefer Claude's ai-title (a real session name); fall back to cwd then id.
func displayLabel(s sessionView) string {
	if s.Title != "" {
		return s.Title
	}
	if s.DisplayCWD != "" {
		return s.DisplayCWD
	}
	if len(s.ID) >= 8 {
		return s.ID[:8]
	}
	return s.ID
}

// rgba converts a packed 0xRRGGBB to a color.RGBA (opaque).
// Used by the tooltip drawing code in showTooltip.
func rgba(c uint32) color.RGBA {
	return color.RGBA{
		R: uint8((c >> 16) & 0xff),
		G: uint8((c >> 8) & 0xff),
		B: uint8(c & 0xff),
		A: 0xff,
	}
}
