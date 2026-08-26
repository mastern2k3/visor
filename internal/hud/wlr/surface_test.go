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
// exactly the arithmetic that regressed once already (see the breathe/
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

// peakSurface returns a *layerSurface plus the "now" at which its breathe term
// is at its peak (t01 == 1) — deterministically, with no time.Sleep.
// motionPhase is fixed at 0, so the (1-cos)/2 peak falls at
// WorkBreathePeriod/2 after motionStart. Mirrors peakTab in
// internal/hud/x11/tab_test.go.
func peakSurface(activity, attention string, bgRunning int) (s *layerSurface, peakNow time.Time) {
	start := time.Unix(0, 0)
	now := start.Add(time.Duration(render.WorkBreathePeriod / 2 * float64(time.Second)))
	return &layerSurface{
		activity:    activity,
		attention:   attention,
		bgRunning:   bgRunning,
		motionStart: start,
		motionPhase: 0,
		slot:        0,
	}, now
}

// An idle surface with no background work stays at rest.
func TestComputeRightMargin_IdleAndNoBackgroundWork_RestOnly(t *testing.T) {
	for _, activity := range []string{"waiting", "unknown"} {
		s := &layerSurface{activity: activity, motionStart: time.Unix(0, 0)}
		if got, want := s.computeRightMargin(time.Now()), -int32(tabOverflow); got != want {
			t.Errorf("activity=%q with no background work: margin = %d, want %d — only background work may move a surface", activity, got, want)
		}
	}
}

// Background work moves a non-working surface; a working one wobbles instead
// (the wobble overrides), so the two never compound.
func TestComputeRightMargin_BackgroundWorkWhileWaiting_Moves(t *testing.T) {
	s, now := peakSurface("waiting", "ack", 1)
	if got, want := s.computeRightMargin(now), -int32(tabOverflow)+int32(math.Round(render.WorkBreatheAmp)); got != want {
		t.Errorf("waiting surface with background work: margin = %d, want %d", got, want)
	}
}

func TestComputeRightMargin_WorkingOverridesBackgroundBreathe(t *testing.T) {
	both, now := peakSurface("working", "ack", 3)
	plain, _ := peakSurface("working", "ack", 0)
	if got, want := both.computeRightMargin(now), plain.computeRightMargin(now); got != want {
		t.Errorf("working+background margin = %d, want %d (same as working alone)", got, want)
	}
}

func TestComputeRightMargin_BackgroundWorkAtBreathePeak_CapsuleReachesScreenEdge(t *testing.T) {
	s, now := peakSurface("waiting", "ack", 1)
	margin := s.computeRightMargin(now)

	wantMargin := -int32(tabOverflow) + int32(math.Round(render.WorkBreatheAmp))
	if margin != wantMargin {
		t.Fatalf("did not land on the combined peak: margin = %d, want %d (deterministic phase setup is broken, not just the invariant)", margin, wantMargin)
	}
	assertWeldedAndBounded(t, "background work at breathe peak", margin)
}

// The tightest point in the system: attention=needs plus the larger of the two
// motions, the breathe, at its peak. By construction (tabOverflow ==
// render.MaxProtrusion == AlertProtrusion + round(max(WobbleAmp,
// WorkBreatheAmp))) the margin lands at exactly 0.
func TestComputeRightMargin_NeedsAndBackgroundAtPeak_MarginExactlyZero(t *testing.T) {
	s, now := peakSurface("waiting", "needs", 2)
	margin := s.computeRightMargin(now)

	if margin != 0 {
		t.Errorf("margin = %d, want exactly 0 — this is the system's tightest margin; any deviation means either a gap or an overshoot", margin)
	}
	assertWeldedAndBounded(t, "needs+background at breathe peak", margin)
}

// The stateElapsed/elapsedChanged/haloPhaseStep decisions used to be tested
// here, duplicated verbatim from internal/hud/x11/tab_test.go. Task 8 review
// flagged the duplication and asked for one copy of both the logic and its
// test table in internal/hud/render — see
// internal/hud/render/format_test.go.

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
