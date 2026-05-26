package focus

import (
	"fmt"
	"os/exec"
)

// focusTmux switches tmux's view to the target pane.
//
// We use `select-window` + `select-pane` rather than `switch-client`:
//
//   * select-window operates per-session — every tmux client currently
//     viewing the session containing the target pane will move to that
//     window. Clients in other sessions are untouched.
//   * switch-client requires a `-c <client>` argument (the daemon has no
//     "current client" because it runs outside tmux) and figuring out
//     *which* client to switch is non-trivial; that work is deferred.
//
// In a normal setup (one client per terminal, terminal already focused
// by focusX11) this gives the right visual outcome: the user lands on
// the pane running their claude session.
func focusTmux(pane string) error {
	if err := exec.Command("tmux", "select-window", "-t", pane).Run(); err != nil {
		return fmt.Errorf("select-window %s: %w", pane, err)
	}
	if err := exec.Command("tmux", "select-pane", "-t", pane).Run(); err != nil {
		return fmt.Errorf("select-pane %s: %w", pane, err)
	}
	return nil
}
