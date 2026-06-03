# Background-Work Capture & Visualization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture background-task lifecycle from Claude Code transcripts and surface it as outlined pip dots on each HUD tab — without touching the activity or attention axes.

**Architecture:** A new orthogonal "Background" axis on each session, derived purely from JSONL markers (a `tool_result` "running in background" start line, and a `<task-notification>` finish line). The tailer folds start/finish events into a per-session running set plus a last-batch outcome. Both native backends (x11, wlr) and eww render small dots in the always-visible tab tip via the shared `internal/hud/render` package.

**Tech Stack:** Go (`github.com/nitzanz/visor`), `image/draw` for pixel rendering, eww/yuck+scss for the GTK backend. `go test` for the pure logic (transcript scanning, state folding, dot pixels).

**Spec:** `docs/superpowers/specs/2026-06-03-background-work-visualization-design.md`

---

## File Structure

**Create:**
- `internal/transcript/background.go` — `ScanBackground` + `BackgroundEvent`/`BackgroundKind` types.
- `internal/transcript/background_test.go` — table tests for the scanner.
- `internal/state/background_test.go` — tests for the per-session folding helper + digest.

**Modify:**
- `internal/state/state.go` — new `Session` fields, `Snapshot` fields, a folding helper, wiring in `ApplyTranscript`, populate in `Snapshot()`.
- `internal/state/hooks.go` — clear outcome on `UserPromptSubmit`.
- `internal/state/notify.go` — add the two new fields to `hudDigest`.
- `internal/hud/render/tab.go` — `TabState` fields + dot drawing + a filled-circle helper.
- `internal/hud/render/tab_test.go` — pixel tests for dots.
- `internal/hud/x11/subscribe.go` — `sessionView` fields.
- `internal/hud/x11/tab.go` — pass fields into `TabState`; re-render on change.
- `internal/hud/wlr/subscribe.go` — `sessionView` fields.
- `internal/hud/wlr/dock.go` — pass fields into `TabState`.
- `internal/hud/eww/eww.yuck` + `internal/hud/eww/eww.scss` — render dots from JSON.

---

## Task 1: Transcript scanner (`ScanBackground`)

**Files:**
- Create: `internal/transcript/background.go`
- Test: `internal/transcript/background_test.go`

This is a pure function over already-parsed `Line`s. It walks `user` lines and emits a start event for each "Command running in background with ID: X" tool_result, and a finish event for each `<task-notification>` block.

- [ ] **Step 1: Write the failing test**

Create `internal/transcript/background_test.go`:

```go
package transcript

import (
	"encoding/json"
	"testing"
)

// userLine builds a `user` Line whose message.content is the given JSON blocks.
func userLine(contentJSON string) Line {
	return Line{Type: "user", Message: &MessageBody{Role: "user", Content: json.RawMessage(contentJSON)}}
}

func TestScanBackground_Start(t *testing.T) {
	lines := []Line{userLine(`[{"type":"tool_result","content":"Command running in background with ID: bkgABC. Output is being written to: /tmp/x. You will be notified when it completes."}]`)}
	got := ScanBackground(lines)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Kind != BackgroundStart || got[0].TaskID != "bkgABC" {
		t.Errorf("got %+v, want Start bkgABC", got[0])
	}
}

func TestScanBackground_FinishCompleted(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgABC</task-id>\n<status>completed</status>\n<summary>ok</summary>\n</task-notification>"}]`
	got := ScanBackground([]Line{userLine(content)})
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Kind != BackgroundFinish || got[0].TaskID != "bkgABC" || got[0].Failed {
		t.Errorf("got %+v, want Finish bkgABC failed=false", got[0])
	}
}

func TestScanBackground_FinishFailed(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgZ9</task-id>\n<status>failed</status>\n</task-notification>"}]`
	got := ScanBackground([]Line{userLine(content)})
	if len(got) != 1 || got[0].Kind != BackgroundFinish || !got[0].Failed {
		t.Fatalf("got %+v, want Finish failed=true", got)
	}
}

func TestScanBackground_IgnoresUnrelated(t *testing.T) {
	lines := []Line{
		userLine(`[{"type":"text","text":"just a normal message"}]`),
		{Type: "assistant", Message: &MessageBody{Role: "assistant", StopReason: "end_turn"}},
	}
	if got := ScanBackground(lines); len(got) != 0 {
		t.Errorf("got %d events, want 0: %+v", len(got), got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transcript/ -run TestScanBackground -v`
Expected: FAIL — `undefined: ScanBackground`, `undefined: BackgroundStart`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/transcript/background.go`:

```go
package transcript

import "regexp"

// BackgroundKind distinguishes a task launch from a task completion.
type BackgroundKind int

const (
	BackgroundStart BackgroundKind = iota
	BackgroundFinish
)

// BackgroundEvent is one background-task lifecycle marker found in the
// transcript. TaskID is the Claude-assigned background task id (e.g. "bkgABC").
// Failed is meaningful only for BackgroundFinish.
type BackgroundEvent struct {
	TaskID string
	Kind   BackgroundKind
	Failed bool
}

// startRe matches the tool_result text emitted when a command is launched in
// the background. The id is alphanumeric (Claude uses a short "bkg…" token).
var startRe = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9]+)`)

// taskIDRe / statusRe extract fields from a <task-notification> finish block.
var taskIDRe = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
var statusRe = regexp.MustCompile(`<status>([^<]+)</status>`)

// ScanBackground walks parsed lines (any order) and returns the background
// lifecycle events found in user-line content. Both markers live inside
// user-line content blocks, so we decode content the same way Classify does.
//
// A <task-notification> can appear in the JSONL twice (a queue-operation line
// and a user line); only the user line is inspected here, and callers key
// events by TaskID into a set so a duplicate finish is a harmless no-op.
func ScanBackground(lines []Line) []BackgroundEvent {
	var out []BackgroundEvent
	for _, ln := range lines {
		if ln.Type != "user" || ln.Message == nil {
			continue
		}
		for _, b := range DecodeContent(ln.Message.Content) {
			text := b.Text
			if b.Type == "tool_result" {
				// tool_result content is itself polymorphic; the human-readable
				// string lands in Block.Text when content is a bare string, or
				// in ContentRM otherwise. Check both.
				if text == "" && len(b.ContentRM) > 0 {
					text = string(b.ContentRM)
				}
			}
			if text == "" {
				continue
			}
			if m := startRe.FindStringSubmatch(text); m != nil {
				out = append(out, BackgroundEvent{TaskID: m[1], Kind: BackgroundStart})
				continue
			}
			if id := taskIDRe.FindStringSubmatch(text); id != nil {
				failed := true
				if st := statusRe.FindStringSubmatch(text); st != nil && st[1] == "completed" {
					failed = false
				}
				out = append(out, BackgroundEvent{TaskID: id[1], Kind: BackgroundFinish, Failed: failed})
			}
		}
	}
	return out
}
```

Note: `DecodeContent` already maps a bare-string `content` to a single `{Type:"text", Text:…}` block, and a `tool_result` object keeps its string body in `Block.Text` when the JSON `content` is a string. The `ContentRM` fallback covers the array-bodied tool_result form.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transcript/ -run TestScanBackground -v`
Expected: PASS (all four subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/transcript/background.go internal/transcript/background_test.go
git commit -m "feat(transcript): scan background-task start/finish markers"
```

---

## Task 2: Per-session folding helper + wiring

**Files:**
- Modify: `internal/state/state.go` (add `Session` fields; add `applyBackground` helper; call it in `ApplyTranscript`)
- Modify: `internal/state/hooks.go` (clear outcome on `UserPromptSubmit`)
- Test: `internal/state/background_test.go`

The folding logic lives in a small method so it is testable without a full `Store`. A "batch" is the span the running set is non-empty; `batchFailed` accumulates failures and resets when a new batch starts.

- [ ] **Step 1: Add the new fields to `Session`**

In `internal/state/state.go`, inside the `Session` struct (after the `Activity/Waiting/Attention` block, around line 80), add:

```go
	// Background work axis (objective, derived from JSONL; never persisted).
	// BackgroundRunning is the set of in-flight background task IDs.
	// BackgroundOutcome is the result of the last finished batch:
	// "" | "done" | "failed". batchFailed accumulates failures within the
	// current batch and resets when the running set goes empty→non-empty.
	BackgroundRunning map[string]bool `json:"-"`
	BackgroundOutcome string          `json:"-"`
	batchFailed       bool
```

- [ ] **Step 2: Write the failing test**

Create `internal/state/background_test.go`:

```go
package state

import (
	"testing"

	"github.com/nitzanz/visor/internal/transcript"
)

func ev(id string, kind transcript.BackgroundKind, failed bool) transcript.BackgroundEvent {
	return transcript.BackgroundEvent{TaskID: id, Kind: kind, Failed: failed}
}

func TestApplyBackground_RunningCount(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("b", transcript.BackgroundStart, false),
	}, false)
	if len(s.BackgroundRunning) != 2 {
		t.Fatalf("running=%d, want 2", len(s.BackgroundRunning))
	}
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want empty while running", s.BackgroundOutcome)
	}
}

func TestApplyBackground_OutcomeDone(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundStart, false)}, false)
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundFinish, false)}, false)
	if len(s.BackgroundRunning) != 0 {
		t.Fatalf("running=%d, want 0", len(s.BackgroundRunning))
	}
	if s.BackgroundOutcome != "done" {
		t.Errorf("outcome=%q, want done", s.BackgroundOutcome)
	}
}

func TestApplyBackground_AnyFailureFailsBatch(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("b", transcript.BackgroundStart, false),
	}, false)
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundFinish, false),
		ev("b", transcript.BackgroundFinish, true),
	}, false)
	if s.BackgroundOutcome != "failed" {
		t.Errorf("outcome=%q, want failed", s.BackgroundOutcome)
	}
}

func TestApplyBackground_NewBatchClearsOutcome(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundStart, false)}, false)
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundFinish, true)}, false)
	if s.BackgroundOutcome != "failed" {
		t.Fatalf("setup: outcome=%q, want failed", s.BackgroundOutcome)
	}
	s.applyBackground([]transcript.BackgroundEvent{ev("b", transcript.BackgroundStart, false)}, false)
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want cleared when new batch starts", s.BackgroundOutcome)
	}
}

func TestApplyBackground_BackfillSuppressesOutcome(t *testing.T) {
	s := &Session{}
	// Whole history replayed at once on daemon start: net running=0, but the
	// completion is historical and must NOT light an outcome dot.
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("a", transcript.BackgroundFinish, false),
	}, true)
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want empty on backfill", s.BackgroundOutcome)
	}
	if len(s.BackgroundRunning) != 0 {
		t.Errorf("running=%d, want 0", len(s.BackgroundRunning))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/state/ -run TestApplyBackground -v`
Expected: FAIL — `s.applyBackground undefined`.

- [ ] **Step 4: Implement the folding helper**

In `internal/state/state.go`, add this method (near `ApplyTranscript`):

```go
// applyBackground folds background lifecycle events into the session's
// Background axis. On backfill (isInitial) the net running count is computed
// but no outcome dot is surfaced — historical completions aren't news.
func (sess *Session) applyBackground(events []transcript.BackgroundEvent, isInitial bool) {
	for _, e := range events {
		switch e.Kind {
		case transcript.BackgroundStart:
			if len(sess.BackgroundRunning) == 0 {
				// New batch begins: reset accumulators and clear any
				// lingering outcome from the previous batch.
				sess.batchFailed = false
				sess.BackgroundOutcome = ""
			}
			if sess.BackgroundRunning == nil {
				sess.BackgroundRunning = map[string]bool{}
			}
			sess.BackgroundRunning[e.TaskID] = true
		case transcript.BackgroundFinish:
			if e.Failed {
				sess.batchFailed = true
			}
			delete(sess.BackgroundRunning, e.TaskID)
			if len(sess.BackgroundRunning) == 0 && !isInitial {
				if sess.batchFailed {
					sess.BackgroundOutcome = "failed"
				} else {
					sess.BackgroundOutcome = "done"
				}
			}
		}
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/state/ -run TestApplyBackground -v`
Expected: PASS (all five subtests).

- [ ] **Step 6: Wire into `ApplyTranscript`**

In `internal/state/state.go`, inside `ApplyTranscript`, just after the existing `if len(lines) > 0 { newAct := transcript.Classify(lines) … }` block (around line 315), add:

```go
	if len(lines) > 0 {
		sess.applyBackground(transcript.ScanBackground(lines), isInitial)
	}
```

- [ ] **Step 7: Clear outcome on `UserPromptSubmit`**

In `internal/state/hooks.go`, inside `ApplyHook`'s `switch event`, change the `UserPromptSubmit` case to also clear the outcome:

```go
	case "UserPromptSubmit":
		sess.BackgroundOutcome = ""
		s.transition(sess, transcript.ActivityWorking, WaitingNone)
```

- [ ] **Step 8: Build and run all state/transcript tests**

Run: `go build ./... && go test ./internal/state/ ./internal/transcript/`
Expected: PASS, no build errors.

- [ ] **Step 9: Commit**

```bash
git add internal/state/state.go internal/state/hooks.go internal/state/background_test.go
git commit -m "feat(state): fold background events into a per-session Background axis"
```

---

## Task 3: Snapshot fields + digest

**Files:**
- Modify: `internal/state/state.go` (`Snapshot` struct + `Snapshot()` population)
- Modify: `internal/state/notify.go` (`hudDigest`)
- Test: `internal/state/background_test.go` (append a digest test)

- [ ] **Step 1: Write the failing test**

Append to `internal/state/background_test.go`:

```go
func TestHudDigest_ChangesWithBackground(t *testing.T) {
	base := []Snapshot{{ID: "x", Activity: "waiting"}}
	withRunning := []Snapshot{{ID: "x", Activity: "waiting", BackgroundRunning: 2}}
	withOutcome := []Snapshot{{ID: "x", Activity: "waiting", BackgroundOutcome: "failed"}}
	if hudDigest(base) == hudDigest(withRunning) {
		t.Error("digest ignored background_running")
	}
	if hudDigest(base) == hudDigest(withOutcome) {
		t.Error("digest ignored background_outcome")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run TestHudDigest -v`
Expected: FAIL — `unknown field BackgroundRunning in struct literal`.

- [ ] **Step 3: Add fields to `Snapshot`**

In `internal/state/state.go`, in the `Snapshot` struct (after `Attention string`, around line 112), add:

```go
	BackgroundRunning int    `json:"background_running"`
	BackgroundOutcome string `json:"background_outcome"`
```

- [ ] **Step 4: Populate them in `Snapshot()`**

In `internal/state/state.go`, in the `Snapshot()` method's struct literal (around line 392), add after `Attention: sess.Attention.String(),`:

```go
			BackgroundRunning: len(sess.BackgroundRunning),
			BackgroundOutcome: sess.BackgroundOutcome,
```

- [ ] **Step 5: Add fields to `hudDigest`**

In `internal/state/notify.go`, inside `hudDigest`'s per-snapshot loop (after the `s.Title` write, before the trailing `h.Write([]byte{1})`), add:

```go
		h.Write([]byte(strconv.Itoa(s.BackgroundRunning)))
		h.Write([]byte{0})
		h.Write([]byte(s.BackgroundOutcome))
		h.Write([]byte{0})
```

Add `"strconv"` to the imports in `internal/state/notify.go`.

- [ ] **Step 6: Run test + build**

Run: `go build ./... && go test ./internal/state/ -run TestHudDigest -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/state/notify.go internal/state/background_test.go
git commit -m "feat(state): expose background fields on Snapshot and digest"
```

---

## Task 4: Render dots (shared renderer)

**Files:**
- Modify: `internal/hud/render/tab.go` (`TabState` fields, filled-circle helper, dot drawing in `DrawTab`)
- Test: `internal/hud/render/tab_test.go`

Dots render in the always-visible tip strip, anchored to the strip's left edge, stacked from the top. Teal per running task (cap 3); a single green/red dot when finished. Each dot gets a 1px contrasting outline.

- [ ] **Step 1: Write the failing test**

Append to `internal/hud/render/tab_test.go`:

```go
func TestDrawTab_RunningDotsTeal(t *testing.T) {
	// Collapsed amber tab with 2 running bg tasks → teal dots near the top
	// of the left edge of the visible strip (x11 strip = columns 0..TabW).
	img := DrawTab(TabState{Color: 0xebcb8b, Expanded: false, BackgroundRunning: 2}, nil)
	// A pixel at the first dot's center should be teal-ish (the dotRunning color).
	got := img.RGBA.RGBAAt(dotInset+dotRadius, dotTop+dotRadius)
	want := unpackRGBA(dotRunning)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("first running dot = %v, want %v", got, want)
	}
}

func TestDrawTab_OutcomeDotFailed(t *testing.T) {
	img := DrawTab(TabState{Color: 0x6b7280, Expanded: false, BackgroundOutcome: "failed"}, nil)
	got := img.RGBA.RGBAAt(dotInset+dotRadius, dotTop+dotRadius)
	want := unpackRGBA(dotFailed)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("outcome dot = %v, want %v (red)", got, want)
	}
}

func TestDrawTab_NoBackgroundNoDots(t *testing.T) {
	// With no bg work, the dot region keeps the strip's bg color.
	img := DrawTab(TabState{Color: 0x6b7280, Expanded: false}, nil)
	got := img.RGBA.RGBAAt(dotInset+dotRadius, dotTop+dotRadius)
	want := unpackRGBA(0x6b7280)
	if got != want {
		t.Errorf("dot region = %v, want strip bg %v", got, want)
	}
}

func TestDrawTab_DotsRightStripWlr(t *testing.T) {
	// TabRight=true: visible strip is the rightmost TabW columns; dots anchor
	// to the left edge of THAT strip (x ≈ ExpandedW-TabW).
	img := DrawTab(TabState{Color: 0x6b7280, Expanded: false, TabRight: true, BackgroundRunning: 1}, nil)
	x := ExpandedW - TabW + dotInset + dotRadius
	got := img.RGBA.RGBAAt(x, dotTop+dotRadius)
	want := unpackRGBA(dotRunning)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("wlr running dot at x=%d = %v, want %v", x, got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hud/render/ -run TestDrawTab_ -v`
Expected: FAIL — `undefined: dotInset`, `undefined: dotRunning`, etc.

- [ ] **Step 3: Add `TabState` fields + dot constants**

In `internal/hud/render/tab.go`, add to the `TabState` struct (after `TabRight bool`):

```go
	// Background work axis. BackgroundRunning > 0 draws that many running
	// dots (capped at dotMaxRunning). When 0 and BackgroundOutcome is set,
	// a single outcome dot is drawn instead.
	BackgroundRunning int
	BackgroundOutcome string // "" | "done" | "failed"
```

Add to the `const` block at the top of the file:

```go
	dotRadius    = 2  // dot is 2*dotRadius+1 px across
	dotInset     = 2  // px from the strip's left edge to the dot's left edge
	dotTop       = 3  // y of the topmost dot's top edge
	dotGap       = 3  // vertical gap between stacked dots
	dotMaxRunning = 3 // cap on running dots drawn
```

And these packed colors (next to where `ColorFor` returns colors, for locality):

```go
const (
	dotRunning uint32 = 0x8be0d0 // teal — task in flight
	dotDone    uint32 = 0xa3d977 // green — batch completed ok
	dotFailed  uint32 = 0xff7a7a // red — batch had a failure
)
```

- [ ] **Step 4: Implement the filled-circle helper + dot drawing**

In `internal/hud/render/tab.go`, add a helper:

```go
// fillDot paints a filled circle of radius dotRadius centered at (cx, cy)
// with a 1px contrasting outline. No anti-aliasing — at radius 2 a plain
// distance test reads cleanly. outline is drawn first (as a slightly larger
// disc), then the fill on top.
func fillDot(img *image.RGBA, cx, cy int, fill, outline color.RGBA) {
	r := dotRadius
	for dy := -r - 1; dy <= r+1; dy++ {
		for dx := -r - 1; dx <= r+1; dx++ {
			d2 := dx*dx + dy*dy
			var c color.RGBA
			switch {
			case d2 <= r*r:
				c = fill
			case d2 <= (r+1)*(r+1):
				c = outline
			default:
				continue
			}
			img.SetRGBA(cx+dx, cy+dy, c)
		}
	}
}

// drawBackgroundDots paints running/outcome dots onto the visible tip strip.
// stripLeftX is the buffer x of the strip's left edge (0 for x11; ExpandedW-TabW
// for wlr). bg is the strip color, used to pick a contrasting outline.
func drawBackgroundDots(img *image.RGBA, s TabState, stripLeftX int, bg color.RGBA) {
	outline := contrastFG(bg)
	cx := stripLeftX + dotInset + dotRadius
	draw := func(i int, packed uint32) {
		cy := dotTop + dotRadius + i*(2*dotRadius+1+dotGap)
		fillDot(img, cx, cy, unpackRGBA(packed), outline)
	}
	if s.BackgroundRunning > 0 {
		n := s.BackgroundRunning
		if n > dotMaxRunning {
			n = dotMaxRunning
		}
		for i := 0; i < n; i++ {
			draw(i, dotRunning)
		}
		return
	}
	switch s.BackgroundOutcome {
	case "done":
		draw(0, dotDone)
	case "failed":
		draw(0, dotFailed)
	}
}
```

In `DrawTab`, draw the dots in **both** the collapsed and expanded paths so they always show. The expanded path has an early `return out` when `font == nil`, so the dot-draw must go **before** that check (right after `out := TabImage{RGBA: img}`) — otherwise dots vanish when the font fails to load.

For the collapsed branch the block becomes:

```go
	if !s.Expanded {
		transparent := color.RGBA{0, 0, 0, 0}
		draw.Draw(img, panelRect, &image.Uniform{C: transparent}, image.Point{}, draw.Src)
		stripLeftX := 0
		if s.TabRight {
			stripLeftX = ExpandedW - TabW
		}
		drawBackgroundDots(img, s, stripLeftX, bg)
		return TabImage{RGBA: img}
	}
```

And in the expanded path, insert the dot-draw immediately after `out := TabImage{RGBA: img}` and before the `if font == nil || s.Label == ""` check:

```go
	out := TabImage{RGBA: img}
	stripLeftX := 0
	if s.TabRight {
		stripLeftX = ExpandedW - TabW
	}
	drawBackgroundDots(img, s, stripLeftX, bg)
	if font == nil || s.Label == "" {
		return out
	}
```

(`image` and `image/color` and `image/draw` are already imported.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/hud/render/ -run TestDrawTab -v`
Expected: PASS — both the new dot tests and the pre-existing `TestDrawTab_*` tests (the dot region in those tests sits where no dots are drawn, so background-fill assertions at `x=2`/`x=200`, `TabH/2` are unaffected — dots are near the top, `y≈5`).

Note: the pre-existing `TestDrawTab_BackgroundFill` samples `(2, TabH/2)` = `(2, 18)`. The topmost dot center is `(dotInset+dotRadius, dotTop+dotRadius)` = `(4, 5)` with radius+outline reaching `y≈7`, so it does not collide with `y=18`. If a future tweak moves dots down, update that test.

- [ ] **Step 6: Commit**

```bash
git add internal/hud/render/tab.go internal/hud/render/tab_test.go
git commit -m "feat(render): draw outlined background-work dots on the tab tip"
```

---

## Task 5: Wire the x11 backend

**Files:**
- Modify: `internal/hud/x11/subscribe.go` (`sessionView` fields)
- Modify: `internal/hud/x11/tab.go` (`render()` passes fields; `update()` re-renders on change)

No unit test — this is rendering wiring verified by build + manual run. The dot pixels are already covered in Task 4.

- [ ] **Step 1: Add fields to x11 `sessionView`**

In `internal/hud/x11/subscribe.go`, in the `sessionView` struct (after `Title string`):

```go
	BackgroundRunning int    `json:"background_running"`
	BackgroundOutcome string `json:"background_outcome"`
```

- [ ] **Step 2: Pass fields into `TabState` in `render()`**

In `internal/hud/x11/tab.go`, in `tab.render()`, extend the `render.TabState{…}` literal (around line 412):

```go
	rt := render.DrawTab(render.TabState{
		Color:             t.opt.color,
		Label:             displayLabel(t.sess),
		Expanded:          true, // x11 uses positional window-slide; always render full opaque panel
		BackgroundRunning: t.sess.BackgroundRunning,
		BackgroundOutcome: t.sess.BackgroundOutcome,
	}, t.font)
```

- [ ] **Step 3: Re-render when background fields change**

In `internal/hud/x11/tab.go`, in `tab.update()`, change the re-render condition (around line 109):

```go
	if color != prevColor ||
		displayLabel(s) != displayLabel(prevSess) ||
		s.BackgroundRunning != prevSess.BackgroundRunning ||
		s.BackgroundOutcome != prevSess.BackgroundOutcome {
		t.render()
	}
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 5: Manual smoke test (optional but recommended)**

Run the daemon and x11 HUD, then launch a background task in a Claude session (e.g. ask it to run a long `sleep 60 &`-style background command). Expect teal dot(s) on that session's tab tip, turning into a single green/red dot when it finishes, clearing on your next prompt.

```bash
go build -o bin/visor ./cmd/visor
./bin/visor daemon -v &
./bin/visor hud open --backend=x11
```

- [ ] **Step 6: Commit**

```bash
git add internal/hud/x11/subscribe.go internal/hud/x11/tab.go
git commit -m "feat(hud/x11): render background-work dots from snapshot"
```

---

## Task 6: Wire the wlr backend

**Files:**
- Modify: `internal/hud/wlr/subscribe.go` (`sessionView` fields)
- Modify: `internal/hud/wlr/dock.go` (`applySnapshot` passes fields into `TabState`)

`TabState` is compared by value in wlr (`ls.state != st`) to decide repaints; the new `int` and `string` fields keep it comparable, so background changes trigger a repaint automatically.

- [ ] **Step 1: Add fields to wlr `sessionView`**

In `internal/hud/wlr/subscribe.go`, in the `sessionView` struct (after `Title string`):

```go
	BackgroundRunning int    `json:"background_running"`
	BackgroundOutcome string `json:"background_outcome"`
```

- [ ] **Step 2: Pass fields into `TabState`**

In `internal/hud/wlr/dock.go`, in `applySnapshot` (around line 332), extend the `render.TabState{…}` literal:

```go
		st := render.TabState{
			Color:             colorFor(s),
			Label:             labelFor(s),
			TabRight:          true,
			BackgroundRunning: s.BackgroundRunning,
			BackgroundOutcome: s.BackgroundOutcome,
		}
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 4: Manual smoke test (optional, requires a wlroots compositor)**

```bash
go build -o bin/visor ./cmd/visor
./bin/visor daemon -v &
./bin/visor hud open --backend=wlr
```
Expect dots on the right-anchored tab tip (left edge of the visible strip), same lifecycle as x11.

- [ ] **Step 5: Commit**

```bash
git add internal/hud/wlr/subscribe.go internal/hud/wlr/dock.go
git commit -m "feat(hud/wlr): render background-work dots from snapshot"
```

---

## Task 7: Wire the eww backend

**Files:**
- Modify: `internal/hud/eww/eww.yuck` (render dots from JSON)
- Modify: `internal/hud/eww/eww.scss` (dot styling + pulse)

eww consumes the raw `visor ctl watch` JSON, so `s.background_running` and `s.background_outcome` are already present in each session object — only the template and styles need updating. eww is a live GTK widget, so it can afford a CSS pulse animation for the running state.

- [ ] **Step 1: Add a dots widget to the tab**

In `internal/hud/eww/eww.yuck`, replace the `(box :class "visor-bar" :width 8 :height 28)` line inside `defwidget tab` with a bar that overlays dots:

```lisp
      (box :class "visor-bar" :width 8 :height 28
           :orientation "v"
           :space-evenly false
           :valign "start"
        (literal :content {s.background_running > 0
          ? "(box :class \"visor-dots visor-dots-running\" :orientation \"v\" :space-evenly false"
            + (s.background_running >= 1 ? " (box :class \"visor-dot\")" : "")
            + (s.background_running >= 2 ? " (box :class \"visor-dot\")" : "")
            + (s.background_running >= 3 ? " (box :class \"visor-dot\")" : "")
            + ")"
          : (s.background_outcome != ""
              ? "(box :class \"visor-dots\" (box :class \"visor-dot visor-dot-${s.background_outcome}\"))"
              : "(box)")})))
```

Note: `literal` evaluates a yuck string at runtime, which is how eww renders a variable number of children. The `${s.background_outcome}` interpolation yields class `visor-dot-done` or `visor-dot-failed`.

- [ ] **Step 2: Add dot styles**

In `internal/hud/eww/eww.scss`, add:

```scss
.visor-dots {
  margin-top: 2px;
}
.visor-dot {
  min-width: 5px;
  min-height: 5px;
  margin: 1px auto;
  border-radius: 50%;
  border: 1px solid rgba(0, 0, 0, 0.55);
  background-color: #8be0d0; // teal — running (default)
}
.visor-dots-running .visor-dot {
  animation: visor-dot-pulse 1.4s ease-in-out infinite;
}
.visor-dot-done {
  background-color: #a3d977;
  animation: none;
}
.visor-dot-failed {
  background-color: #ff7a7a;
  animation: none;
}
@keyframes visor-dot-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}
```

- [ ] **Step 3: Validate the eww config still parses (if eww is installed)**

Run: `./bin/visor hud install --backend=eww && eww -c ~/.config/eww/visor reload`
Expected: no parse errors. If eww is not installed, skip — the yuck change is covered by manual inspection.

Note: if the `literal`-based variable-children approach proves brittle in your eww version, the fallback is three statically-defined dot boxes whose `:visible` is bound to `{s.background_running >= 1}`, `{>= 2}`, `{>= 3}`, plus one outcome dot with `:visible {s.background_running == 0 && s.background_outcome != ""}`. This avoids `literal` entirely at the cost of more verbose yuck.

- [ ] **Step 4: Commit**

```bash
git add internal/hud/eww/eww.yuck internal/hud/eww/eww.scss
git commit -m "feat(hud/eww): render background-work dots from snapshot JSON"
```

---

## Final verification

- [ ] **Step 1: Full build + test**

Run: `go build ./... && go test ./...`
Expected: PASS across `internal/transcript`, `internal/state`, `internal/hud/render` (other packages have no new tests).

- [ ] **Step 2: End-to-end manual check**

Start the daemon and your preferred HUD backend, open a Claude session, and have it run a background command. Confirm: teal dot appears while running; a green dot replaces it on success (red on failure); the dot count tracks multiple concurrent tasks (capped at 3); the dock does not reorder; the outcome dot clears when you send the next prompt to that session.

- [ ] **Step 3: Commit any final tweaks** (e.g. dot geometry adjustments from the smoke test)

```bash
git add -A && git commit -m "chore: background-work dot polish"
```
