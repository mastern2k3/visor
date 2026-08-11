# Armed tab browsing

Date: 2026-08-11

## Problem

Browsing the dock by hover is uncomfortable. Each tab is its own 44px-tall row
at the right screen edge, and hovering one slides that row's window ~300px
left to reveal its panel. But the *neighbouring* rows are still only sensitive
in an 18px strip pinned at the screen edge. So once the cursor is deep inland
reading one panel, there is no sensitive area beneath it: to reach the next
session you must travel the full ~300px back to the edge, drop into a 18px
target, and come back. Comparing three sessions means three round trips.

## Behaviour

Two states, armed by the user's first deliberate hover.

**Disarmed** (nothing expanded). Sensitivity is exactly what it is today: the
18px edge strip, per row. Approaching the dock from anywhere on screen
triggers nothing early, and the tab you touch is the tab that opens.

**Armed** (one tab expanded). The whole `render.BufW`-wide column becomes
row-sensitive: whichever row the cursor occupies owns the panel, and moving
straight down one row swaps which panel is open. Leaving the column collapses
everything and returns to disarmed.

One rule carries the feel: **the row holding the cursor is the row that is
open, immediately.** Arming does not change that rule — it only changes how much
ground counts as holding the cursor (the edge strip, or the full width a panel
occupies). Both the inland-motion path and the per-tab hover path feed the same
logic, which matters for browsing *along* the edge: those strips are covered by
the tab windows themselves, so the catch window never sees motion there and
per-tab hover is the only thing that can swap rows.

### Revision: the intent timer is gone

The first implementation gated armed row swaps behind a ~70ms dwell, on the
theory that sweeping diagonally out of the dock crosses two or three rows and
would flash each one's panel open. Tested live, that delay was the dominant
complaint — and it was worse than 70ms, because the deferred commit was resolved
on the existing 30Hz animation ticker rather than a real timer, adding up to
33ms of jitter (~87ms average).

It came out entirely, for two reasons:

- The flicker it prevented was asserted, never measured. The dock's own edge
  strips have always popped instantly with no dwell — moving the cursor along
  them expands each tab it crosses — and that has never been reported as a
  problem.
- A crossed row's panel is visible for roughly the ~20ms it takes to sweep
  through, and appears with no animation (an instant window `Move`), so it reads
  as a flicker at most: the same flicker the dock already produced.

Deferring the decision was buying a hypothetical against a certain,
every-interaction cost. Do not reintroduce a swap delay without evidence the
flicker is actually objectionable.

What remains is `DisarmGrace`, a different mechanism despite the similar shape:
a debounce on *disarming*, required only by the wlr backend (see below), where a
bare `wl_pointer.leave` cannot be told apart from an ordinary row crossing. A
late disarm is imperceptible — the panel just stays open a few tens of ms after
the cursor has gone — so it costs nothing.

## Components

### `internal/hud/browse` — the state machine

All interaction logic, no display-server dependency. Backends feed it events
and apply the `Action` values it returns; it never touches X or Wayland
itself, which is what makes it testable in the style of `render`.

```go
type Row struct{ ID string; Top int }
type Action struct {
    Collapse, Expand string // session ids, "" = nothing
    Arm, Disarm      bool   // backend maps/unmaps its catch mechanism
}

func (t *Tracker) SetColumn(x0, x1 int)
func (t *Tracker) SetRows(rows []Row)
func (t *Tracker) Contains(x, y int) bool
func (t *Tracker) Hover(id string, now time.Time) Action
func (t *Tracker) Motion(x, y int, now time.Time) Action
func (t *Tracker) LeaveSurface(id string, now time.Time) Action
func (t *Tracker) Exit() Action
func (t *Tracker) Tick(now time.Time) Action
func (t *Tracker) Drop(id string) Action
```

Row hit-testing is `(y - rows[0].Top) / RowPitch`, so rows are contiguous with
no dead bands — a row's shadow padding belongs to that row rather than being a
gap that stalls browsing.

`Tick` exists only to resolve the disarm debounce, which has to be able to fire
once the pointer has gone and no further events are coming. It is called from
the wlr backend's existing event loop; x11 never calls it, because every
crossing it sees carries root coordinates and is decided synchronously.

### Revision: one catch window per row, and no motion events

The first x11 implementation used a single InputOnly window spanning the whole
column, selecting `PointerMotion` and hit-testing the cursor's y to a row. That
hung the dock permanently within seconds of the first hover.

Root cause, from `kill -QUIT`: motion delivers hundreds of events per second,
which saturates xgb's 100-slot `eventChan`. Once it is full, any *checked* X
request issued from inside an xevent callback deadlocks — the callback waits for
its reply, xgb's reader goroutine is blocked pushing an event into the full
channel, and the only drainer of that channel is the goroutine stuck in the
callback. `setExpanded` has always made such a request
(`shape.RectanglesChecked`) on every hover, so the deadlock was latent long
before this feature; motion is what made it reachable.

Both halves are fixed:

- **One catch window per row**, selecting `EnterWindow | LeaveWindow` only. A row
  change costs one Leave plus one Enter, the channel never fills, and entering a
  window *is* the row identification — no coordinate hit-testing, which also
  deleted `Tracker.Motion` and `Tracker.RowAt`.
- **Every X request reachable from a callback is unchecked**, including
  `setInputRegion`, whose failure mode was already documented as cosmetic.

A second bug surfaced while verifying the first fix: `StackModeBelow` with no
`Sibling` stacks a window at the bottom of the *global* order, beneath other
applications' windows, so the catch windows received nothing inland while edge
hover still worked. They are now stacked below their own row's tab as an
explicit sibling.

### x11 — `internal/hud/x11/catch.go`

An input region is clipped to its own window, and a collapsed tab's window is
positioned with all but ~23px hanging off-screen right. So a collapsed tab
*cannot* be made sensitive to a cursor 300px inland; something else has to see
it.

That something is one `WindowClassInputOnly` child of root: depth 0, no
visual, no colormap, nothing drawn — which sidesteps the ARGB/compositor story
entirely. Event mask `PointerMotion | LeaveWindow`. Unmapped until armed.

- **Geometry**, recomputed at the end of `applySnapshot` because row count
  tracks session count: `x = rightX - BufW`, `y` = the help tab's top,
  `w = BufW`, `h = rowCount * RowPitch`.
- **Stacking**: created before any tab window, and explicitly lowered
  (`ConfigureWindow{StackMode: Below}`) whenever a new tab is created, since
  tabs come and go with sessions.
- **Wiring**: tabs gain `hoverFn`/`leaveFn` callbacks, set by the dock in the
  same place `clickFn` already is; nil preserves the old direct-`setExpanded`
  behaviour. The catch window's motion feeds `Motion`, its leave and each
  tab's leave feed `Exit` — but only after testing `Contains(ev.RootX,
  ev.RootY)`. That root-coord test is what stops a move from the catch window
  onto the expanded tab (a sibling, so a genuine `LeaveNotify`) from
  collapsing anything.

A row swap in x11 costs **zero renders**. `tabState` sets `Expanded: true`
unconditionally — the panel is always drawn into the buffer, and expansion is
purely a window `Move`. So the whole feature adds two `Move` requests per
swap and one `tracker.Tick` per frame, preserving the invariant that the 30Hz
tick never calls `DrawTab`.

Clicks are unchanged. Collapsed tabs' edge strips and the expanded tab both
stay above the catch window. A click on the inland area of a non-hot row hits
the InputOnly window, which selects no button events, so it propagates to root
and does nothing — there is nothing visible there to click.

### wlr — no catch surface needed

`regionTab`/`regionFull` already exist. Arming sets `regionFull` on the
collapsed surfaces; disarming restores `regionTab`. Row swapping needs no
motion handler at all: the surfaces occupy distinct y bands, so the
compositor's own pointer focus identifies the row via `wl_pointer.enter`,
which feeds `Hover` directly.

One correctness detail: `repaint` currently derives the input region from
`state.Expanded` alone, so it must consult the dock's armed flag too or the
next repaint silently reverts an armed surface to `regionTab`.

One wrinkle: `regionFull` spans only `ShadowPad .. ShadowPad+ContentH`, so
there is a real 10px insensitive band between adjacent rows. Crossing it
produces leave-then-enter a millisecond apart. That is what `DisarmGrace` is
for, and why disarm in wlr is a countdown resolved in `Tick` (`LeaveSurface`)
rather than an immediate action on leave.

## Failure modes

- **Hot session disappears mid-browse** (exits, or right-click dismiss):
  `applySnapshot` calls `Drop`, which collapses it and clears the hot row but
  **stays armed**, so the next motion picks up a row normally.
- **Hovering with no hot row** (immediately after a `Drop`) expands without
  emitting a `Collapse` — the tab that would have been collapsed has already
  been destroyed.
- **`!argb`** is unaffected in both backends. The catch window is InputOnly,
  independent of visual depth, and the existing black-column limitation is
  untouched.

## Tests

Following the repo's existing split — pure logic gets deep coverage, anything
needing a live display server gets none.

- `browse`: table-driven with an injected clock. Arm on first hover; inland row
  swaps are immediate; motion within the hot row emits nothing (so the backend
  does not re-`Move` the same window on every motion event); a diagonal exit
  ends collapsed and disarmed with nothing left to fire; a wlr pad-band
  crossing does not disarm but a real leave does after `DisarmGrace`;
  drop-the-hot-row keeps the tracker armed; rows are contiguous with no dead
  bands.
- x11: a pure geometry test for the catch rect derived from row layout, in the
  shape of the existing `tickX`/`computeRightMargin` tests.
- No display-server tests, no golden images.

## Deliberately not doing: one window for the whole dock

A single dock window spanning the column would make this problem nearly
disappear — the dock window is already inland, so `MotionNotify` on it gives
the row directly and the catch window would not exist. It would also unpin
`RowPitch == BufH` (that constraint exists only so each shadow fits inside its
own window), letting tabs sit closer with blended shadows, and make the alpha
between tabs a property of one buffer.

The reason it is not happening now: **one window per tab is what makes
animation free.** Wobble today is `win.Move` — zero drawing, zero upload,
which is precisely why the 30Hz tick can run with `DrawTab` gated off. In a
single window, moving a capsule means redrawing that strip and re-uploading
it: three working tabs would cost 3 `DrawTab` (each including the triple box
blur) plus ~216KB of upload per frame, 30 times a second, against today's
zero. It is recoverable — cache each tab's rendered RGBA and `memcpy` it into
the dock buffer at the wobble offset, then `PutImage` only the damaged
sub-rect, since wobble is pure translation — but that is hand-writing a
compositor with damage tracking to replace one the X server already provides.

The catch window is ~30 lines and gets deleted by that refactor with nothing
stranded, so shipping it now costs almost nothing later. The refactor becomes
clearly correct the moment something needs to span rows: overlapping shadows,
a hover panel taller than its row, a dock header/footer, or drag-to-reorder.
