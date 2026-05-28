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
// the window is *always* expandedW wide. We anchor its right edge well
// past the screen edge so only the leftmost tabW pixels are visible.
// Hover = slide leftward; collapse = slide back. Width never changes,
// so the rendered image (bg + cwd text) stays intact across states.
//
// Layout of the rendered image (window-relative X):
//
//	0 .. tabW        : pure bg color — this is what shows as the "tab"
//	tabW .. textPad  : padding gap between tab and text
//	textPad .. expandedW: cwd text
const (
	tabW   = render.TabW
	tabH   = render.TabH
	expandedW = render.ExpandedW
	textPad   = render.TextPad
	fontPt    = render.FontPt
)

// Wobble animation for "working" tabs. We oscillate leftward (never
// rightward — that would push the window past the screen edge) with cosine
// easing. Each tab gets a randomized phase so they breathe independently.
const (
	wobbleAmp    = 4.0
	wobblePeriod = 0.9 // seconds for one full cycle
)

// alertProtrusion: when a session is attention=needs it sticks out this
// many px past the collapsed rest position. Picked > wobbleAmp so a needs
// tab is unambiguously further left than any working tab at its
// wobble peak. The user can spot "you need to do something here" by
// shape alone, not just color.
const alertProtrusion = 8

type tabOpts struct {
	x, y     int    // absolute X / Y on the root (current position)
	rightX   int    // x coordinate of the screen edge (mon.x + mon.w)
	color    uint32 // 0xRRGGBB
	expanded bool
}

// tab is one X11 window representing one Claude session.
type tab struct {
	X    *xgbutil.XUtil
	win  *xwindow.Window
	opt  tabOpts
	sess sessionView

	font *truetype.Font // shared with the dock; may be nil if loadFont failed

	wobblePhase float64
	wobbleStart time.Time

	// xgraphics image used as the window's background pixmap when expanded.
	// We retain it so we can free the X pixmap explicitly on collapse.
	expandedImg *xgraphics.Image

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

// update repositions and recolors the tab if anything changed.
// Reusing the same X window across updates is much cheaper than
// destroy+create, and avoids brief visual flicker. The rendered image
// is regenerated only when color or text changed.
func (t *tab) update(s sessionView, y int, color uint32) {
	prevSess := t.sess
	prevColor := t.opt.color
	t.sess = s
	if y != t.opt.y {
		t.opt.y = y
		t.win.Move(t.x(), y)
	}
	if color != t.opt.color {
		t.opt.color = color
	}
	if color != prevColor || displayLabel(s) != displayLabel(prevSess) {
		t.render()
	}
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
	base := t.opt.rightX - tabW
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
	win, err := xwindow.Generate(X)
	if err != nil {
		return nil, err
	}

	opt.rightX = mon.x + mon.w
	opt.x = opt.rightX - tabW
	bgPixel := opt.color & 0x00_ff_ff_ff // 24-bit colour (no alpha on default visual)

	// The window is always expandedW wide; only its X position changes
	// between states. The off-screen-right portion is clipped by X.
	if err := win.CreateChecked(
		X.RootWin(),
		opt.x, opt.y, expandedW, tabH,
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
	// First render happens once the font is wired in by the dock (it's
	// assigned right after newTab returns). The dock calls render()
	// explicitly for the initial draw.
	return t, nil
}

func (t *tab) destroy() {
	t.hideTooltip()
	if t.expandedImg != nil {
		t.expandedImg.Destroy()
		t.expandedImg = nil
	}
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
		newX = t.opt.rightX - expandedW
	} else {
		newX = t.restX()
	}
	t.opt.x = newX
	t.win.Move(newX, t.opt.y)

	if expand && t.overflow {
		t.showTooltip()
	} else {
		t.hideTooltip()
	}
}

// Tooltip layout constants.
const (
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
	if t.font == nil || t.tooltipWin != nil {
		return
	}
	text := displayLabel(t.sess)
	textW, textH := xgraphics.Extents(t.font, fontPt, text)
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
		y = t.opt.y + tabH + tipGapY
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
	_, _, _ = im.Text(tipPadX, tipPadY, color.RGBA{0xe5, 0xe9, 0xf0, 0xff}, fontPt, t.font, text)
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

// render generates the full expanded panel (bg color + cwd text) and
// installs it as the window's background pixmap. Called once after
// font assignment and whenever color or text changes.
func (t *tab) render() {
	if t.expandedImg != nil {
		t.expandedImg.Destroy()
		t.expandedImg = nil
	}

	rt := render.DrawTab(render.TabState{
		Color:    t.opt.color,
		Label:    displayLabel(t.sess),
		Expanded: true, // x11 uses positional window-slide; always render full opaque panel
	}, t.font)
	t.overflow = rt.Overflow

	// Wrap the RGBA into an xgraphics.Image for X upload. xgraphics.Image
	// stores pixels in BGRA order, so we must swap R and B channels rather
	// than doing a raw copy.
	im := xgraphics.New(t.X, rt.RGBA.Bounds())
	src := rt.RGBA.Pix
	dst := im.Pix
	if len(src) != len(dst) {
		panic("render: src/dst pixel buffer size mismatch")
	}
	// RGBA (image.RGBA) → BGRA (xgraphics.Image), preserving alpha.
	for i := 0; i < len(src); i += 4 {
		dst[i+0] = src[i+2] // B
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i+0] // R
		dst[i+3] = src[i+3] // A
	}

	im.CreatePixmap()
	im.XDraw()
	im.XSurfaceSet(t.win.Id)
	xproto.ClearArea(t.X.Conn(), false, t.win.Id, 0, 0, expandedW, tabH)
	t.expandedImg = im
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
