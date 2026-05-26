package focus

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
)

// focusX11 sends an EWMH _NET_ACTIVE_WINDOW ClientMessage to the root
// window, asking the WM to focus the target window. LeftWM and other
// EWMH-compliant X11 WMs honor source-indication=2 ("user request") by
// switching workspaces/tags and raising the window as needed.
//
// We open a short-lived X connection per call. The daemon doesn't keep a
// persistent X conn (it would complicate the otherwise-headless service),
// and EWMH messages are tiny — opening costs are negligible.
func focusX11(idStr string) error {
	id, err := parseWindowID(idStr)
	if err != nil {
		return fmt.Errorf("parse window id %q: %w", idStr, err)
	}
	X, err := xgbutil.NewConn()
	if err != nil {
		return fmt.Errorf("X connect: %w", err)
	}
	defer X.Conn().Close()

	if err := ewmh.ActiveWindowReq(X, id); err != nil {
		return err
	}
	X.Sync()
	return nil
}

// parseWindowID accepts a decimal id (xdotool output) or a 0x-prefixed hex id.
func parseWindowID(s string) (xproto.Window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty window id")
	}
	base := 10
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
		base = 16
	}
	n, err := strconv.ParseUint(s, base, 32)
	if err != nil {
		return 0, err
	}
	return xproto.Window(uint32(n)), nil
}
