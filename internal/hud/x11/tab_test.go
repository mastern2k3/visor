package x11

import (
	"math"
	"testing"
	"time"

	"github.com/nitzanz/visor/internal/hud/render"
)

// These tests pin the "welded-edge" invariant: the capsule's drawn right edge
// must never fall short of the screen edge (rightX), in any state. This is
// pure integer geometry — no X connection needed — but nothing asserted it
// before, and it is exactly the arithmetic that regressed once already (see
// the wobble/MaxProtrusion fix in internal/hud/render/tab.go).

// weldedEdge is the invariant itself: for a tab resting at restX(), the
// capsule is drawn render.CapsuleDrawW wide starting at
// render.ShadowPad past the window's left edge (restX()), so its drawn
// right edge is restX() + render.ShadowPad + render.CapsuleDrawW. That must
// be >= rightX or the capsule visibly detaches from the screen edge, leaving
// a transparent gap between the tab and the edge of the monitor.
func weldedEdge(t *tab) int {
	return t.restX() + render.ShadowPad + render.CapsuleDrawW
}

func TestRestX_NormalTab_CapsuleDrawnEdgeReachesScreenEdge(t *testing.T) {
	tb := &tab{opt: tabOpts{rightX: 2560}, sess: sessionView{Attention: ""}}
	if got := weldedEdge(tb); got < tb.opt.rightX {
		t.Errorf("capsule detached from screen edge: restX()+ShadowPad+CapsuleDrawW = %d, want >= rightX = %d", got, tb.opt.rightX)
	}
}

func TestRestX_NeedsTab_CapsuleDrawnEdgeReachesScreenEdge(t *testing.T) {
	tb := &tab{opt: tabOpts{rightX: 2560}, sess: sessionView{Attention: "needs"}}
	if got := weldedEdge(tb); got < tb.opt.rightX {
		t.Errorf("capsule detached from screen edge: restX()+ShadowPad+CapsuleDrawW = %d, want >= rightX = %d", got, tb.opt.rightX)
	}
}

// TestRestX_NormalTab_ShowsExactlyCapsuleWVisiblePixels pins how much of the
// capsule is visible on screen at rest for a tab with no attention overlay: it
// must be exactly render.CapsuleW, no more (or the capsule would spill onto
// the desktop past its intended strip) and no less (or part of the capsule
// that should be visible would be clipped off-window).
func TestRestX_NormalTab_ShowsExactlyCapsuleWVisiblePixels(t *testing.T) {
	if got := collapsedVisibleW - render.ShadowPad; got != render.CapsuleW {
		t.Errorf("visible capsule width = %d, want CapsuleW = %d — the strip the user sees at rest is not CapsuleW wide", got, render.CapsuleW)
	}
}

// TestExpandedX_PanelRightEdgeAtScreenEdge pins the expanded position by
// calling the real expandedX helper that setExpanded uses (setExpanded
// itself also drives t.win.Move and reshape(), which need a live X
// connection, so it isn't called directly here) — the window's right edge,
// since the buffer is bufW wide, must land exactly on rightX: flush with the
// screen edge, not short of it and not past it.
func TestExpandedX_PanelRightEdgeAtScreenEdge(t *testing.T) {
	rightX := 2560
	newX := expandedX(rightX)
	if got := newX + bufW; got != rightX {
		t.Errorf("expanded panel right edge = %d, want exactly rightX = %d", got, rightX)
	}
}

// peakTab returns a *tab plus the "now" time at which its wobble term is at
// its peak (t01 == 1) — deterministically, with no time.Sleep. wobblePhase
// is fixed at 0, so the peak (cos argument == pi) falls at
// elapsed == wobblePeriod/2 seconds after wobbleStart. Mirrors peakSurface
// in internal/hud/wlr/surface_test.go.
func peakTab(rightX int, attention, activity string) (tb *tab, peakNow time.Time) {
	start := time.Unix(0, 0)
	now := start.Add(time.Duration(wobblePeriod / 2 * float64(time.Second)))
	tb = &tab{
		opt:         tabOpts{rightX: rightX},
		sess:        sessionView{Attention: attention, Activity: activity},
		wobbleStart: start,
		wobblePhase: 0,
	}
	return tb, now
}

// TestTickX_NotWorking_MatchesRestX pins that a non-working tab's animated
// position is just its rest position — tickX must not apply any wobble
// offset when Activity != "working".
func TestTickX_NotWorking_MatchesRestX(t *testing.T) {
	tb := &tab{opt: tabOpts{rightX: 2560}, sess: sessionView{Activity: "waiting"}}
	if got, want := tb.tickX(time.Now()), tb.restX(); got != want {
		t.Errorf("tickX() = %d, want restX() = %d for a non-working tab", got, want)
	}
}

// TestTickX_WorkingAtWobblePeak_CapsuleReachesScreenEdge exercises tick()'s
// pure wobble-offset computation (tickX) directly, mirroring
// computeRightMargin's peak-phase test on the wlr side. This code path had
// no test coverage at all before — the wlr backend's equivalent
// (computeRightMargin) did, x11's tick() did not, despite both applying the
// identical math.Round-based wobble.
func TestTickX_WorkingAtWobblePeak_CapsuleReachesScreenEdge(t *testing.T) {
	tb, now := peakTab(2560, "", "working")

	// Sanity-check we actually hit the peak: at t01=1 the offset should equal
	// -round(wobbleAmp), matching tickX's own math.
	wantOffset := -int(math.Round(wobbleAmp))
	wantX := tb.restX() + wantOffset
	gotX := tb.tickX(now)
	if gotX != wantX {
		t.Fatalf("did not land on the wobble peak: tickX() = %d, want %d (deterministic phase setup is broken, not just the invariant)", gotX, wantX)
	}

	if got := gotX + render.ShadowPad + render.CapsuleDrawW; got < tb.opt.rightX {
		t.Errorf("capsule detached from screen edge: tickX()+ShadowPad+CapsuleDrawW = %d, want >= rightX = %d", got, tb.opt.rightX)
	}
}

// TestTickX_NeedsAndWorkingAtWobblePeak_MarginExactlyZero covers the tightest
// point in the whole system: a tab that is both attention=needs (so restX
// already sits AlertProtrusion further out) AND working at its wobble peak
// (so the wobble adds its full round(WobbleAmp) further still). By
// construction (MaxProtrusion == AlertProtrusion + round(WobbleAmp) at
// today's values) the capsule's drawn right edge lands exactly on the
// screen edge here — zero slack in either direction. If MaxProtrusion ever
// under-provisions relative to the real animation peak, this is the combined
// state where the capsule detaches first.
func TestTickX_NeedsAndWorkingAtWobblePeak_MarginExactlyZero(t *testing.T) {
	tb, now := peakTab(2560, "needs", "working")

	got := tb.tickX(now) + render.ShadowPad + render.CapsuleDrawW
	if got != tb.opt.rightX {
		t.Errorf("needs+working-at-peak capsule edge = %d, want exactly rightX = %d (this is the system's tightest margin — any deviation means either a gap or an overshoot)", got, tb.opt.rightX)
	}
}

// The stateElapsed/elapsedChanged/haloPhaseStep decisions used to be
// duplicated verbatim here and in internal/hud/wlr/surface_test.go — Task 8
// review flagged the duplication and asked for the logic (and its test
// table) to be hoisted into internal/hud/render, since none of the three
// touch anything x11-specific. See render.Elapsed / render.ElapsedChanged /
// render.HaloPhaseStep and their tests in
// internal/hud/render/format_test.go.
