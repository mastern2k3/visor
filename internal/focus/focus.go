// Package focus warps the user's input focus to a target Claude session.
//
// A jump is two steps for a typical setup (terminal + tmux):
//
//  1. Focus the window hosting the terminal. The protocol depends on the
//     WM captured at SessionStart / UserPromptSubmit (see internal/wm):
//     X11/EWMH for x11+leftwm, niri's IPC for niri, etc.
//
//  2. If the session lives in a tmux pane, point tmux's relevant
//     client(s) at that pane via select-window / select-pane.
//
// Either step is a no-op when its prerequisite metadata isn't present
// (e.g. a non-tmux session has empty tmux_pane).
//
// Adding a new WM:
//
//  1. In internal/wm/adapters.go, add a fooFocusedID() that returns the
//     WM-specific id of the focused window, and a Detect() case keyed on
//     the WM's env var (e.g. $SWAYSOCK).
//  2. In this package, add a focusFoo(id) that drives that WM's IPC.
//  3. Add a case to the switch in Dispatch below. Unknown WM values
//     return an error rather than silently falling back to X11 — sending
//     a non-X11 id to EWMH would target the wrong window.
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

	// JumpCmd, when non-empty, replaces all built-in focus paths. Run via
	// `sh -c` with session metadata exported as VISOR_* env vars.
	JumpCmd   string
	SessionID string // forwarded to a custom jump command as $VISOR_SESSION_ID
	CWD       string // forwarded to a custom jump command as $VISOR_CWD
}

// Dispatch warps focus to the target. Returns the first error encountered;
// subsequent best-effort steps still run so a failed terminal-focus doesn't
// stop us from also switching tmux to the right pane.
func Dispatch(t Target) error {
	var firstErr error
	tried := 0

	// Custom jump command (set by a launcher via $VISOR_JUMP_CMD at
	// SessionStart) fully replaces the WM and tmux paths. The launcher
	// is presumed authoritative about how to bring its session back.
	if t.JumpCmd != "" {
		return focusCustom(t)
	}

	if t.WindowID != "" {
		tried++
		var err error
		switch t.WM {
		case "niri":
			err = focusNiri(t.WindowID)
		case "x11", "leftwm", "":
			// Empty WM is the legacy case — pre-WM-detection sessions
			// stored a window id without recording the protocol. Best
			// effort: assume X11 (the only path that existed then).
			err = focusX11(t.WindowID)
		default:
			err = fmt.Errorf("no focus adapter for WM %q (window_id=%s); add one in internal/focus", t.WM, t.WindowID)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s focus: %w", t.WM, err)
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
