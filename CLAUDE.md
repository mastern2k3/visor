# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Visor is a cross-WM "attention HUD" for Claude Code sessions running on the same Linux machine. A long-lived daemon watches every live session and emits its state ("working", "waiting on user", "blocked on permission prompt") to a small dock pinned to the right edge of the screen — one drawer per session, color-coded, click to dismiss/jump. Single static Go binary.

The repo contains an experimental product in active iteration with one user — there is no test suite yet, no release tag, no stability promise. Read the conversation context (or the user) before making sweeping changes.

## Build & run

```
go build -o bin/visor ./cmd/visor

# install hook wrapper + print settings.json snippet for ~/.claude/settings.json
./bin/visor install

# run the daemon (foreground)
./bin/visor daemon [-v]

# query daemon state
./bin/visor ctl list                # table
./bin/visor ctl json                # JSON
./bin/visor ctl watch               # long-lived stream (used by the HUD)
./bin/visor ctl dismiss <id>
./bin/visor ctl ack <id>
./bin/visor ctl jump <id>           # warps focus via internal/focus (X11 + tmux)

# HUD — picks a backend with --backend=eww (default), --backend=x11, or --backend=wlr
./bin/visor hud install
./bin/visor hud open
./bin/visor hud close

# debug helper: classify a transcript without the daemon
./bin/visor ctl classify <path>.jsonl
```

The daemon listens on `$VISOR_SOCK` or `$XDG_RUNTIME_DIR/visor.sock`. Reads transcripts from `$CLAUDE_CONFIG_DIR/projects` or `~/.claude/projects`. State recovery file lives under `$XDG_STATE_HOME/visor` or `~/.local/state/visor`.

## Architecture in one breath

**Inputs.** Two streams of evidence feed the daemon:
1. Global hooks registered in `~/.claude/settings.json` (`SessionStart`, `Stop`, `UserPromptSubmit`, `Notification` with separate `matcher` entries for `permission_prompt` and `idle_prompt`, `SessionEnd`). The shell wrapper `scripts/visor-hook.sh` sets `CLAUDE_PID=$PPID` and exec's `visor hook <event> [--matcher ...]`. `cmd/visor/hook.go` enriches the payload with PID, WM info (via `internal/wm`), and tmux pane, then POSTs JSON to the daemon socket.
2. A file tailer (`internal/discovery`) using fsnotify on the projects root plus a 400 ms per-file mtime/size poll. New appends to `<project>/<sessionId>.jsonl` are decoded with `internal/transcript` and folded into the session store.

**Source of truth.** JSONL is canonical; hooks are low-latency metadata + the only way to learn about permission prompts (which never appear in the transcript). The Notification hook is the *sole* signal for `waiting=permission`.

**Classification.** `internal/transcript/classify.go`:
- last non-sidechain conversation line is `assistant` with `stop_reason="tool_use"` → **working**
- last is `assistant` with `stop_reason="end_turn"` → **waiting**
- last is `user` whose content has `type=tool_result` → **working** (model is processing)
- last is `user` otherwise → **waiting**
- Sidechain lines (`isSidechain: true`) and metadata types are skipped. Use `bufio.Scanner` with the **10 MB buffer** — tool results blow past the 64 KB default and silently truncate parses.

**State model.** `internal/state` has two orthogonal axes:
- *Activity* (objective, from JSONL/hooks): `working` / `waiting` / `unknown`.
- *Attention* (subjective): `ack` / `needs` / `dismissed`. Dismiss silences a session until the next transition; a working→waiting cycle re-arms `needs`. `Waiting` enum distinguishes `user` vs `permission`. Any subsequent live event for a dismissed session (hook or transcript append, but not startup backfill) clears the dismissal — silence is per-quiescent-period, not permanent.

`Snapshot` is what leaves the store — sorted by attention priority (needs > ack > dismissed), then by `FirstSeen`, so HUD tabs do not reshuffle on every update. `display_cwd` is computed server-side (`$HOME → ~`) so backends don't need home-dir logic.

**Pub/sub.** `internal/state/notify.go` broadcasts a fresh `Snapshot` after every mutation. A SHA-256 digest of *only HUD-observable fields* (id, activity, waiting, attention, display_cwd) suppresses no-op broadcasts — this is why poll-driven backends (`defpoll` in eww) used to flicker hover/tooltip state every second and push-driven backends (`visor ctl watch`) do not. **Never widen the digest with high-frequency fields** like `LastUpdate` or you will reintroduce the flicker.

**IPC.** `internal/ipc` — one JSON request per line over a Unix socket, one response back. A `Response.Stream` channel switches the server into long-lived line-streaming mode (used by `watch`). `ipc.Call` is one-shot; the `watch` ctl handler dials directly to keep the connection open.

**HUD backends.** `internal/hud/hud.go` defines a minimal `Backend` interface (`Name / Install / Open / Close`). Today: `eww` (yuck config that consumes `visor ctl watch` via `deflisten`), `x11` (pure-Go native dock via `jezek/xgb` + `xgbutil` — one override-redirect window per session, EWMH dock/sticky/above hints, no transparent regions so clicks between tabs hit the desktop), and `wlr` (pure-Go Wayland-native dock via `codeberg.org/tesselslate/wl` + locally-generated `wlr-layer-shell-unstable-v1` bindings in `internal/hud/wlr/protocol/` — one `zwlr_layer_surface_v1` per session, anchored right, ARGB8888 `wl_shm` buffers double-buffered with release tracking; collapsed tabs render the panel region transparent so only the leftmost strip is visible). Both native backends share pixel drawing via `internal/hud/render` (`DrawTab`, `ColorFor`, font discovery — see the candidate list at `internal/hud/render/font.go`). Compositor coverage for `wlr`: Niri, sway, hyprland, river, wayfire, labwc, KDE. GNOME has no layer-shell — use `--backend=x11` (via XWayland) there. `cmd/visor/hud.go::pickBackend` is the registry — add a new package under `internal/hud/<name>/`, implement Backend, and add a case. Do not widen the interface until a new backend needs a method neither existing one has.

**Tab clicks (x11 backend).** Left = `jump` (focus dispatch), middle = `dismiss`, right = `ack`. See `internal/hud/x11/tab.go::onButton`.

**Focus dispatch.** `internal/focus` warps the user back to a session. Two best-effort steps run in order: (1) EWMH `_NET_ACTIVE_WINDOW` ClientMessage to the captured X11 `WindowID`, (2) tmux `select-window` / `select-pane` against the captured `TmuxPane`. Either step is skipped if its locator wasn't captured at SessionStart. The daemon's `jump` IPC handler in `cmd/visor/daemon.go` builds the `focus.Target` from the session and calls `focus.Dispatch`. Wayland-native focus (Niri, sway, hyprland) is not yet wired — currently relies on the X11 path working under XWayland or the WM honoring EWMH.

## Things that will bite you

- **Hooks must stay silent on failure.** Anything written to stderr is logged by Claude as a warning. `visor hook` already treats "daemon not running" (ECONNREFUSED / ENOENT / ECONNRESET) as expected and silent; preserve that behavior or you will spam the user's session log.
- **Map iteration is randomized.** `Snapshot()` sorts the slice before returning. If you add a new entry path that bypasses it, the HUD will reshuffle every refresh.
- **Eww has no input-shape / passthrough option in 0.6.0** (verified by reading `crates/yuck/src/config/backend_window_options.rs` — only `sticky`, `struts`, `windowtype`, `wm-ignore`). GTK does not auto-apply XShape from alpha here. This is why the x11 backend exists.
- **`xevent.Quit` only sets a flag.** If the X loop is blocked inside `Read`, the flag is never checked. `dock.quit()` sends a synthetic ClientMessage to root to wake it. If you add another shutdown path, mirror that.
- **PID capture happens in the hook wrapper.** `$PPID` in `scripts/visor-hook.sh` is the claude process. If you ever invoke `visor hook` differently, set `CLAUDE_PID` explicitly or this metadata is lost.
- **Backfill discovery filters by `mtime > 24h ago`** to avoid storming the daemon with historical transcripts. Active sessions older than 24h get picked up only when something appends to their JSONL or a hook fires.
- **wlr buffer ownership.** The compositor owns each `wl_buffer` from `attach`+`commit` until it sends `wl_buffer.release`. Reusing a buffer earlier corrupts pixels silently. `internal/hud/wlr/buffer.go` tracks a `released bool` per buffer; never bypass it. The Wayland dispatch goroutine is the single mutator — calling `Acquire()` from another goroutine would need synchronization.
- **wlr dispatch loop is a 20Hz poller, not edge-triggered.** `tesselslate/wl` doesn't expose the display fd, so `dock.run()` uses a rate-limited `wl_display.sync` wakeup to bound `Dispatch()` latency. Snapshot updates interrupt the sleep, so user-visible latency stays low. Proper fix would be vendor-patching `tesselslate/wl` to expose `Display.Fd()` and switching to `unix.Poll`.

## Conventions

- One canonical source per asset: eww files live in `internal/hud/eww/`; `visor hud install` copies them out via `embed.FS`. The hook wrapper has a working copy in `scripts/visor-hook.sh` *and* an embedded copy in `cmd/visor/install_hook.sh` — keep them in sync (the build script `cp`s; CI doesn't exist yet).
- The `_refs/` directory holds upstream projects we cribbed parsing patterns from (`ccdiag` for JSONL types, `claude-pool` for hook scripts). It's gitignored; do not import from it.
- Logging uses `log/slog`. Hook command exits 0 on every path (Claude blocks on the hook's exit code).

## Pending / known WIP

- Focus dispatch covers X11 (EWMH) + tmux. Niri / sway / hyprland native focus protocols are not yet wired — under those compositors, jump relies on XWayland or the WM honoring EWMH. Add per-WM adapters in `internal/focus/` when needed; the `WM` field on `focus.Target` is the dispatch key.
- No Wayland backend yet. Design draft lives at `docs/superpowers/specs/2026-05-27-wayland-backend-design.md` (target package `internal/hud/wlr`, pure-Go via `codeberg.org/tesselslate/wl`, no cgo). The `Backend` interface is the seam.
