package focus

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// focusNiri asks niri to focus the window with the given numeric id via
// `niri msg action focus-window --id <N>`. The id is the integer captured
// at SessionStart from `niri msg --json focused-window` (see internal/wm).
//
// We shell out to the CLI rather than dialing $NIRI_SOCKET directly:
// the CLI is the documented stable interface, the call is one-shot, and
// the daemon already pays a similar shell-out cost on the tmux step.
func focusNiri(idStr string) error {
	id, err := parseNiriID(idStr)
	if err != nil {
		return fmt.Errorf("parse niri window id %q: %w", idStr, err)
	}
	out, err := exec.Command("niri", "msg", "action", "focus-window", "--id", strconv.FormatUint(id, 10)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("niri focus-window %d: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func parseNiriID(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty window id")
	}
	return strconv.ParseUint(s, 10, 64)
}
