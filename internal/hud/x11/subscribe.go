package x11

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
)

// sessionView is the minimal session shape the dock needs. Keeping it
// local (instead of importing state.Snapshot) decouples the dock from
// daemon internals — schema changes to the IPC payload land here.
type sessionView struct {
	ID         string `json:"id"`
	Activity   string `json:"activity"`
	Attention  string `json:"attention"`
	Waiting    string `json:"waiting"`
	DisplayCWD string `json:"display_cwd"`
	Title      string `json:"title"`
}

// subscribeLoop keeps a subscription alive across daemon restarts. When the
// connection drops it pushes an empty snapshot (so stale tongues clear) and
// reconnects with capped exponential backoff. Returns when ctx is cancelled,
// preventing goroutine and FD leaks on shutdown.
func subscribeLoop(ctx context.Context, out chan<- []sessionView, log *slog.Logger) {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 2 * time.Second
	)
	backoff := minBackoff
	for {
		connected, err := subscribe(ctx, out)
		// Connection ended (daemon died, restarted, or never came up). Clear
		// the dock so it doesn't show sessions from the dead daemon.
		select {
		case out <- nil:
		case <-ctx.Done():
			return
		}
		if connected {
			// We reached a live subscription; the daemon was up. Recover
			// quickly when it comes back rather than carrying stale backoff.
			backoff = minBackoff
		} else {
			log.Debug("daemon unreachable; retrying", "err", err, "backoff", backoff)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if !connected && backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// subscribe opens a long-lived connection to the visor daemon and sends
// every snapshot it receives on `out`. The bool reports whether a live
// subscription was reached (ack line received) before the connection ended —
// the caller uses it to distinguish "daemon down" from "daemon restarted".
// Returns (true, nil) on clean shutdown via ctx cancellation.
func subscribe(ctx context.Context, out chan<- []sessionView) (connected bool, err error) {
	c, err := net.Dial("unix", paths.Socket())
	if err != nil {
		return false, fmt.Errorf("dial visor socket: %w (is the daemon running?)", err)
	}
	defer c.Close()

	req := ipc.Request{Cmd: "watch"}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := c.Write(b); err != nil {
		return false, err
	}

	br := bufio.NewReader(c)
	// First line: ack response (we ignore the contents)
	if _, err := br.ReadBytes('\n'); err != nil {
		return false, err
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var snap []sessionView
			if jerr := json.Unmarshal(line, &snap); jerr == nil {
				select {
				case out <- snap:
				case <-ctx.Done():
					return true, nil
				}
			}
		}
		if err != nil {
			return true, err
		}
	}
}
