# Background-work capture & visualization

**Date:** 2026-06-03
**Status:** Design — approved for planning

## Problem

A Claude Code session can launch background work — a `Bash` command (or agent/workflow)
run with `run_in_background: true`. That work keeps running after the model's turn ends.
The transcript then shows the session as `waiting` (the assistant hit `end_turn`), so
Visor renders it as idle / needs-you — even though work is genuinely in flight. And when
that work finishes, nothing in Visor reflects it. Today we neither capture nor visualize
background work at all.

## Goal

Full lifecycle visibility, as a purely informational signal:

1. Show that a session has in-flight background work, and roughly how much.
2. Mark when background work finishes — success or failure.

Explicitly **not** a goal: background work never reshuffles the dock, never changes the
activity or attention axes, and never nags. It is a glanceable badge only.

## What background work looks like in the transcript

Background work has a concrete, uniform signature in the JSONL. It is the only reliable
signal — there is no hook for background-task completion (see Rejected alternatives).

**Start.** When a task is launched in the background, a `user` line carries a
`tool_result` block whose text reads:

```
Command running in background with ID: bkgqevgds. Output is being written to:
/tmp/.../tasks/bkgqevgds.output. You will be notified when it completes. ...
```

(The same line's `toolUseResult` object also carries `backgroundTaskId`, but the
tool_result text is sufficient and stays within the existing content-decoding model.)

**Finish.** When a task completes or fails, a `<task-notification>` block lands in the
JSONL — once in a `queue-operation` line and once in a `user` line:

```
<task-notification>
<task-id>bkgqevgds</task-id>
<tool-use-id>toolu_…</tool-use-id>
<output-file>/tmp/.../tasks/bkgqevgds.output</output-file>
<status>failed</status>
<summary>Background command "…" failed with exit code 2</summary>
</task-notification>
```

`<status>` is `completed` on success; any other value (e.g. `failed`) is treated as
failure. The mechanism is uniform across background Bash commands, background agents, and
workflows, so all background work is captured the same way with no type special-casing.

## Architecture

Background work becomes a **third orthogonal axis** on a session, alongside the existing
two:

- *Activity* (objective, from JSONL/hooks): `working` / `waiting` / `unknown`
- *Attention* (subjective): `ack` / `needs` / `dismissed`
- *Background* (new, objective, from JSONL): how many tasks are running, and the outcome
  of the last finished batch

The background axis has **zero coupling** to the other two. It is derived state — never
persisted — recomputed by the tailer on restart, exactly like Activity/Waiting.

### Data model

On `state.Session` (internal, not persisted):

```go
BackgroundRunning map[string]bool // task IDs currently in flight
BackgroundOutcome string          // "" | "done" | "failed" — last finished batch
batchFailed       bool            // any task in the current batch failed (internal accumulator)
```

On `state.Snapshot` (public / HUD view), two new non-omitempty fields:

```go
BackgroundRunning int    `json:"background_running"` // count of in-flight tasks
BackgroundOutcome string `json:"background_outcome"` // "" | "done" | "failed"
```

- `background_running` — the count of in-flight tasks. The HUD draws this many dots,
  capped at 3.
- `background_outcome` — set only when `background_running` drops to 0. It is the result
  of the batch that just finished: `"failed"` if **any** task in that batch failed, else
  `"done"`. Clears back to `""` on the next `UserPromptSubmit` for the session.

A "batch" is the span between the running set being empty and returning to empty.
`batchFailed` accumulates while the set is non-empty and resets when the set transitions
empty → non-empty.

### Capture (tailer)

New function in `internal/transcript`, separate from `Classify` (which keeps its
single-activity responsibility):

```go
type BackgroundKind int
const (
    BackgroundStart BackgroundKind = iota
    BackgroundFinish
)

type BackgroundEvent struct {
    TaskID string
    Kind   BackgroundKind
    Failed bool // Finish only
}

func ScanBackground(lines []Line) []BackgroundEvent
```

`ScanBackground` walks the appended lines and recognizes, inside `user`-line content:

- **Start** — a `tool_result` block whose text matches
  `Command running in background with ID: (\w+)` → `{TaskID, BackgroundStart}`.
- **Finish** — a text block containing `<task-notification>` with `<task-id>…</task-id>`
  and `<status>…</status>` → `{TaskID, BackgroundFinish, Failed: status != "completed"}`.

Both markers live in `user` lines, so capture stays within the existing `DecodeContent`
model — no new top-level `Line` fields. The `<task-notification>` appears twice in the
JSONL; because events are keyed by task ID into a set, the duplicate finish is a harmless
no-op (deleting an already-deleted ID does nothing).

`state.ApplyTranscript` folds the events into the session under the existing lock:

- `BackgroundStart`: if the running set was empty, reset `batchFailed = false`; then
  `BackgroundRunning[id] = true`; clear any lingering `BackgroundOutcome`.
- `BackgroundFinish`: `batchFailed = batchFailed || ev.Failed`; `delete(BackgroundRunning, id)`;
  if the set is now empty, set `BackgroundOutcome = "failed"` if `batchFailed` else `"done"`.

### Pub/sub & the digest

`internal/state/notify.go` suppresses no-op broadcasts via a SHA-256 digest over only
HUD-observable fields. Add `background_running` and `background_outcome` to that digest —
otherwise the HUD will not repaint when background state changes.

These fields are **event-driven and low-frequency** (a task starting or finishing), so
adding them is safe. This does not violate the standing rule against widening the digest
with high-frequency fields like `LastUpdate`.

### Clearing the outcome dot

`BackgroundOutcome` clears to `""` in two places:

- On the next `UserPromptSubmit` hook for the session — the unambiguous "user re-engaged"
  signal (a real user prompt line in the transcript classifies as `unknown`, deferring to
  this hook anyway).
- When a new background batch starts (the running set goes empty → non-empty).

It is deliberately **not** cleared on transcript-derived working edges: a `tool_result`
line reads as `working`, and clearing there would wipe a freshly-set outcome dot during
ongoing foreground work. The badge is orthogonal — a green/red dot may sit on a cyan
"working" strip, which is correct.

## Rendering

All dot drawing lives in the shared renderer `internal/hud/render` so the native backends
stay consistent. `render.TabState` gains:

```go
BackgroundRunning int    // 0 = no dots
BackgroundOutcome string // "" | "done" | "failed"
```

`DrawTab` draws dots in the always-visible collapsed strip (the `TabW`=10px × `TabH`=36px
tip):

- **Running** (`BackgroundRunning > 0`): one teal dot (`#8be0d0`) per task, capped at 3,
  stacked vertically from the top.
- **Finished** (`BackgroundRunning == 0`, outcome set): a single dot — green `#a3d977`
  for `"done"`, red `#ff7a7a` for `"failed"`.
- Each dot gets a 1px contrasting outline (reuse the `contrastFG` luminance check against
  the strip color: dark `#10141c` on light strips, light `#e5e9f0` on dark strips) so it
  reads on amber, cyan, grey, or red strips.

**Placement.** Dots sit at the **left edge of the visible tip strip** in both backends —
the inner edge of the tongue that points into the screen, which is the first thing the eye
reaches scanning inward from the screen edge. The dot helper takes the strip's left-x as
its anchor and is the only code that branches on `TabRight`:

- x11 (`TabRight=false`, visible strip = buffer columns `0..TabW`): anchor near `x≈1`.
- wlr (`TabRight=true`, visible strip = `ExpandedW-TabW..ExpandedW`): anchor near
  `x≈ExpandedW-TabW+1`.

Dot geometry: ~4px diameter, ~2px vertical gap, drawn by a small filled-circle helper in
`render` (a simple per-row span fill — no anti-aliasing needed at this size). Because they
live in the always-visible strip region, they show whether the tab is collapsed or
expanded. Static color only (no pulse) — the native backends have no render loop to
animate against; teal alone reads as "active."

**Tuning note (not a design change):** 10px is tight for a 4px dot plus outline. If it
looks cramped in practice, drop the running cap to 2 or shrink dots to 3px.

**eww backend.** The yuck template reads the two new JSON fields from `visor ctl watch`
and renders dots in CSS. eww is a live GTK widget, so it *may* add a CSS pulse animation
for the running state — a backend-local nicety, not part of the shared contract.

## Edge cases

- **Backfill (`isInitial`).** On daemon start the tailer reads the full existing
  transcript. `ScanBackground` over the whole history yields net running =
  starts − finishes; set `BackgroundRunning` from that net count. **Suppress the lingering
  outcome dot on backfill** — historical completions are not news. `BackgroundOutcome` is
  set only by *live* finish events, mirroring how backfill already does not arm attention.
- **Stale running tasks.** A background process dies with its Claude session. A backfilled
  idle transcript could show a task as "running" with no finish notification (the session
  was killed). It would display teal dots indefinitely. Accepted for v1: discovery already
  filters transcripts older than 24h, and live sessions resolve when the notification
  lands. No session-liveness cross-checking in this version.
- **No persistence.** Background state is derived, like Activity/Waiting. It is not written
  to the state file and `persist.go` is unchanged.

## Rejected alternatives

- **Hook-driven capture.** Claude Code exposes no hook for background-task completion; the
  `<task-notification>` lands only in the JSONL. This matches the existing doctrine that
  JSONL is canonical and that some signals (like permission prompts) have a single source.
- **Full per-task detail in v1.** Parsing each task's summary/output path and listing them
  in the expanded hover panel ("2 running: build…, tests…") is deferred. The `ScanBackground`
  seam makes it a clean later addition; v1 ships count + outcome only (YAGNI).

## Success criteria

- A session running background work shows teal dot(s) on its tongue while the work is in
  flight, whether the tab is collapsed or expanded, on x11 and wlr (and eww).
- The dot count reflects the number of in-flight tasks, capped at 3.
- When all background work finishes, a single green (all succeeded) or red (any failed)
  dot replaces the teal dots and lingers until the next prompt to that session.
- The dock never reorders and attention state never changes because of background work.
- Background state survives a daemon restart for live sessions (recomputed from the
  transcript), without surfacing stale historical outcome dots.
