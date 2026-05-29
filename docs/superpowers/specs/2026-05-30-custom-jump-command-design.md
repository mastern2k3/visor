# Custom jump command (`VISOR_JUMP_CMD`)

**Date:** 2026-05-30
**Status:** Approved, ready for implementation plan

## Problem

`internal/focus` currently knows how to bring a session's terminal forward on
X11 (EWMH) and niri (IPC), and how to point tmux at the right pane. Some real
setups don't fit either model — e.g. a "scratchpad" window that's toggled
in/out of view by a user-written script. Adding per-WM adapters for every
niche launcher is the wrong shape: the launcher already knows how to undo
itself, and we don't want that knowledge in `internal/focus`.

We need a generic escape hatch so any launcher can declare "to jump back to
the session I spawned, run *this*."

## Solution

The launcher exports `VISOR_JUMP_CMD` in the environment before exec'ing
`claude`. Visor captures it through the existing SessionStart hook, persists
it on the session, and `focus.Dispatch` runs it instead of the built-in WM /
tmux paths when present.

### Capture

`cmd/visor/hook.go` enriches SessionStart payloads with PID, WM, and tmux
metadata pulled from the hook process's own environment. Add a read of
`os.Getenv("VISOR_JUMP_CMD")` to that same enrichment step. The value (which
may be empty) rides along in the hook payload and is stored on
`state.Session`.

Capture is SessionStart-only. We do not re-read it on later hooks: the
launcher's intent is fixed at spawn time. We also do not expose a CLI to
mutate it on a running session — YAGNI for the scratchpad use case.

### Persistence

`state.Session` gains a `JumpCmd string` field. `internal/state/persist.go`
serialises it as `jump_cmd`. Snapshots used by the HUD do not include it
(the HUD never needs to know).

### Dispatch

`focus.Target` gains `JumpCmd string`. `focus.Dispatch` short-circuits:

```
if t.JumpCmd != "" {
    return focusCustom(t)
}
// existing WM + tmux logic unchanged
```

`focusCustom` lives in `internal/focus/custom.go`. It runs the command via
`sh -c` with a 3-second context timeout, captures stderr, discards stdout,
and wraps any non-zero exit or timeout into a returned error consistent with
the other focus errors. The custom command fully *replaces* the WM and tmux
steps — when a launcher declares its own jump, we trust it.

### Environment passed to the command

The child sees the daemon's environment plus:

| Variable           | Source                          |
|--------------------|---------------------------------|
| `VISOR_SESSION_ID` | `session.ID`                    |
| `VISOR_PID`        | `session.PID` (decimal)         |
| `VISOR_CWD`        | `session.CWD`                   |
| `VISOR_WM`         | `session.WM`                    |
| `VISOR_WINDOW_ID`  | `session.WindowID` (may be `""`)|
| `VISOR_TMUX_PANE`  | `session.TmuxPane` (may be `""`)|

This lets one-liners work either with the launcher's own state or with what
visor captured:

```sh
# Scratchpad launcher knows its own toggle command
export VISOR_JUMP_CMD='my-scratchpad show'

# Or use the captured locator
export VISOR_JUMP_CMD='niri msg action focus-window --id $VISOR_WINDOW_ID'
```

### Wiring

`cmd/visor/daemon.go`'s `jump` IPC handler builds `focus.Target` from the
session — add the new field there.

## Non-goals

- **No global config file or rules engine.** A `VISOR_JUMP_CMD` set in the
  user's shell profile already acts as a global default — every spawned
  session inherits it.
- **No augment mode.** If `JumpCmd` is set, WM + tmux focus do not run. The
  launcher is presumed authoritative.
- **No CLI override.** Env-var only.

## Files touched

- `cmd/visor/hook.go` — capture `VISOR_JUMP_CMD`.
- `cmd/visor/daemon.go` — pass `JumpCmd` into `focus.Target`.
- `internal/state/state.go` — `JumpCmd` field on `Session`.
- `internal/state/persist.go` — serialise `jump_cmd`.
- `internal/state/hooks.go` — accept `jump_cmd` from hook payload at
  SessionStart.
- `internal/focus/focus.go` — `JumpCmd` field on `Target`, short-circuit in
  `Dispatch`.
- `internal/focus/custom.go` (new) — `focusCustom` implementation.

## Backward compatibility

`JumpCmd == ""` preserves all existing behavior. Persisted sessions without
the field load as empty string. No migration needed.
