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
