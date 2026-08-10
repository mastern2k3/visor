package wlr

import (
	"math"
	"testing"
	"time"

	"github.com/nitzanz/visor/internal/hud/render"
)

// These tests pin the "welded-edge" invariant for the wlr backend: the
// capsule's drawn right edge must never fall short of the screen edge, in
// any state. layerSurface.computeRightMargin is pure integer/float geometry —
// no Wayland compositor needed — but nothing asserted it before, and this is
// exactly the arithmetic that regressed once already (see the wobble/
// MaxProtrusion fix in internal/hud/render/tab.go).
//
// A margin of 0 puts the surface's right edge exactly at the screen edge; a
// negative margin overflows the surface (and its drawn capsule) past it. So
// the invariant "capsule reaches the edge" is: CapsuleDrawW + margin >=
// CapsuleW, i.e. the margin never claws back more than tabOverflow
// (render.MaxProtrusion) px.

func TestTabOverflow_EqualsRenderMaxProtrusion(t *testing.T) {
	if tabOverflow != render.MaxProtrusion {
		t.Errorf("tabOverflow = %d, want render.MaxProtrusion = %d — the wlr surface overhang and the render-drawn capsule overhang have diverged, which will detach the capsule from the screen edge", tabOverflow, render.MaxProtrusion)
	}
}

// peakSurface returns a layerSurface plus the "now" time at which its
// wobble term is at its peak (t01 == 1) — deterministically, with no
// time.Sleep. wobblePhase is fixed at 0, so the peak (cos argument == pi)
// falls at elapsed == wobblePeriod/2 seconds after wobbleStart.
func peakSurface(activity, attention string) (s *layerSurface, peakNow time.Time) {
	start := time.Unix(0, 0)
	now := start.Add(time.Duration(render.WobblePeriod / 2 * float64(time.Second)))
	return &layerSurface{
		activity:    activity,
		attention:   attention,
		wobbleStart: start,
		wobblePhase: 0,
		slot:        0,
	}, now
}

func TestComputeRightMargin_Rest_CapsuleReachesScreenEdge(t *testing.T) {
	s := &layerSurface{activity: "waiting", attention: "ack", wobbleStart: time.Now()}
	margin := s.computeRightMargin(time.Now())
	assertWeldedAndBounded(t, "rest", margin)
}

func TestComputeRightMargin_Needs_CapsuleReachesScreenEdge(t *testing.T) {
	s := &layerSurface{activity: "waiting", attention: "needs", wobbleStart: time.Now()}
	margin := s.computeRightMargin(time.Now())
	assertWeldedAndBounded(t, "needs", margin)
}

func TestComputeRightMargin_WorkingAtWobblePeak_CapsuleReachesScreenEdge(t *testing.T) {
	s, now := peakSurface("working", "ack")
	margin := s.computeRightMargin(now)

	// Sanity-check we actually hit the peak: at t01=1 the working delta should
	// equal round(wobbleAmp), matching computeRightMargin's own math.
	wantDelta := int32(math.Round(wobbleAmp))
	wantMargin := -int32(tabOverflow) + wantDelta
	if margin != wantMargin {
		t.Fatalf("did not land on the wobble peak: margin = %d, want %d (deterministic phase setup is broken, not just the invariant)", margin, wantMargin)
	}
	assertWeldedAndBounded(t, "working at wobble peak", margin)
}

// --- stateElapsed ------------------------------------------------------

// TestStateElapsed_ZeroTimestamp_ClampsToZero pins the defence against a zero
// StateSince: time.Since(time.Time{}) is about 2562047h positive (not
// negative, so ElapsedString's own negative-clamp does not save us), and an
// older daemon, a replayed snapshot, or a future regression could produce
// one. Without this guard the panel would show "2562047h 47m" instead of
// "0s".
func TestStateElapsed_ZeroTimestamp_ClampsToZero(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if got := stateElapsed(now, time.Time{}); got != 0 {
		t.Errorf("stateElapsed(now, zero) = %v, want 0", got)
	}
}

// TestStateElapsed_TruncatesToWholeSeconds pins the truncation that keeps the
// per-snapshot equality-based skip check meaningful: two calls within the
// same second must produce an identical duration, or every snapshot
// broadcast (even one carrying no observable change for this surface) would
// defeat the skip and force a repaint.
func TestStateElapsed_TruncatesToWholeSeconds(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(65*time.Second + 700*time.Millisecond)
	got := stateElapsed(now, since)
	want := 65 * time.Second
	if got != want {
		t.Errorf("stateElapsed = %v, want %v (sub-second remainder must be truncated away)", got, want)
	}
}

// --- elapsedChanged -----------------------------------------------------

// TestElapsedChanged_SameSecond_NoChange pins the "no repaint" branch: within
// the same rendered second the string is unchanged, so tickElapsed must not
// repaint.
func TestElapsedChanged_SameSecond_NoChange(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(65 * time.Second)
	changed, str := elapsedChanged(now, since, "1m 05s")
	if changed {
		t.Errorf("elapsedChanged = true, want false (rendered string %q is unchanged)", str)
	}
	if str != "1m 05s" {
		t.Errorf("str = %q, want %q", str, "1m 05s")
	}
}

// TestElapsedChanged_SecondTicksOver_Changes pins the "repaint" branch: once
// the second rolls over, the rendered string differs from the last-painted
// one, so tickElapsed must repaint.
func TestElapsedChanged_SecondTicksOver_Changes(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	now := since.Add(66 * time.Second)
	changed, str := elapsedChanged(now, since, "1m 05s")
	if !changed {
		t.Errorf("elapsedChanged = false, want true (second rolled over)")
	}
	if str != "1m 06s" {
		t.Errorf("str = %q, want %q", str, "1m 06s")
	}
}

// TestElapsedChanged_ZeroTimestamp pins that a zero StateSince, once routed
// through stateElapsed's clamp, renders "0s" rather than a multi-thousand-hour
// string, and that elapsedChanged reports a change when the last-painted
// string was something else.
func TestElapsedChanged_ZeroTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	changed, str := elapsedChanged(now, time.Time{}, "4m 12s")
	if str != "0s" {
		t.Errorf("str = %q, want %q", str, "0s")
	}
	if !changed {
		t.Errorf("elapsedChanged = false, want true")
	}
}

// --- haloPhaseStep -------------------------------------------------------

// TestHaloPhaseStep_FullCycle walks a full render.HaloPeriod (1.6s) in
// render.HaloSteps (8) equal increments and pins the exact step/phase pairs
// by hand — not by calling the formula under test a second time — so the
// test can actually catch a regression in the quantisation math rather than
// restating it.
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
		step, phase := haloPhaseStep(start.Add(c.elapsed), start)
		if step != c.wantStep {
			t.Errorf("haloPhaseStep(+%v): step = %d, want %d", c.elapsed, step, c.wantStep)
		}
		if math.Abs(phase-c.wantPh) > 1e-9 {
			t.Errorf("haloPhaseStep(+%v): phase = %v, want %v", c.elapsed, phase, c.wantPh)
		}
	}
}

// assertWeldedAndBounded checks both directions of the welded-edge invariant:
// the capsule must reach the screen edge (not fall short) and must never be
// drawn wider than CapsuleDrawW (not overshoot past what render.DrawTab
// actually painted).
func assertWeldedAndBounded(t *testing.T, label string, margin int32) {
	t.Helper()
	visible := int32(render.CapsuleDrawW) + margin
	if visible < int32(render.CapsuleW) {
		t.Errorf("%s: capsule detached from screen edge: CapsuleDrawW+margin = %d, want >= CapsuleW = %d (margin=%d)", label, visible, render.CapsuleW, margin)
	}
	if visible > int32(render.CapsuleDrawW) {
		t.Errorf("%s: visible width %d exceeds CapsuleDrawW %d — margin should never be positive", label, visible, render.CapsuleDrawW)
	}
}
