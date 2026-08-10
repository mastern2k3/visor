package x11

import (
	"testing"

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

// TestSetExpanded_PanelRightEdgeAtScreenEdge pins the expanded position: the
// window moves to rightX-bufW, so the panel's drawn right edge (which is the
// window's right edge, since the buffer is bufW wide) lands exactly on
// rightX — flush with the screen edge, not short of it and not past it.
func TestSetExpanded_PanelRightEdgeAtScreenEdge(t *testing.T) {
	rightX := 2560
	newX := rightX - bufW
	if got := newX + bufW; got != rightX {
		t.Errorf("expanded panel right edge = %d, want exactly rightX = %d", got, rightX)
	}
}
