// Package focus warps the user's input focus to a target Claude session.
//
// A jump is two steps for a typical setup (terminal + tmux):
//
//  1. Focus the X11 window hosting the terminal (EWMH _NET_ACTIVE_WINDOW
//     ClientMessage to root). LeftWM, sway-in-X11-mode, and most other
//     X11 tiling WMs honor this and switch workspaces/tags as needed.
//
//  2. If the session lives in a tmux pane, point tmux's relevant
//     client(s) at that pane via select-window / select-pane.
//
// Either step is a no-op when its prerequisite metadata isn't present
// (e.g. a non-tmux session has empty tmux_pane).
package focus

import (
	"errors"
	"fmt"
)

// Target carries the metadata captured by the SessionStart hook.
type Target struct {
	WM       string // "x11", "leftwm", "sway", "hypr", "tmux", "unknown"
	WindowID string // hex or decimal X11 window id (set when WM ∈ X11 family)
	TmuxPane string // %N (when session ran inside tmux)
	PID      int    // claude process pid (currently unused, kept for adapters)
}

// Dispatch warps focus to the target. Returns the first error encountered;
// subsequent best-effort steps still run so a failed terminal-focus doesn't
// stop us from also switching tmux to the right pane.
func Dispatch(t Target) error {
	var firstErr error
	tried := 0

	if t.WindowID != "" {
		tried++
		if err := focusX11(t.WindowID); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("x11 focus: %w", err)
		}
	}
	if t.TmuxPane != "" {
		tried++
		if err := focusTmux(t.TmuxPane); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("tmux focus: %w", err)
		}
	}
	if tried == 0 {
		return errors.New("no focus locator (need window_id or tmux_pane on the session)")
	}
	return firstErr
}
