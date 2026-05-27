package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
	"github.com/nitzanz/visor/internal/state"
	"github.com/nitzanz/visor/internal/transcript"
)

func runCtl(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ctl: subcommand required (list|jump|dismiss|ack|json|classify)")
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		ctlList(false)
	case "json":
		ctlList(true)
	case "dismiss":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "dismiss: need session id")
			os.Exit(2)
		}
		ctlSimple("dismiss", args[1])
	case "ack":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "ack: need session id")
			os.Exit(2)
		}
		ctlSimple("ack", args[1])
	case "jump":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "jump: need session id")
			os.Exit(2)
		}
		ctlSimple("jump", args[1])
	case "watch":
		// Long-lived subscription: prints one JSON line whenever HUD-observable
		// state changes. Consumed by eww's deflisten.
		ctlWatch()
	case "classify":
		// Local debug helper (no daemon required).
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "classify: need transcript path")
			os.Exit(2)
		}
		lines, err := transcript.ParseFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s\t%d lines\t%s\n", transcript.Classify(lines), len(lines), args[1])
	default:
		fmt.Fprintf(os.Stderr, "ctl: unknown subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func ctlList(asJSON bool) {
	resp, err := ipc.Call(paths.Socket(), ipc.Request{Cmd: "list"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctl:", err)
		os.Exit(1)
	}
	if asJSON {
		os.Stdout.Write(resp.Data)
		if len(resp.Data) > 0 && resp.Data[len(resp.Data)-1] != '\n' {
			os.Stdout.Write([]byte{'\n'})
		}
		return
	}
	var snaps []state.Snapshot
	if err := json.Unmarshal(resp.Data, &snaps); err != nil {
		fmt.Fprintln(os.Stderr, "ctl: decode:", err)
		os.Exit(1)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ACTIVITY\tATTN\tAGE\tCWD\tID")
	now := time.Now()
	for _, s := range snaps {
		age := now.Sub(s.LastUpdate).Truncate(time.Second)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Activity, s.Attention, age, shortPath(s.CWD), shortID(s.ID))
	}
	tw.Flush()
}

func ctlSimple(cmd, id string) {
	_, err := ipc.Call(paths.Socket(), ipc.Request{Cmd: cmd, ID: id})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctl:", err)
		os.Exit(1)
	}
}

func ctlWatch() {
	// Long-lived stream for eww's deflisten. The daemon can die and restart
	// under us, so we reconnect with backoff rather than exiting — that keeps
	// the deflisten widget alive across daemon restarts. On disconnect we emit
	// an empty array so the HUD clears instead of showing stale sessions.
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 2 * time.Second
	)
	backoff := minBackoff
	for {
		connected := watchOnce(out)
		// Connection is down (or never came up): clear the HUD and back off.
		fmt.Fprintln(out, "[]")
		out.Flush()
		if connected {
			backoff = minBackoff // daemon was alive; recover fast next time
		}
		time.Sleep(backoff)
		if !connected && backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// watchOnce holds one connection's lifetime, copying snapshot lines to out.
// Returns true if it reached a live subscription (got the ack line) before the
// connection ended — used by the caller to decide whether to reset backoff.
func watchOnce(out *bufio.Writer) bool {
	c, err := net.Dial("unix", paths.Socket())
	if err != nil {
		return false
	}
	defer c.Close()
	req := ipc.Request{Cmd: "watch"}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := c.Write(b); err != nil {
		return false
	}
	br := bufio.NewReader(c)
	// First line is the Response acknowledgement.
	if _, err := br.ReadBytes('\n'); err != nil {
		return false
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out.Write(line)
			out.Flush()
		}
		if err != nil {
			return true
		}
	}
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && len(p) > len(home) && p[:len(home)] == home {
		return "~" + p[len(home):]
	}
	return p
}
