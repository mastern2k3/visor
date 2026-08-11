package x11

import (
	"testing"

	"github.com/nitzanz/visor/internal/hud/render"
)

// catchRowRect is pure integer geometry, like restX/tickX/expandedX — the rest
// of catch.go needs a live X connection and is not covered here (there is no
// headless X server in CI, and there is no CI).
//
// The invariant that matters: each row's catch window must cover exactly the
// ground that row's panel can occupy. Too narrow and the cursor falls off the
// inland end of an expanded panel into nothing; not a full pitch tall and the
// gap between rows becomes a hole that disarms browsing mid-move.

func TestCatchRowRect_CoversTheFullPanelSlide(t *testing.T) {
	mon := monitor{x: 0, y: 0, w: 2560, h: 1440}
	const rowTop = 194

	x, y, w, h := catchRowRect(mon, rowTop)

	rightX := mon.x + mon.w
	// An expanded tab's window sits at expandedX(rightX); its leftmost pixel is
	// the furthest inland the cursor ever needs to be caught.
	if want := expandedX(rightX); x != want {
		t.Errorf("left edge = %d, want %d (expandedX): the catch window must reach as far inland as an expanded panel", x, want)
	}
	if x+w != rightX {
		t.Errorf("right edge = %d, want rightX = %d: the column must stay welded to the screen edge", x+w, rightX)
	}
	if w != render.BufW {
		t.Errorf("width = %d, want BufW = %d", w, render.BufW)
	}
	if y != rowTop {
		t.Errorf("top = %d, want %d", y, rowTop)
	}
	if h != render.RowPitch {
		t.Errorf("height = %d, want RowPitch = %d", h, render.RowPitch)
	}
}

// Adjacent rows' catch windows must abut exactly: no overlap (two windows
// fighting for the same pixel) and no gap (a dead band that would disarm the
// browse as the cursor passes through it).
func TestCatchRowRect_AdjacentRowsAbutExactly(t *testing.T) {
	mon := monitor{x: 0, y: 0, w: 1920, h: 1080}
	const firstTop = 140

	_, y1, _, h1 := catchRowRect(mon, firstTop)
	_, y2, _, _ := catchRowRect(mon, firstTop+render.RowPitch)

	if y1+h1 != y2 {
		t.Errorf("row 1 ends at %d but row 2 starts at %d: rows must be contiguous", y1+h1, y2)
	}
}

// A multi-monitor origin must not be dropped: the column belongs to the primary
// monitor's right edge, not the root window's.
func TestCatchRowRect_HonoursMonitorOrigin(t *testing.T) {
	mon := monitor{x: 1920, y: 300, w: 2560, h: 1440}
	x, y, w, _ := catchRowRect(mon, mon.y+140)

	if got := x + w; got != mon.x+mon.w {
		t.Errorf("right edge = %d, want %d", got, mon.x+mon.w)
	}
	if y != 440 {
		t.Errorf("top = %d, want 440", y)
	}
}
