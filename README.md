# Visor

A cross-WM **attention HUD** for [Claude Code](https://claude.com/claude-code) sessions running on the same Linux machine.

A long-lived daemon watches every live Claude Code session and emits its state — `working`, `waiting on user`, or `blocked on a permission prompt` — to a small dock pinned to the right edge of your screen. One drawer ("tongue") per session, color-coded by state, click to dismiss or jump. Single static Go binary.

> ⚠️ Experimental, single-user project in active iteration. No test suite, no release tag, no stability promise yet.

## Why

When you run several Claude Code sessions at once, it's easy to lose track of which one is grinding away and which one is sitting idle waiting for your input or stuck on a permission prompt. Visor surfaces that at a glance, regardless of which window manager you use.

## Build

```sh
go build -o bin/visor ./cmd/visor
```

## Quick start

```sh
# 1. Install the hook wrapper and print the settings.json snippet
#    to paste into ~/.claude/settings.json
./bin/visor install

# 2. Run the daemon (foreground)
./bin/visor daemon [-v]

# 3. Install and open the HUD
./bin/visor hud install
./bin/visor hud open
```

## Usage

```sh
# Query daemon state
./bin/visor ctl list                # table
./bin/visor ctl json                # JSON
./bin/visor ctl watch               # long-lived stream (used by the HUD)

# Act on a session
./bin/visor ctl dismiss <id>        # silence until next state transition
./bin/visor ctl ack <id>
./bin/visor ctl jump <id>           # focus the session's window (WIP)

# HUD lifecycle
./bin/visor hud open --backend=eww  # default
./bin/visor hud open --backend=x11  # native Go dock
./bin/visor hud close

# Debug: classify a transcript without the daemon
./bin/visor ctl classify <path>.jsonl
```

## How it works

Two streams of evidence feed the daemon:

1. **Global hooks** registered in `~/.claude/settings.json` (`SessionStart`, `Stop`, `UserPromptSubmit`, `Notification`, `SessionEnd`). The hook wrapper enriches each event with PID, window-manager info, and tmux pane, then POSTs JSON to the daemon socket. Hooks are the *only* way to learn about permission prompts, which never appear in the transcript.
2. **A file tailer** that watches `~/.claude/projects/**/*.jsonl` with fsnotify plus a short mtime/size poll, decoding new appends. The JSONL transcript is the canonical source of truth; hooks are low-latency metadata on top.

The daemon classifies each session along two axes:

- **Activity** (objective, from the transcript/hooks): `working` / `waiting` / `unknown`.
- **Attention** (subjective): `ack` / `needs` / `dismissed`. Dismissing silences a session until its next state transition.

A pub/sub layer broadcasts a fresh snapshot after every change, suppressing no-op updates so the HUD doesn't flicker.

### HUD backends

The `Backend` interface (`Name / Install / Open / Close`) has two implementations today:

- **`eww`** — a [yuck](https://github.com/elkowar/eww) config that consumes `visor ctl watch`.
- **`x11`** — a pure-Go native dock (`jezek/xgb` + `xgbutil`): one override-redirect window per session with EWMH dock/sticky/above hints.

## Paths & environment

- Daemon socket: `$VISOR_SOCK` or `$XDG_RUNTIME_DIR/visor.sock`
- Transcripts: `$CLAUDE_CONFIG_DIR/projects` or `~/.claude/projects`
- State recovery: `$XDG_STATE_HOME/visor` or `~/.local/state/visor`

## Project layout

```
cmd/visor/         CLI entrypoints (daemon, hook, ctl, hud, install)
internal/state/    session store, activity/attention model, pub/sub
internal/transcript/  JSONL decode + classification
internal/discovery/   fsnotify file tailer
internal/hud/      backend interface + eww and x11 implementations
internal/ipc/      Unix-socket JSON IPC
internal/wm/       window-manager detection
scripts/           visor-hook.sh wrapper
```

See [CLAUDE.md](CLAUDE.md) for deeper architecture notes and known gotchas.
