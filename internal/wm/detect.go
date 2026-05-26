// Package wm detects the running window manager and captures the locator
// needed to focus a specific window. Detection runs in the hook context
// (i.e. inside the claude process's environment) so env vars are reliable.
package wm

import (
	"os"
	"os/exec"
)

type Info struct {
	WM       string // niri | sway | hypr | x11 | unknown
	WindowID string // WM-specific identifier (may be empty if can't be determined)
	TmuxPane string // %N pane id if running inside tmux
}

// Detect identifies the WM and (when cheap) the currently-focused window id.
// Called from `visor hook` at SessionStart — the focused window at that moment
// is the terminal hosting claude.
func Detect() Info {
	info := Info{TmuxPane: os.Getenv("TMUX_PANE")}

	switch {
	case os.Getenv("NIRI_SOCKET") != "":
		info.WM = "niri"
		info.WindowID = niriFocusedID()
	case os.Getenv("SWAYSOCK") != "":
		info.WM = "sway"
		info.WindowID = swayFocusedID()
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		info.WM = "hypr"
		info.WindowID = hyprFocusedID()
	case os.Getenv("DISPLAY") != "":
		info.WM = "x11"
		info.WindowID = x11FocusedID()
	default:
		info.WM = "unknown"
	}
	return info
}

func runOut(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(b)
}
