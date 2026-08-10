package main

import (
	"fmt"
	"os"
)

const usage = `visor — cross-WM attention HUD for Claude Code sessions

Usage:
  visor daemon              run the attention daemon
  visor install             write hook wrapper, print settings.json snippet
  visor hud <subcommand>    HUD control
                              install [--backend=x11|wlr] [--theme=<name>] [--shadow=<bool>]
                              open    [--backend=x11|wlr] [--theme=<name>] [--shadow=<bool>]
                              close   [--backend=x11|wlr] [--theme=<name>] [--shadow=<bool>]
                              theme <tuned|silent|traffic>  persist a palette theme
                              shadow <on|off>                persist the drop-shadow setting
                              (backend defaults to auto-detect; --theme/--shadow each
                              pin the whole config — theme AND shadow — disabling the
                              live config-file reload for the life of the process)
  visor hook <event>        post a hook event to the daemon (stdin: JSON)
  visor ctl <subcommand>    query/control the daemon
                              list                  print sessions
                              jump <session-id>     focus the session's window
                              dismiss <session-id>  silence until next state change
                              ack <session-id>      acknowledge (clear needs-attention)
                              json                  dump full state as JSON
                              watch                 long-lived stream of snapshots
                              classify <path.jsonl> classify a transcript, no daemon needed

Env:
  VISOR_SOCK   socket path (default $XDG_RUNTIME_DIR/visor.sock)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon":
		runDaemon(os.Args[2:])
	case "install":
		runInstall(os.Args[2:])
	case "hud":
		runHUD(os.Args[2:])
	case "hook":
		runHook(os.Args[2:])
	case "ctl":
		runCtl(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
