# `VISOR_JUMP_CMD` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let any launcher (scratchpad scripts, custom WM setups) declare its own jump-back command via `VISOR_JUMP_CMD`, captured at SessionStart and run instead of the built-in WM/tmux focus paths.

**Architecture:** A new `JumpCmd` string flows: env var → hook process → enriched payload → session struct → persisted state → `focus.Target`. When non-empty, `focus.Dispatch` short-circuits to a new `focusCustom` that runs the command via `sh -c` with a 3s timeout and a child env populated with session metadata (`VISOR_SESSION_ID`, `VISOR_WM`, `VISOR_WINDOW_ID`, `VISOR_TMUX_PANE`, `VISOR_PID`, `VISOR_CWD`).

**Tech Stack:** Go standard library only (`os/exec`, `context`).

**Spec:** `docs/superpowers/specs/2026-05-30-custom-jump-command-design.md`

**Testing note:** This repo has no test suite (see CLAUDE.md). Tasks use `go build` + `go vet` for verification, plus an end-of-plan manual smoke test. Don't add a one-off test framework — keep parity with the rest of the codebase.

---

## File map

- Modify `internal/hookpayload/hookpayload.go` — add `JumpCmd` to `Enriched`.
- Modify `cmd/visor/hook.go` — read `VISOR_JUMP_CMD` from env on `SessionStart` only.
- Modify `internal/state/state.go` — add `JumpCmd` field to `Session`; hydrate from persisted; carry through `notify`.
- Modify `internal/state/persist.go` — add `jump_cmd` to `persistedSession`; save + load.
- Modify `internal/state/hooks.go` — copy `p.JumpCmd` onto the session on `SessionStart`.
- Modify `internal/focus/focus.go` — add `JumpCmd` field to `Target`; short-circuit `Dispatch`.
- Create `internal/focus/custom.go` — `focusCustom(t Target) error`.
- Modify `cmd/visor/daemon.go` — pass `sess.JumpCmd` into the `focus.Target` built in the `jump` handler.

---

## Task 1: Add `JumpCmd` to hook payload type

**Files:**
- Modify: `internal/hookpayload/hookpayload.go`

- [ ] **Step 1: Add the field**

In `internal/hookpayload/hookpayload.go`, in the `Enriched` struct, add after `TmuxPane`:

```go
	// JumpCmd is the value of $VISOR_JUMP_CMD captured at SessionStart.
	// When non-empty, focus.Dispatch runs this via `sh -c` instead of the
	// built-in WM and tmux focus paths.
	JumpCmd string `json:"jump_cmd,omitempty"`
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/hookpayload/hookpayload.go
git commit -m "feat(hookpayload): add JumpCmd field for custom jump command"
```

---

## Task 2: Capture `VISOR_JUMP_CMD` in the hook CLI

**Files:**
- Modify: `cmd/visor/hook.go`

- [ ] **Step 1: Read the env var on SessionStart**

In `cmd/visor/hook.go::runHook`, just after the existing `wm.Detect()` block (the `if event == "SessionStart" || event == "UserPromptSubmit"` branch, lines 62–69), add a separate block scoped to `SessionStart` only:

```go
	// VISOR_JUMP_CMD is the launcher's escape hatch: if set, focus.Dispatch
	// runs it instead of the built-in focus paths. Capture only on
	// SessionStart — the launcher's intent is fixed at spawn time and we
	// don't want a later hook (which may run after `exec`-style env
	// changes) to clobber it.
	if event == "SessionStart" {
		enriched.JumpCmd = os.Getenv("VISOR_JUMP_CMD")
	}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add cmd/visor/hook.go
git commit -m "feat(hook): forward VISOR_JUMP_CMD on SessionStart"
```

---

## Task 3: Persist `JumpCmd` on the session

**Files:**
- Modify: `internal/state/state.go`
- Modify: `internal/state/persist.go`
- Modify: `internal/state/hooks.go`

- [ ] **Step 1: Add the field to `Session`**

In `internal/state/state.go`, in the `Session` struct, add after `TmuxPane` (around line 69):

```go
	// JumpCmd is the launcher-declared custom jump command captured at
	// SessionStart from $VISOR_JUMP_CMD. Empty for ordinary sessions.
	JumpCmd string `json:"jump_cmd,omitempty"`
```

- [ ] **Step 2: Add the field to `persistedSession`**

In `internal/state/persist.go`, in the `persistedSession` struct, add after `TmuxPane` (around line 35):

```go
	JumpCmd        string    `json:"jump_cmd,omitempty"`
```

- [ ] **Step 3: Save `JumpCmd` in `snapshotPersist`**

In `internal/state/state.go::snapshotPersist`, add `JumpCmd: sess.JumpCmd,` to the `persistedSession{...}` literal (the block starting at line 198), so the trailing fields read:

```go
			out = append(out, persistedSession{
				ID:             sess.ID,
				TranscriptPath: sess.TranscriptPath,
				CWD:            sess.CWD,
				PID:            sess.PID,
				WM:             sess.WM,
				WindowID:       sess.WindowID,
				TmuxPane:       sess.TmuxPane,
				JumpCmd:        sess.JumpCmd,
				FirstSeen:      sess.FirstSeen,
				Dismissed:      sess.Attention == AttentionDismiss,
				Ended:          sess.Ended,
			})
```

- [ ] **Step 4: Hydrate `JumpCmd` in `NewStore`**

In `internal/state/state.go::NewStore`, add `JumpCmd: p.JumpCmd,` to the `&Session{...}` literal (the block starting at line 161):

```go
		sess := &Session{
			ID:             p.ID,
			TranscriptPath: p.TranscriptPath,
			CWD:            p.CWD,
			PID:            p.PID,
			WM:             p.WM,
			WindowID:       p.WindowID,
			TmuxPane:       p.TmuxPane,
			JumpCmd:        p.JumpCmd,
			FirstSeen:      p.FirstSeen,
			Ended:          p.Ended,
		}
```

- [ ] **Step 5: Copy from hook payload on SessionStart**

In `internal/state/hooks.go::ApplyHook`, the existing "Always-apply metadata" block (lines 60–76) updates `CWD`, `PID`, `WM`, etc. Add a SessionStart-only assignment inside the `case "SessionStart":` arm (currently empty, line 88) — keep it inside that case because we only want to set it once and never overwrite:

```go
	case "SessionStart":
		// JumpCmd is captured by the hook CLI from $VISOR_JUMP_CMD and is
		// only meaningful at session creation. Set unconditionally here
		// (including to "") so a SessionStart replay reflects current intent.
		sess.JumpCmd = p.JumpCmd
```

- [ ] **Step 6: Verify it compiles**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/state/persist.go internal/state/hooks.go
git commit -m "feat(state): persist JumpCmd per session"
```

---

## Task 4: Implement `focusCustom` and the dispatch short-circuit

**Files:**
- Modify: `internal/focus/focus.go`
- Create: `internal/focus/custom.go`

- [ ] **Step 1: Add `JumpCmd` to `focus.Target`**

In `internal/focus/focus.go`, in the `Target` struct (around line 32), add after `PID`:

```go
	// JumpCmd, when non-empty, replaces all built-in focus paths. Run via
	// `sh -c` with session metadata exported as VISOR_* env vars.
	JumpCmd string
}
```

- [ ] **Step 2: Short-circuit in `Dispatch`**

In `internal/focus/focus.go::Dispatch`, immediately after the `var firstErr error` / `tried := 0` lines (around line 43), insert the short-circuit:

```go
	// Custom jump command (set by a launcher via $VISOR_JUMP_CMD at
	// SessionStart) fully replaces the WM and tmux paths. The launcher
	// is presumed authoritative about how to bring its session back.
	if t.JumpCmd != "" {
		return focusCustom(t)
	}
```

- [ ] **Step 3: Create `internal/focus/custom.go`**

Create `internal/focus/custom.go` with:

```go
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
		"VISOR_WM="+t.WM,
		"VISOR_WINDOW_ID="+t.WindowID,
		"VISOR_TMUX_PANE="+t.TmuxPane,
		"VISOR_PID="+strconv.Itoa(t.PID),
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
```

Note: `VISOR_SESSION_ID` and `VISOR_CWD` from the spec aren't on `focus.Target` today — Task 5 adds them via daemon.go (`Target` itself doesn't need them as fields if we pass them through there; we'll thread them via Target additions below).

- [ ] **Step 4: Add `SessionID` and `CWD` to `Target` so they reach `focusCustom`**

The spec passes `VISOR_SESSION_ID` and `VISOR_CWD` to the child. Add them to `Target` in `internal/focus/focus.go` (after `JumpCmd`):

```go
	SessionID string // forwarded to a custom jump command as $VISOR_SESSION_ID
	CWD       string // forwarded to a custom jump command as $VISOR_CWD
```

And extend the `cmd.Env` slice in `focusCustom` to include them:

```go
	cmd.Env = append(os.Environ(),
		"VISOR_SESSION_ID="+t.SessionID,
		"VISOR_WM="+t.WM,
		"VISOR_WINDOW_ID="+t.WindowID,
		"VISOR_TMUX_PANE="+t.TmuxPane,
		"VISOR_PID="+strconv.Itoa(t.PID),
		"VISOR_CWD="+t.CWD,
	)
```

- [ ] **Step 5: Verify it compiles and vet passes**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/focus/focus.go internal/focus/custom.go
git commit -m "feat(focus): add custom jump command dispatch"
```

---

## Task 5: Wire `JumpCmd`, `SessionID`, `CWD` through the daemon's `jump` handler

**Files:**
- Modify: `cmd/visor/daemon.go`

- [ ] **Step 1: Populate the new `Target` fields**

In `cmd/visor/daemon.go`, in the `case "jump":` branch (around line 103), update the `focus.Target{...}` literal to:

```go
				t := focus.Target{
					WM:        sess.WM,
					WindowID:  sess.WindowID,
					TmuxPane:  sess.TmuxPane,
					PID:       sess.PID,
					SessionID: sess.ID,
					CWD:       sess.CWD,
					JumpCmd:   sess.JumpCmd,
				}
```

- [ ] **Step 2: Verify it compiles and vet passes**

Run: `go build -o bin/visor ./cmd/visor && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add cmd/visor/daemon.go
git commit -m "feat(daemon): forward JumpCmd into focus.Target on jump"
```

---

## Task 6: Manual smoke test

This verifies end-to-end. The repo has no automated test suite — do this by hand.

- [ ] **Step 1: Confirm daemon is using the new binary**

Run:
```bash
go build -o bin/visor ./cmd/visor
pkill -f 'visor daemon' 2>/dev/null; sleep 0.5
./bin/visor daemon -v &
sleep 0.3
```

Expected: daemon prints its socket path and a few "ready" lines; no errors.

- [ ] **Step 2: Spawn a tagged claude session via a wrapper**

In a separate terminal, run:
```bash
VISOR_JUMP_CMD='echo "JUMP FIRED at $(date) wm=$VISOR_WM wid=$VISOR_WINDOW_ID sid=$VISOR_SESSION_ID cwd=$VISOR_CWD" >> /tmp/visor-jump-test.log' claude
```

Type any prompt at Claude to make sure SessionStart fires, then exit Claude (or leave it running — only SessionStart is needed).

- [ ] **Step 3: Verify the jump command was persisted**

Run: `./bin/visor ctl json | jq '.[] | {id, wm, window_id}'`
Expected: at least one session listed.

Then inspect the persisted state:
```bash
jq '.sessions[] | {id, jump_cmd}' "$(ls -t ${XDG_STATE_HOME:-$HOME/.local/state}/visor/state.json | head -1)"
```
Expected: the new session shows the `echo ...` string in `jump_cmd`.

- [ ] **Step 4: Trigger the jump**

Pick the session id from step 3 and run:
```bash
./bin/visor ctl jump <id>
cat /tmp/visor-jump-test.log
```

Expected: `/tmp/visor-jump-test.log` contains a `JUMP FIRED at ...` line with the right metadata interpolated. The daemon's stderr should show a `jump` log line with no error.

- [ ] **Step 5: Negative case — no custom command**

Open a normal `claude` session (no `VISOR_JUMP_CMD` set). `./bin/visor ctl jump <id>` for it should still drive the WM / tmux paths (or log the same warnings it logged before this change). The new custom path must not be active.

- [ ] **Step 6: Daemon restart preserves `JumpCmd`**

```bash
pkill -f 'visor daemon'; sleep 0.5; ./bin/visor daemon -v &
sleep 0.3
./bin/visor ctl jump <id-from-step-3>
cat /tmp/visor-jump-test.log
```

Expected: a second `JUMP FIRED ...` line is appended, proving the value survived restart via `persist.go`.

- [ ] **Step 7: Clean up and commit any incidental fixes**

```bash
rm -f /tmp/visor-jump-test.log
pkill -f 'visor daemon' || true
```

If steps 1–6 surfaced any bugs, fix them and commit each fix separately. No commit needed if everything passed.
