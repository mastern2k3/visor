package x11

import (
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"

	"github.com/nitzanz/visor/internal/hud/render"
)

// catchWindow is one row's armed-browsing pointer sensor: a window covering the
// full column width at that row's height, used to notice the cursor where the
// row's own tab window cannot reach it.
//
// Why these exist. An XShape input region is clipped to its own window, and a
// collapsed tab's window sits at rightX-collapsedVisibleW with everything past
// the capsule hanging off-screen right. So a collapsed tab is physically
// incapable of being sensitive to a cursor 300px inland, where the expanded
// panel is — something else must see that cursor.
//
// They are InputOnly (X11 window class 2): no depth, no visual, no colormap, no
// pixels, nothing drawn ever. That sidesteps the entire ARGB/compositor story
// the tab windows have to deal with (see argb.go) — an InputOnly window has no
// contents to composite, so it behaves identically with and without a
// compositing manager.
//
// They are mapped only while browsing is armed. Unmapped, they are completely
// inert, so the dock's resting behaviour is exactly what it was before: only the
// tabs' own edge strips are sensitive.
//
// WHY ONE PER ROW, and not one window spanning the whole column: event volume.
// A single window would have to select PointerMotion to notice the cursor
// changing rows within it, and motion delivers hundreds of events per second.
// That saturates xgb's 100-slot event channel, and once it is full, ANY checked
// (synchronous) X request issued from inside an xevent callback deadlocks the
// process: the callback waits for a reply that xgb's reader cannot deliver
// because it is blocked trying to push an event into the full channel, which
// only the callback's own goroutine drains. setExpanded makes exactly such a
// checked request (shape.RectanglesChecked) on every hover. One window per row
// reduces a row change to a single Leave plus a single Enter, so the channel
// never fills, and the row is identified by *which* window was entered rather
// than by hit-testing a coordinate.
type catchWindow struct {
	X  *xgbutil.XUtil
	id xproto.Window

	mapped     bool
	x, y, w, h int
}

// newCatch creates one (unmapped) catch window for a row.
//
// Creation is checked because it runs from applySnapshot on the main loop
// goroutine, not from an event callback, and a failure here is worth reporting.
// Everything after creation is deliberately unchecked — see the methods below.
func newCatch(X *xgbutil.XUtil, x, y, w, h int) (*catchWindow, error) {
	id, err := xproto.NewWindowId(X.Conn())
	if err != nil {
		return nil, err
	}
	// InputOnly windows take depth 0 and visual 0 (CopyFromParent), and the only
	// attributes X permits on them are win-gravity, event-mask,
	// do-not-propagate-mask, override-redirect and cursor — notably not
	// background or border pixel, which is why none of the depth-32 colormap
	// dance in argb.go applies here.
	//
	// Value list order follows the numeric order of the mask bits:
	// CwOverrideRedirect (0x200) then CwEventMask (0x800).
	//
	// PointerMotion is deliberately NOT selected. See the type comment.
	if err := xproto.CreateWindowChecked(
		X.Conn(),
		0, // depth
		id,
		X.RootWin(),
		int16(x), int16(y), uint16(w), uint16(h),
		0, // border width: must be 0 for InputOnly
		xproto.WindowClassInputOnly,
		0, // visual: CopyFromParent
		xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{
			1,
			uint32(xproto.EventMaskEnterWindow | xproto.EventMaskLeaveWindow),
		},
	).Check(); err != nil {
		return nil, err
	}
	return &catchWindow{X: X, id: id, x: x, y: y, w: w, h: h}, nil
}

// The mutators below are all UNCHECKED, and must stay that way: every one of
// them is reachable from an xevent callback (via dock.applyBrowse), and a
// checked request there can deadlock the process against xgb's event channel —
// see the type comment for the mechanism. Unchecked X errors surface
// asynchronously on the error handler instead, which is the right trade for
// requests that are best-effort anyway: the worst case for a dropped map or
// move is that one row stops catching the cursor.

// moveResize repositions the catch window over its row.
func (c *catchWindow) moveResize(x, y, w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if c.x == x && c.y == y && c.w == w && c.h == h {
		return
	}
	c.x, c.y, c.w, c.h = x, y, w, h
	xproto.ConfigureWindow(
		c.X.Conn(), c.id,
		xproto.ConfigWindowX|xproto.ConfigWindowY|
			xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(int32(x)), uint32(int32(y)), uint32(w), uint32(h)},
	)
}

// stackBelow puts the catch window immediately below sibling, which must be its
// row's tab window.
//
// The sibling matters, and getting this wrong is invisible until you measure it.
// A bare StackModeBelow with no sibling drops the window to the bottom of the
// *global* stacking order — beneath every other application's windows, not just
// beneath visor's own. The catch window then receives pointer events only where
// nothing else covers it, which on a populated desktop is nowhere: the pointer
// inland lands in whatever terminal or browser happens to be under the dock.
// (The tabs are unaffected by this because they are override-redirect dock
// windows sitting on top of everything.)
//
// Stacking relative to the row's own tab is both correct and sufficient: rows
// occupy distinct y bands, so a catch window overlaps exactly one tab — its
// own. Being below it keeps the tab's edge strip winning hover and clicks, and
// keeps the expanded tab receiving its own events, while inheriting the tab's
// position above other clients.
func (c *catchWindow) stackBelow(sibling xproto.Window) {
	// Value order follows the mask bits: Sibling (0x20) then StackMode (0x40).
	xproto.ConfigureWindow(
		c.X.Conn(), c.id,
		xproto.ConfigWindowSibling|xproto.ConfigWindowStackMode,
		[]uint32{uint32(sibling), xproto.StackModeBelow},
	)
}

func (c *catchWindow) show() {
	if c.mapped {
		return
	}
	c.mapped = true
	xproto.MapWindow(c.X.Conn(), c.id)
}

func (c *catchWindow) hide() {
	if !c.mapped {
		return
	}
	c.mapped = false
	xproto.UnmapWindow(c.X.Conn(), c.id)
}

func (c *catchWindow) destroy() {
	if c.id != 0 {
		xproto.DestroyWindow(c.X.Conn(), c.id)
		c.id = 0
	}
}

// catchRowRect is the pure geometry of one row's catch window, in root
// coordinates. Extracted from the dock so it can be tested without an X
// connection, like tickX and expandedX.
//
// The rect is exactly as wide as a tab's buffer, which is also how far a tab
// slides when it expands, so its left edge is the leftmost pixel any panel ever
// reaches. It is a full RowPitch tall, so adjacent rows' catch windows abut with
// no gap for the cursor to fall into — a row's shadow padding belongs to that
// row rather than being a dead band.
func catchRowRect(mon monitor, rowTop int) (x, y, w, h int) {
	return mon.x + mon.w - bufW, rowTop, bufW, render.RowPitch
}
