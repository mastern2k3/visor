package x11

// ARGB support for the x11 backend.
//
// Depth-32 windows REQUIRE an explicit CwBorderPixel and CwColormap;
// inheriting the root's yields BadMatch. xgbutil's xgraphics helper cannot be
// used at all here — its CreatePixmap hardcodes X.Screen().RootDepth and its
// Image is depth-24 by construction — so pixel upload goes through a raw
// depth-32 pixmap + PutImage.
//
// The BOUNDING shape region is deliberately left unset. Shaping it would make
// picom's shadow follow the capsule outline, but a bounding region hard-clips
// rendering and the antialiased corners would become stair-steps. Only the
// INPUT region is shaped, so the transparent shadow padding does not eat
// clicks. Verified on LeftWM + picom: unshaped windows swallow padding
// clicks, input-shaped ones pass them through.

import (
	"fmt"
	"image"
	"sync"

	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xwindow"
)

// hasCompositor reports whether a compositing manager owns the
// _NET_WM_CM_S<screen> selection. That is the conventional probe, and it is
// the second half of what "we can use alpha" means: a depth-32 visual with
// nothing compositing it renders transparency as black.
func hasCompositor(X *xgbutil.XUtil) bool {
	name := fmt.Sprintf("_NET_WM_CM_S%d", X.Conn().DefaultScreen)
	atom, err := xproto.InternAtom(X.Conn(), false, uint16(len(name)), name).Reply()
	if err != nil {
		return false
	}
	owner, err := xproto.GetSelectionOwner(X.Conn(), atom.Atom).Reply()
	if err != nil {
		return false
	}
	return owner.Owner != 0
}

// argbVisual finds a depth-32 TrueColor visual on the default screen.
func argbVisual(X *xgbutil.XUtil) (xproto.Visualid, bool) {
	for _, d := range X.Screen().AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				return v.VisualId, true
			}
		}
	}
	return 0, false
}

// argbColormap returns a colormap for the ARGB visual, created once and shared
// by every tab window. A colormap is per-visual, not per-window, and tabs come
// and go as sessions do — creating one per window would leak an X resource on
// every session that ever appears.
var (
	cmapOnce sync.Once
	cmapID   xproto.Colormap
	cmapErr  error
)

func argbColormap(X *xgbutil.XUtil, visual xproto.Visualid) (xproto.Colormap, error) {
	cmapOnce.Do(func() {
		id, err := xproto.NewColormapId(X.Conn())
		if err != nil {
			cmapErr = err
			return
		}
		if err := xproto.CreateColormapChecked(X.Conn(), xproto.ColormapAllocNone,
			id, X.RootWin(), visual).Check(); err != nil {
			cmapErr = fmt.Errorf("create colormap: %w", err)
			return
		}
		cmapID = id
	})
	return cmapID, cmapErr
}

// newARGBWindow creates an unmapped override-redirect window on the given
// depth-32 visual. It returns an *xwindow.Window so the rest of the backend
// keeps using its existing types (Move, Destroy, event connection all work on
// a window xgbutil did not itself create — xwindow.New only wraps the id).
//
// The event mask matches the root-depth path in newTab: button presses plus
// enter/leave for hover, plus exposure.
func newARGBWindow(X *xgbutil.XUtil, visual xproto.Visualid, x, y, w, h int) (*xwindow.Window, error) {
	cmap, err := argbColormap(X, visual)
	if err != nil {
		return nil, err
	}
	id, err := xproto.NewWindowId(X.Conn())
	if err != nil {
		return nil, err
	}
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwEventMask | xproto.CwColormap)
	vals := []uint32{
		0, // BackPixel: fully transparent
		0, // BorderPixel: required for depth 32
		1, // OverrideRedirect: the WM must not manage or tile this
		uint32(xproto.EventMaskButtonPress |
			xproto.EventMaskEnterWindow |
			xproto.EventMaskLeaveWindow |
			xproto.EventMaskExposure),
		uint32(cmap),
	}
	if err := xproto.CreateWindowChecked(X.Conn(), 32, id, X.RootWin(),
		int16(x), int16(y), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, visual, mask, vals).Check(); err != nil {
		return nil, fmt.Errorf("create depth-32 window: %w", err)
	}
	return xwindow.New(X, id), nil
}

// uploadRGBA uploads img to a pixmap of the given depth and installs it as
// win's background, then clears the window so it shows.
//
// Background-pixmap rather than CopyArea-on-Expose: the backend has no Expose
// handler, and the X server repaints a window's background itself. The pixmap
// and GC are freed before returning — the X protocol explicitly permits
// freeing a background pixmap as soon as it has been installed (the server
// keeps its own reference), and not freeing it would leak BufW*BufH*4 bytes of
// server memory on every repaint.
func uploadRGBA(X *xgbutil.XUtil, win xproto.Window, img *image.RGBA, depth byte) error {
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
	defer xproto.FreePixmap(X.Conn(), pix)

	gc, err := xproto.NewGcontextId(X.Conn())
	if err != nil {
		return err
	}
	if err := xproto.CreateGCChecked(X.Conn(), gc, xproto.Drawable(pix),
		0, nil).Check(); err != nil {
		return fmt.Errorf("create gc: %w", err)
	}
	defer xproto.FreeGC(X.Conn(), gc)

	// X wants ZPixmap BGRA on little-endian. Go's image/RGBA is already
	// alpha-premultiplied, which is what a composited ARGB visual expects.
	buf := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		o := y * w * 4
		for x := 0; x < w; x++ {
			buf[o+0] = row[2] // B
			buf[o+1] = row[1] // G
			buf[o+2] = row[0] // R
			buf[o+3] = row[3] // A
			row = row[4:]
			o += 4
		}
	}
	if err := xproto.PutImageChecked(X.Conn(), xproto.ImageFormatZPixmap,
		xproto.Drawable(pix), gc, uint16(w), uint16(h), 0, 0, 0, depth,
		buf).Check(); err != nil {
		return fmt.Errorf("put image: %w", err)
	}
	if err := xproto.ChangeWindowAttributesChecked(X.Conn(), win,
		xproto.CwBackPixmap, []uint32{uint32(pix)}).Check(); err != nil {
		return fmt.Errorf("set bg pixmap: %w", err)
	}
	xproto.ClearArea(X.Conn(), false, win, 0, 0, uint16(w), uint16(h))
	return nil
}

// shapeOnce guards the one-time SHAPE extension handshake. shape.Init must run
// before any shape request; QueryVersion confirms the server really has it
// rather than only advertising the opcode.
var (
	shapeOnce sync.Once
	shapeErr  error
)

func initShape(X *xgbutil.XUtil) error {
	shapeOnce.Do(func() {
		if err := shape.Init(X.Conn()); err != nil {
			shapeErr = fmt.Errorf("shape extension unavailable: %w", err)
			return
		}
		if _, err := shape.QueryVersion(X.Conn()).Reply(); err != nil {
			shapeErr = fmt.Errorf("shape QueryVersion: %w", err)
		}
	})
	return shapeErr
}

// setInputRegion replaces win's XShape INPUT region with rects, in
// window-local coordinates. The BOUNDING region is left alone on purpose (see
// the file comment): only input is shaped, so clicks on the transparent shadow
// padding fall through to whatever is behind the tab.
func setInputRegion(X *xgbutil.XUtil, win xproto.Window, rects []xproto.Rectangle) error {
	if err := initShape(X); err != nil {
		return err
	}
	return shape.RectanglesChecked(X.Conn(), shape.SoSet, shape.SkInput,
		xproto.ClipOrderingUnsorted, win, 0, 0, rects).Check()
}
