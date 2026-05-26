package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/nitzanz/visor/internal/hookpayload"
	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
	"github.com/nitzanz/visor/internal/wm"
)

// runHook is invoked from the shell wrapper registered in ~/.claude/settings.json.
//
// Usage: visor hook <event> [--matcher <s>]
//
// stdin: Claude's hook payload JSON.
// env:   CLAUDE_PID (set by the wrapper to the bash script's PPID, which is claude).
//
// The hook command must be fast and never fail loudly — Claude will block on
// the hook's exit code. We log to stderr and always exit 0.
func runHook(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "hook: need event name")
		os.Exit(0)
	}
	event := args[0]
	var matcher string
	for i := 1; i < len(args); i++ {
		if args[i] == "--matcher" && i+1 < len(args) {
			matcher = args[i+1]
			i++
		}
	}

	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	var fc hookpayload.FromClaude
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &fc)
	}

	enriched := hookpayload.Enriched{FromClaude: fc, Matcher: matcher}
	if p := os.Getenv("CLAUDE_PID"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			enriched.PID = n
		}
	}
	// Only spend time detecting WM on SessionStart — other hooks don't need it.
	if event == "SessionStart" {
		i := wm.Detect()
		enriched.WM = i.WM
		enriched.WindowID = i.WindowID
		enriched.TmuxPane = i.TmuxPane
	} else if t := os.Getenv("TMUX_PANE"); t != "" {
		enriched.TmuxPane = t
	}

	body, _ := json.Marshal(enriched)
	_, err := ipc.Call(paths.Socket(), ipc.Request{
		Cmd:  "hook",
		Hook: event,
		Body: body,
	})
	if err != nil {
		// "Daemon not running" is the expected state when the user hasn't
		// started visord — stay silent so Claude doesn't log a warning per hook.
		// Anything else (corrupt socket, protocol mismatch) is worth surfacing.
		if !isDaemonDown(err) {
			fmt.Fprintf(os.Stderr, "visor hook %s: %v\n", event, err)
		}
	}
}

func isDaemonDown(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNRESET)
}
