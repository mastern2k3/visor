// Package x11 is the native X11 HUD backend.
//
// Each Claude session gets its own borderless override-redirect X11 window
// ("tab") pinned to the right edge of the screen. Because each tab is
// its own window with finite bounds, there's no transparent dead-space to
// swallow clicks — the empty area between tabs is literal desktop, so
// clicks land on whatever's underneath without any input-shape hackery.
//
// Per-window properties (set on map):
//
//	_NET_WM_WINDOW_TYPE = _NET_WM_WINDOW_TYPE_DOCK   (visible across workspaces)
//	_NET_WM_STATE       = _NET_WM_STATE_ABOVE,        (always on top)
//	                      _NET_WM_STATE_STICKY,
//	                      _NET_WM_STATE_SKIP_TASKBAR,
//	                      _NET_WM_STATE_SKIP_PAGER
//	override-redirect   = true                        (no WM decorations/managed-window stealing)
package x11

import (
	"fmt"

	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xinerama"

	"github.com/nitzanz/visor/internal/hud"
)

var _ hud.Backend = (*Backend)(nil)

// Backend implements hud.Backend by spawning an X11 dock process that
// subscribes to the visor daemon and manages one window per session.
type Backend struct{}

func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return "x11" }

func (b *Backend) Install() (string, error) {
	// No on-disk install needed — everything is in the binary.
	return "x11 backend is built into visor; nothing to install. Run `visor hud open --backend=x11`.\n", nil
}

func (b *Backend) Open() error {
	d, err := newDock()
	if err != nil {
		return fmt.Errorf("connect X: %w", err)
	}
	defer d.close()
	return d.run()
}

func (b *Backend) Close() error {
	// The dock runs in-process; closing means SIGTERM the running `visor hud
	// open --backend=x11` process. Users can do that directly.
	return fmt.Errorf("x11 backend runs in-foreground; send SIGTERM (Ctrl-C / kill) to stop it")
}

// monitor reports the geometry of the primary monitor, falling back to the
// full root geometry if Xinerama isn't available.
type monitor struct {
	x, y, w, h int
}

func primaryMonitor(X *xgbutil.XUtil) (monitor, error) {
	heads, err := xinerama.PhysicalHeads(X)
	if err != nil || len(heads) == 0 {
		s := X.Screen()
		return monitor{0, 0, int(s.WidthInPixels), int(s.HeightInPixels)}, nil
	}
	// Pick the head that contains the most "primary"-ish indicator. Xinerama
	// doesn't expose RandR primary; we fall back to the first head, which is
	// usually the primary on most setups.
	h := heads[0]
	return monitor{h.X(), h.Y(), h.Width(), h.Height()}, nil
}
