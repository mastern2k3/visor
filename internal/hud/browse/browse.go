// Package browse implements the dock's "armed browsing" interaction: the rule
// that decides which tab is expanded as the cursor moves through the dock.
//
// The problem it solves is ergonomic. Each tab occupies one row at the right
// screen edge and is only sensitive in a narrow strip there, but expanding one
// slides its panel ~300px inland. Once the cursor is inland reading a panel it
// sits over nothing sensitive, so reaching the next session means travelling
// all the way back to the edge. Armed browsing makes the whole panel-width
// column row-sensitive, but only while the user is already browsing:
//
//	disarmed  only the edge strips are sensitive
//	armed     the whole column is row-sensitive
//
// In both states the rule is the same and immediate: the row holding the cursor
// is the row that is open. Arming only changes how much ground counts as
// "holding the cursor" — the edge strip, or the full width a panel occupies.
//
// Nothing here touches X or Wayland. Backends feed it events and apply the
// Action values it returns, which is what lets the whole state machine be
// tested without a display server — the same split the render package uses.
package browse

import "time"

// DisarmGrace is how long a "pointer left the surface" report is held before it
// counts as leaving the dock. It exists only for backends that learn about
// crossings without coordinates — see LeaveSurface, which is the wlr path.
//
// Note what this is NOT. Row swaps have no delay at all: the cursor's row is
// known exactly and immediately, so the matching panel opens immediately. An
// earlier draft made swaps wait out a dwell on the theory that sweeping out of
// the dock diagonally would flash every crossed row's panel open on the way.
// That was never measured, and the dock's own edge strips have always behaved
// without a dwell — crossing them pops each tab instantly and reads fine. The
// delay was costing real responsiveness on every deliberate row change to
// prevent a flicker nobody had complained about.
//
// A late disarm, by contrast, is imperceptible: the panel just stays open a few
// tens of ms longer after the cursor has already left.
const DisarmGrace = 70 * time.Millisecond

// Row is one tab's position in the column, in root coordinates. Top is the
// row's upper edge; height is uniform (the pitch passed to New), so rows are
// contiguous and a row's shadow padding belongs to that row rather than being
// a gap that stalls browsing.
type Row struct {
	ID  string
	Top int
}

// Action is what the backend should do. Every field is independently optional;
// the zero Action means "nothing changed", which is what the vast majority of
// calls (notably every Tick) return.
type Action struct {
	// Collapse and Expand are session ids, or "" for none. When both are set
	// they are a swap and the backend should apply Collapse first.
	Collapse string
	Expand   string

	// Arm and Disarm tell the backend to start/stop making the full column
	// row-sensitive: mapping the InputOnly catch window in x11, widening the
	// collapsed surfaces' input regions in wlr.
	Arm    bool
	Disarm bool
}

// Tracker holds the browsing state for one dock. It is not safe for
// concurrent use: both backends drive it from their single event-loop
// goroutine, the same one that owns their display objects.
type Tracker struct {
	grace time.Duration
	pitch int

	rows   []Row
	x0, x1 int // column bounds, root coords, [x0, x1)

	armed bool
	hot   string // the currently expanded row, "" if none

	// leaving is the wlr-side disarm debounce: the pointer left a surface and we
	// are waiting to see whether it lands on another one (an ordinary row
	// crossing over the insensitive pad band) or really left the dock.
	leaving bool
	leaveAt time.Time
}

// New returns a Tracker. pitch is the vertical distance between adjacent rows
// (render.RowPitch); grace is the disarm debounce (DisarmGrace).
func New(grace time.Duration, pitch int) *Tracker {
	return &Tracker{grace: grace, pitch: pitch}
}

// SetColumn sets the column's horizontal bounds in root coordinates, as the
// half-open interval [x0, x1). Contains tests against it.
func (t *Tracker) SetColumn(x0, x1 int) { t.x0, t.x1 = x0, x1 }

// SetRows replaces the row layout. Called whenever the dock re-lays out, which
// is every snapshot. It does not disturb the armed/hot state: a snapshot
// arriving mid-browse must not collapse the panel the user is reading. Use
// Drop for a row that actually went away.
func (t *Tracker) SetRows(rows []Row) { t.rows = rows }

// Armed reports whether the full column is currently row-sensitive.
func (t *Tracker) Armed() bool { return t.armed }

// Hot returns the expanded row's session id, or "" if none.
func (t *Tracker) Hot() string { return t.hot }

// Contains reports whether a root-coordinate point is inside the dock column.
// Backends test leave events against it, because most crossings are not exits:
// the expanded tab is a sibling of the row's catch window, so opening a panel
// under the cursor produces a genuine LeaveNotify, and so does every move to an
// adjacent row. Only coordinates outside the column end a browse.
//
// This is the only thing rows and pitch are used for. Rows are contiguous at
// pitch, so the vertical span runs from the first row's top to one pitch past
// the last one's.
func (t *Tracker) Contains(x, y int) bool {
	if len(t.rows) == 0 {
		return false
	}
	if x < t.x0 || x >= t.x1 {
		return false
	}
	top := t.rows[0].Top
	return y >= top && y < top+len(t.rows)*t.pitch
}

// Hover reports that the cursor is on row id, from a source that knows the row
// directly: an x11 tab's EnterNotify or a wlr surface's wl_pointer.enter.
//
// The row that has the cursor is the row that is open, with no delay in either
// direction — arriving from outside, or swapping between rows mid-browse. See
// DisarmGrace for why there is deliberately no dwell here.
//
// The armed case also covers browsing *along* the edge strips: those are covered
// by the tab windows themselves, so the x11 catch window sees no motion there
// and this is the only path that can swap rows.
func (t *Tracker) Hover(id string, now time.Time) Action {
	t.leaving = false
	if id == "" || id == t.hot {
		return Action{}
	}
	old := t.hot
	t.hot = id
	if !t.armed {
		t.armed = true
		return Action{Expand: id, Arm: true}
	}
	// old is "" when the previous row was dropped from under the cursor.
	return Action{Collapse: old, Expand: id}
}

// LeaveSurface reports that the pointer left the surface owning id, without
// saying where it went — the wlr case, where the compositor reports pointer
// focus per surface and gives no coordinates outside one.
//
// It cannot disarm immediately: adjacent surfaces are separated by an
// insensitive shadow-pad band, so an ordinary downward browse produces a leave
// for one row and an enter for the next a millisecond later. Instead it starts
// a DisarmGrace countdown that Tick resolves and any Hover cancels.
func (t *Tracker) LeaveSurface(id string, now time.Time) Action {
	if !t.armed || t.leaving {
		return Action{}
	}
	t.leaving = true
	t.leaveAt = now
	return Action{}
}

// Exit collapses the hot row and disarms, unconditionally and immediately.
func (t *Tracker) Exit() Action {
	if !t.armed {
		return Action{}
	}
	old := t.hot
	t.armed = false
	t.hot = ""
	t.leaving = false
	return Action{Collapse: old, Disarm: true}
}

// Tick resolves the disarm debounce. Backends call it from their existing
// animation loop, because the countdown has to be able to fire once the pointer
// has gone and no further input events are coming.
//
// Nothing else is deferred: row swaps happen in the event handlers, so a
// backend whose leave events carry coordinates (x11 — see Motion and Exit)
// never depends on this for anything the user can see.
func (t *Tracker) Tick(now time.Time) Action {
	if !t.armed || !t.leaving {
		return Action{}
	}
	if now.Sub(t.leaveAt) < t.grace {
		return Action{}
	}
	return t.Exit()
}

// Drop reports that a session went away — it exited, or the user dismissed it,
// so its tab is being destroyed.
//
// Dropping the hot row stays armed on purpose: the cursor has not moved, and
// the user is still browsing, so the next Hover or Motion picks up a row
// normally.
func (t *Tracker) Drop(id string) Action {
	if id != t.hot {
		return Action{}
	}
	t.hot = ""
	return Action{Collapse: id}
}
