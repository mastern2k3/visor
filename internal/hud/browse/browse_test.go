package browse

import (
	"testing"
	"time"
)

// Layout mirrors the real dock: RowPitch-tall contiguous rows starting at the
// help tab's top margin, in a BufW-wide column welded to the screen edge.
const (
	testPitch = 54  // render.RowPitch
	testTop   = 140 // dockTopMargin
	testX0    = 1920 - 335
	testX1    = 1920
)

func newTestTracker() *Tracker {
	t := New(DisarmGrace, testPitch)
	t.SetColumn(testX0, testX1)
	t.SetRows([]Row{
		{ID: "help", Top: testTop},
		{ID: "a", Top: testTop + testPitch},
		{ID: "b", Top: testTop + 2*testPitch},
		{ID: "c", Top: testTop + 3*testPitch},
	})
	return t
}

// rowMid is a y coordinate in the middle of row i.
func rowMid(i int) int { return testTop + i*testPitch + testPitch/2 }

var t0 = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

func TestHover_DisarmedExpandsImmediatelyAndArms(t *testing.T) {
	tr := newTestTracker()
	got := tr.Hover("a", t0)
	if got.Expand != "a" || !got.Arm {
		t.Fatalf("first hover: got %+v, want Expand=a Arm=true", got)
	}
	if got.Collapse != "" {
		t.Errorf("first hover collapsed %q; nothing was expanded yet", got.Collapse)
	}
	if !tr.Armed() || tr.Hot() != "a" {
		t.Errorf("armed=%v hot=%q, want true/a", tr.Armed(), tr.Hot())
	}
}

// The whole point of the feature: with the cursor deep inland where no tab
// window is sensitive, moving to another row swaps which panel is open —
// immediately. Both backends report this as a Hover: x11 from the row's own
// InputOnly catch window, wlr from the surface's wl_pointer.enter. There is no
// dwell to wait out (see the DisarmGrace doc comment for why).
func TestHover_ArmedRowSwapIsImmediate(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)

	got := tr.Hover("b", t0)
	if got.Collapse != "a" || got.Expand != "b" {
		t.Fatalf("armed hover: got %+v, want Collapse=a Expand=b", got)
	}
	if tr.Hot() != "b" || !tr.Armed() {
		t.Errorf("hot=%q armed=%v, want b/true", tr.Hot(), tr.Armed())
	}
}

// Re-reporting the row that is already open must emit nothing. This is not
// hypothetical: expanding a tab slides its window under the cursor, which makes
// the compositor/X send a fresh Enter for that same row. Acting on it would
// re-Move a window that is already in place.
func TestHover_RepeatOfHotRowIsANoOp(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)

	for i := range 3 {
		if got := tr.Hover("a", t0.Add(time.Duration(i)*time.Millisecond)); got != (Action{}) {
			t.Errorf("repeat %d: got %+v, want nothing", i, got)
		}
	}
}

// Sweeping out across rows opens each one on the way. That is the accepted cost
// of having no dwell: the flicker matches what the edge strips have always done,
// and it buys instant response on every deliberate row change. The invariant
// that matters is that the sweep ends collapsed and disarmed, with nothing left
// to fire afterwards.
func TestExit_AfterCrossingSeveralRows(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)

	if got := tr.Hover("b", t0.Add(20*time.Millisecond)); got.Expand != "b" {
		t.Errorf("crossing row b: got %+v, want Expand=b", got)
	}
	if got := tr.Hover("c", t0.Add(40*time.Millisecond)); got.Expand != "c" {
		t.Errorf("crossing row c: got %+v, want Expand=c", got)
	}

	exit := tr.Exit()
	if exit.Collapse != "c" || !exit.Disarm {
		t.Fatalf("exiting the column: got %+v, want Collapse=c Disarm=true", exit)
	}
	if tr.Armed() || tr.Hot() != "" {
		t.Errorf("armed=%v hot=%q after exit, want false/empty", tr.Armed(), tr.Hot())
	}
	if got := tr.Tick(t0.Add(time.Second)); got != (Action{}) {
		t.Errorf("tick after exit: got %+v, want nothing", got)
	}
	if got := tr.Exit(); got != (Action{}) {
		t.Errorf("second exit: got %+v, want nothing", got)
	}
}

// wlr's only disarm path. Crossing the insensitive shadow-pad band between two
// surfaces produces leave-then-enter milliseconds apart, which must read as a
// row swap, not as leaving the dock.
func TestLeaveSurface_PadBandCrossingDoesNotDisarm(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)

	tr.LeaveSurface("a", t0)
	tr.Hover("b", t0.Add(2*time.Millisecond))

	if got := tr.Tick(t0.Add(DisarmGrace)); got.Disarm {
		t.Fatalf("got %+v, want no disarm: the pointer landed on the next row", got)
	}
	if !tr.Armed() || tr.Hot() != "b" {
		t.Errorf("armed=%v hot=%q, want true/b", tr.Armed(), tr.Hot())
	}
}

func TestLeaveSurface_NoReentryDisarmsAfterGrace(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)
	tr.LeaveSurface("a", t0)

	if got := tr.Tick(t0.Add(DisarmGrace - time.Millisecond)); got != (Action{}) {
		t.Fatalf("tick before the grace period elapsed: got %+v, want nothing", got)
	}
	got := tr.Tick(t0.Add(DisarmGrace))
	if got.Collapse != "a" || !got.Disarm {
		t.Fatalf("got %+v, want Collapse=a Disarm=true", got)
	}
}

// A session exiting or being dismissed mid-browse collapses its panel but must
// leave the dock armed: the cursor has not moved and the user is still
// browsing.
func TestDrop_HotRowStaysArmedAndNextHoverIsImmediate(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)

	got := tr.Drop("a")
	if got.Collapse != "a" {
		t.Fatalf("drop hot row: got %+v, want Collapse=a", got)
	}
	if !tr.Armed() {
		t.Fatal("armed=false after dropping the hot row, want true")
	}
	// Nothing left to collapse — the dropped tab is already gone.
	next := tr.Hover("b", t0.Add(time.Millisecond))
	if next.Expand != "b" {
		t.Errorf("hover after a drop: got %+v, want Expand=b", next)
	}
	if next.Collapse != "" {
		t.Errorf("hover after a drop collapsed %q, want nothing: that tab is destroyed", next.Collapse)
	}
}

func TestDrop_NonHotRowDoesNothing(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)
	if got := tr.Drop("c"); got != (Action{}) {
		t.Errorf("got %+v, want nothing", got)
	}
	if tr.Hot() != "a" {
		t.Errorf("hot=%q, want a", tr.Hot())
	}
}

// Dropping a row the cursor reached mid-browse, rather than the one it armed
// on: same contract, and nothing may fire on a later tick for a tab that has
// been destroyed.
func TestDrop_RowReachedMidBrowse(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("a", t0)
	tr.Hover("b", t0)

	if got := tr.Drop("b"); got.Collapse != "b" {
		t.Fatalf("got %+v, want Collapse=b", got)
	}
	if got := tr.Tick(t0.Add(time.Second)); got != (Action{}) {
		t.Errorf("tick after the drop: got %+v, want nothing", got)
	}
	if !tr.Armed() {
		t.Error("armed=false, want true: the cursor is still in the column")
	}
}

func TestContains(t *testing.T) {
	tr := newTestTracker()
	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"inland middle row", testX0 + 10, rowMid(1), true},
		{"edge strip", testX1 - 1, rowMid(1), true},
		{"left of column", testX0 - 1, rowMid(1), false},
		{"at right bound", testX1, rowMid(1), false},
		{"above first row", testX0 + 10, testTop - 1, false},
		{"first row top edge", testX0 + 10, testTop, true},
		{"last row bottom edge", testX0 + 10, testTop + 4*testPitch, false},
		{"last row last pixel", testX0 + 10, testTop + 4*testPitch - 1, true},
	}
	for _, c := range cases {
		if got := tr.Contains(c.x, c.y); got != c.want {
			t.Errorf("%s: Contains(%d,%d)=%v, want %v", c.name, c.x, c.y, got, c.want)
		}
	}
}

// The column has no vertical holes: every pixel from the first row's top to the
// last row's bottom counts as inside, including the shadow-pad bands between
// capsules. A hole there would disarm the dock mid-browse.
func TestContains_NoVerticalHolesBetweenRows(t *testing.T) {
	tr := newTestTracker()
	for y := testTop; y < testTop+4*testPitch; y++ {
		if !tr.Contains(testX0+10, y) {
			t.Fatalf("y=%d is outside the column, want inside", y)
		}
	}
}

// A snapshot arriving mid-browse must not collapse the panel being read.
func TestSetRows_PreservesArmedState(t *testing.T) {
	tr := newTestTracker()
	tr.Hover("b", t0)
	tr.SetRows([]Row{
		{ID: "help", Top: testTop},
		{ID: "b", Top: testTop + testPitch},
	})
	if !tr.Armed() || tr.Hot() != "b" {
		t.Errorf("armed=%v hot=%q after re-layout, want true/b", tr.Armed(), tr.Hot())
	}
}

// Leave reports that arrive while nothing is open must not do anything — in
// particular they must not emit a Disarm the backend would act on by unmapping
// windows that are already unmapped.
func TestDisarmed_LeaveAndTickAreInert(t *testing.T) {
	tr := newTestTracker()
	if got := tr.LeaveSurface("a", t0); got != (Action{}) {
		t.Errorf("LeaveSurface while disarmed: got %+v, want nothing", got)
	}
	if got := tr.Tick(t0.Add(time.Second)); got != (Action{}) {
		t.Errorf("Tick while disarmed: got %+v, want nothing", got)
	}
	if got := tr.Exit(); got != (Action{}) {
		t.Errorf("Exit while disarmed: got %+v, want nothing", got)
	}
	if tr.Armed() {
		t.Error("armed=true, want false")
	}
}
