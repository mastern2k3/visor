package render

import (
	"testing"
	"time"
)

func TestStateWords(t *testing.T) {
	cases := []struct {
		activity, attention, waiting, want string
	}{
		{"waiting", "needs", "permission", "blocked on approval"},
		{"waiting", "needs", "user", "waiting for you"},
		{"waiting", "dismissed", "", "dismissed"},
		{"working", "ack", "", "working"},
		{"waiting", "ack", "", "idle"},
		{"unknown", "ack", "", "idle"},
		// permission outranks everything, even while working.
		{"working", "needs", "permission", "blocked on approval"},
		// Divergence case: even with waiting=="permission", if attention is
		// dismissed, the words must match the colour (which For() returns as
		// Dismissed for this input).
		{"waiting", "dismissed", "permission", "dismissed"},
	}
	for _, c := range cases {
		if got := StateWords(c.activity, c.attention, c.waiting); got != c.want {
			t.Errorf("StateWords(%q,%q,%q) = %q, want %q",
				c.activity, c.attention, c.waiting, got, c.want)
		}
	}
}

func TestElapsedString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{4*time.Minute + 12*time.Second, "4m 12s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"},
		{time.Hour + 4*time.Minute, "1h 04m"},
		{26*time.Hour + 3*time.Minute, "26h 03m"},
		// Negative durations (clock skew between daemon and HUD) must not
		// render as garbage like "-1m -3s".
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := ElapsedString(c.d); got != c.want {
			t.Errorf("ElapsedString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// --- Elapsed --------------------------------------------------------------
//
// These three test groups were previously duplicated verbatim in
// internal/hud/x11/tab_test.go and internal/hud/wlr/surface_test.go. Task 8
// review flagged the duplication (the decisions never touched a
// backend-specific type) and asked for one copy, here, backing the hoisted
// Elapsed/ElapsedChanged/HaloPhaseStep.

// TestElapsed_ZeroTimestamp_ClampsToZero pins the defence against a zero
// StateSince: time.Since(time.Time{}) is about 2562047h positive (not
// negative, so ElapsedString's own negative-clamp does not save us), and an
// older daemon, a replayed snapshot, or a future regression could produce
// one. Without this guard the panel would show "2562047h 47m" instead of
// "0s".
func TestElapsed_ZeroTimestamp_ClampsToZero(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if got := Elapsed(time.Time{}, now); got != 0 {
		t.Errorf("Elapsed(zero, now) = %v, want 0", got)
	}
}

// TestElapsed_TruncatesToWholeSeconds pins the truncation that keeps a
// backend's equality-based skip-check meaningful: two calls within the same
// second must produce an identical duration, or every snapshot broadcast
// (even one carrying no observable change for a given tab) would defeat the
// skip and force a redraw.
func TestElapsed_TruncatesToWholeSeconds(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(65*time.Second + 700*time.Millisecond)
	got := Elapsed(since, now)
	want := 65 * time.Second
	if got != want {
		t.Errorf("Elapsed = %v, want %v (sub-second remainder must be truncated away)", got, want)
	}
}

// --- ElapsedChanged --------------------------------------------------------

// TestElapsedChanged_SameSecond_NoChange pins the "no redraw" branch: within
// the same rendered second the string is unchanged, so a tick handler must
// not re-render.
func TestElapsedChanged_SameSecond_NoChange(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(65 * time.Second)
	changed, str := ElapsedChanged(since, now, "1m 05s")
	if changed {
		t.Errorf("ElapsedChanged = true, want false (rendered string %q is unchanged)", str)
	}
	if str != "1m 05s" {
		t.Errorf("str = %q, want %q", str, "1m 05s")
	}
}

// TestElapsedChanged_SecondTicksOver_Changes pins the "redraw" branch: once
// the second rolls over, the rendered string differs from the last-painted
// one, so a tick handler must re-render.
func TestElapsedChanged_SecondTicksOver_Changes(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(66 * time.Second)
	changed, str := ElapsedChanged(since, now, "1m 05s")
	if !changed {
		t.Errorf("ElapsedChanged = false, want true (second rolled over)")
	}
	if str != "1m 06s" {
		t.Errorf("str = %q, want %q", str, "1m 06s")
	}
}

// TestElapsedChanged_ZeroTimestamp pins that a zero StateSince, once routed
// through Elapsed's clamp, renders "0s" rather than a multi-thousand-hour
// string, and that ElapsedChanged reports a change when the last-painted
// string was something else.
func TestElapsedChanged_ZeroTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	changed, str := ElapsedChanged(time.Time{}, now, "4m 12s")
	if str != "0s" {
		t.Errorf("str = %q, want %q", str, "0s")
	}
	if !changed {
		t.Errorf("ElapsedChanged = false, want true")
	}
}

// --- HaloPhaseStep ---------------------------------------------------------

// TestHaloPhaseStep_FullCycle walks a full HaloPeriod (1.6s) in HaloSteps (8)
// equal increments and pins the exact step/phase pairs by hand — not by
// calling the formula under test a second time — so the test can actually
// catch a regression in the quantisation math rather than restating it. This
// is exactly the table that caught the original float-precision bug (a naive
// elapsed.Seconds()/HaloPeriod computation landed a hair under exact step
// boundaries at +600ms, +1.2s and +1.4s).
func TestHaloPhaseStep_FullCycle(t *testing.T) {
	start := time.Unix(0, 0)
	cases := []struct {
		elapsed  time.Duration
		wantStep int
		wantPh   float64
	}{
		{0, 0, 0.000},
		{200 * time.Millisecond, 1, 0.125},
		{400 * time.Millisecond, 2, 0.250},
		{600 * time.Millisecond, 3, 0.375},
		{800 * time.Millisecond, 4, 0.500},
		{1000 * time.Millisecond, 5, 0.625},
		{1200 * time.Millisecond, 6, 0.750},
		{1400 * time.Millisecond, 7, 0.875},
		// One full period later, the cycle repeats from step 0.
		{1600 * time.Millisecond, 0, 0.000},
		{1800 * time.Millisecond, 1, 0.125},
	}
	for _, c := range cases {
		step, phase := HaloPhaseStep(start, start.Add(c.elapsed))
		if step != c.wantStep {
			t.Errorf("HaloPhaseStep(+%v): step = %d, want %d", c.elapsed, step, c.wantStep)
		}
		if diff := phase - c.wantPh; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("HaloPhaseStep(+%v): phase = %v, want %v", c.elapsed, phase, c.wantPh)
		}
	}
}
