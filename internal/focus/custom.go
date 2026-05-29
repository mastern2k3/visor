package focus

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// customJumpTimeout bounds how long a launcher-supplied jump command may run
// before the daemon gives up. The user is waiting on focus to switch, so
// this needs to be short; matches the "best effort, don't hang the daemon"
// shape of the other focus paths.
const customJumpTimeout = 3 * time.Second

// focusCustom runs t.JumpCmd via `sh -c`, with session metadata exported as
// VISOR_* env vars so the command can interpolate them. Stdout is discarded;
// stderr is captured into any returned error.
func focusCustom(t Target) error {
	ctx, cancel := context.WithTimeout(context.Background(), customJumpTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", t.JumpCmd)
	cmd.Env = append(os.Environ(),
		"VISOR_SESSION_ID="+t.SessionID,
		"VISOR_WM="+t.WM,
		"VISOR_WINDOW_ID="+t.WindowID,
		"VISOR_TMUX_PANE="+t.TmuxPane,
		"VISOR_PID="+strconv.Itoa(t.PID),
		"VISOR_CWD="+t.CWD,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("custom jump: timed out after %s: %s", customJumpTimeout, msg)
		}
		if msg != "" {
			return fmt.Errorf("custom jump: %w: %s", err, msg)
		}
		return fmt.Errorf("custom jump: %w", err)
	}
	return nil
}
