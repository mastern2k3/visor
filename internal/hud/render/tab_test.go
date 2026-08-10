package render

import (
	"testing"
	"time"
)

func silent() Palette { return Theme("silent") }

// withShadow returns s with Shadow set, so a test can render the same state
// twice and diff the two buffers.
func withShadow(s TabState, on bool) TabState {
	s.Shadow = on
	return s
}

func TestDrawTab_BufferSize(t *testing.T) {
	img := DrawTab(TabState{Expanded: true}, nil, silent())
	if img.RGBA.Bounds().Dx() != BufW || img.RGBA.Bounds().Dy() != BufH {
		t.Fatalf("size = %v, want %dx%d", img.RGBA.Bounds(), BufW, BufH)
	}
}

// The shadow pad must be transparent in every configuration — it is what lets
// the desktop show through and what the XShape input region excludes.
func TestDrawTab_ShadowPadTransparentWhenShadowOff(t *testing.T) {
	img := DrawTab(TabState{Expanded: true, Shadow: false}, nil, silent())
	if got := img.RGBA.RGBAAt(0, 0); got.A != 0 {
		t.Errorf("top-left pad alpha = %#x, want 0", got.A)
	}
	if got := img.RGBA.RGBAAt(1, BufH-1); got.A != 0 {
		t.Errorf("bottom pad alpha = %#x, want 0", got.A)
	}
}

func TestDrawTab_CollapsedPanelIsTransparent(t *testing.T) {
	img := DrawTab(TabState{Expanded: false}, nil, silent())
	// Capsule interior is opaque.
	if got := img.RGBA.RGBAAt(ShadowPad+CapsuleW/2, BufH/2); got.A != 0xff {
		t.Errorf("capsule alpha = %#x, want 0xff", got.A)
	}
	// Panel region is fully transparent.
	if got := img.RGBA.RGBAAt(200, BufH/2); got.A != 0 {
		t.Errorf("panel alpha = %#x, want 0", got.A)
	}
}

// The capsule is drawn Radius wider than it is so its right corners fall
// outside the shape and stay square. That overhang must be clipped away, or a
// collapsed tab is CapsuleW+Radius wide instead of CapsuleW and the x11
// window-slide arithmetic (which reveals exactly ShadowPad+CapsuleW px) breaks.
func TestDrawTab_CollapsedCapsuleDoesNotBleedIntoPanel(t *testing.T) {
	img := DrawTab(TabState{Expanded: false}, nil, silent())
	if got := img.RGBA.RGBAAt(ShadowPad+CapsuleW+2, BufH/2); got.A != 0 {
		t.Errorf("alpha just right of the capsule = %#x, want 0 (overhang leaked)", got.A)
	}
}

// Same invariant stated positively, and in both orientations: scanning the
// buffer's vertical centre must find exactly CapsuleW opaque pixels, starting
// at the capsule's left edge. Shadow is off so only the capsule is opaque.
func TestDrawTab_CapsuleOpaqueSpanIsExactlyCapsuleW(t *testing.T) {
	for _, tabRight := range []bool{false, true} {
		img := DrawTab(TabState{Expanded: false, TabRight: tabRight, Shadow: false}, nil, silent())
		y := BufH / 2
		first, count := -1, 0
		for x := 0; x < BufW; x++ {
			if img.RGBA.RGBAAt(x, y).A == 0xff {
				if first < 0 {
					first = x
				}
				count++
			}
		}
		if want := capsuleX(tabRight); first != want {
			t.Errorf("TabRight=%v: first opaque x = %d, want %d", tabRight, first, want)
		}
		if count != CapsuleW {
			t.Errorf("TabRight=%v: opaque span = %d px, want %d", tabRight, count, CapsuleW)
		}
	}
}

func TestDrawTab_ExpandedPanelIsOpaque(t *testing.T) {
	img := DrawTab(TabState{Expanded: true}, nil, silent())
	if got := img.RGBA.RGBAAt(200, BufH/2); got.A != 0xff {
		t.Errorf("expanded panel alpha = %#x, want 0xff", got.A)
	}
}

// The capsule carries the state colour; the panel must stay neutral. This is
// the regression guard for the old behaviour where the whole 300px panel was
// filled with amber or red.
func TestDrawTab_PanelIsNeutralNotStateColoured(t *testing.T) {
	p := silent()
	img := DrawTab(TabState{
		Expanded: true, Activity: "waiting", Attention: "needs", Waiting: "permission",
	}, nil, p)
	got := img.RGBA.RGBAAt(200, BufH/2)
	red := rgbaOf(p.Permission.Base)
	if got.R == red.R && got.G == red.G && got.B == red.B {
		t.Errorf("panel pixel = %v, must not be the permission colour", got)
	}
	capsule := img.RGBA.RGBAAt(ShadowPad+CapsuleW/2, BufH/2)
	if capsule.R < 0x80 {
		t.Errorf("capsule pixel = %v, expected a saturated red", capsule)
	}
}

func TestDrawTab_CornersAreAntialiased(t *testing.T) {
	img := DrawTab(TabState{Expanded: true, Shadow: false}, nil, silent())
	// Walk the top-left rounded corner and look for at least one partially
	// transparent pixel. A hard-edged (aliased) corner has only 0x00 and 0xff.
	found := false
	for y := ShadowPad; y < ShadowPad+Radius; y++ {
		for x := ShadowPad; x < ShadowPad+Radius; x++ {
			if a := img.RGBA.RGBAAt(x, y).A; a > 0 && a < 0xff {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no partial-alpha pixel in the corner; corner is not antialiased")
	}
}

// The shadow and halo shapes are unclipped on purpose — they must spread into
// the transparent pad. What they must NOT do is reach across the capsule into
// the panel: in the x11 orientation the panel is drawn first and the capsule's
// shadow/halo land on top of it, so any rightward overhang shows up as a dark
// band (or coloured wash) cast by nothing. Sampling a little way into the panel
// and requiring it to match the shadow-off render pins that.
func TestDrawTab_CapsuleShadowDoesNotDarkenPanel(t *testing.T) {
	base := TabState{Expanded: true, TabRight: false}
	on := DrawTab(withShadow(base, true), nil, silent())
	off := DrawTab(withShadow(base, false), nil, silent())
	x, y := ShadowPad+CapsuleW+12, BufH/2
	if got, want := on.RGBA.RGBAAt(x, y), off.RGBA.RGBAAt(x, y); got != want {
		t.Errorf("panel pixel at x=%d with shadow = %v, without = %v; the capsule's shadow bled into the panel", x, got, want)
	}
}

// Same invariant for the permission halo, whose shape is larger than the
// shadow's and whose colour makes a bleed even more obvious. The panel gradient
// does not depend on state, so a non-glowing state is a valid baseline.
func TestDrawTab_CapsuleHaloDoesNotWashPanel(t *testing.T) {
	p := silent()
	glow := TabState{
		Expanded: true, TabRight: false,
		Activity: "waiting", Attention: "needs", Waiting: "permission",
		HaloPhase: 0.5, // peak alpha
	}
	if !p.For(glow.Activity, glow.Attention, glow.Waiting).Glow {
		t.Fatalf("permission state is not a glow state; test no longer exercises the halo")
	}
	plain := TabState{
		Expanded: true, TabRight: false,
		Activity: "waiting", Attention: "needs", Waiting: "user",
	}
	x, y := ShadowPad+CapsuleW+12, BufH/2
	got := DrawTab(glow, nil, p).RGBA.RGBAAt(x, y)
	want := DrawTab(plain, nil, p).RGBA.RGBAAt(x, y)
	if got != want {
		t.Errorf("panel pixel at x=%d with halo = %v, without = %v; the halo washed the panel", x, got, want)
	}
}

// Corner rule: round the leading (left) edge of the outermost visible element,
// never an edge that butts another one. The two backends are mirrored, so the
// panel's left edge is rounded for wlr (it is the leading edge) and square for
// x11 (it butts the capsule). A future "simplification" that rounds both would
// put a visible notch between the x11 panel and its capsule.
func TestDrawTab_PanelLeftCornerFollowsOrientation(t *testing.T) {
	// x11: the panel's top-left pixel butts the capsule, so it must be square,
	// i.e. fully opaque right at the corner.
	x11 := DrawTab(TabState{Expanded: true, Shadow: false, TabRight: false}, nil, silent())
	if got := x11.RGBA.RGBAAt(ShadowPad+CapsuleW, ShadowPad); got.A != 0xff {
		t.Errorf("x11 panel top-left alpha = %#x, want 0xff (square corner; a rounded one leaves a notch beside the capsule)", got.A)
	}
	// wlr: the panel's left edge is the leading edge, so the corner is rounded
	// and its outermost pixel falls outside the shape.
	wlr := DrawTab(TabState{Expanded: true, Shadow: false, TabRight: true}, nil, silent())
	if got := wlr.RGBA.RGBAAt(0, ShadowPad); got.A != 0 {
		t.Errorf("wlr panel top-left alpha = %#x, want 0 (rounded corner)", got.A)
	}
}

func TestDrawTab_ShadowDarkensPadWhenEnabled(t *testing.T) {
	on := DrawTab(TabState{Expanded: true, Shadow: true}, nil, silent())
	off := DrawTab(TabState{Expanded: true, Shadow: false}, nil, silent())
	// Just left of the capsule, inside the pad, the shadow must add opacity.
	x, y := ShadowPad-2, BufH/2
	if on.RGBA.RGBAAt(x, y).A <= off.RGBA.RGBAAt(x, y).A {
		t.Errorf("shadow on alpha %#x not greater than off alpha %#x",
			on.RGBA.RGBAAt(x, y).A, off.RGBA.RGBAAt(x, y).A)
	}
}

func TestDrawTab_TabRightPutsCapsuleOnRight(t *testing.T) {
	img := DrawTab(TabState{Expanded: false, TabRight: true}, nil, silent())
	// Capsule now occupies the rightmost CapsuleW columns.
	if got := img.RGBA.RGBAAt(BufW-1-CapsuleW/2, BufH/2); got.A != 0xff {
		t.Errorf("right-edge capsule alpha = %#x, want 0xff", got.A)
	}
	// Far left is panel region → transparent when collapsed.
	if got := img.RGBA.RGBAAt(2, BufH/2); got.A != 0 {
		t.Errorf("far-left alpha = %#x, want 0", got.A)
	}
}

func TestDrawTab_WorkBarRunningSegments(t *testing.T) {
	p := silent()
	img := DrawTab(TabState{Expanded: false, BackgroundRunning: 2}, nil, p)
	want := rgbaOf(p.WorkRunning)
	got := img.RGBA.RGBAAt(workSegX(0, false)+1, workBarY()+1)
	if got.R != want.R || got.G != want.G || got.B != want.B {
		t.Errorf("first work segment = %v, want %v", got, want)
	}
	// Third segment should be the "off" colour with only two running.
	off := rgbaOf(p.WorkOff)
	got = img.RGBA.RGBAAt(workSegX(2, false)+1, workBarY()+1)
	if got.R != off.R || got.G != off.G || got.B != off.B {
		t.Errorf("third work segment = %v, want off colour %v", got, off)
	}
}

func TestDrawTab_WorkBarOutcome(t *testing.T) {
	p := silent()
	for _, c := range []struct {
		outcome string
		want    uint32
	}{{"done", p.WorkDone}, {"failed", p.WorkFailed}} {
		img := DrawTab(TabState{Expanded: false, BackgroundOutcome: c.outcome}, nil, p)
		got := img.RGBA.RGBAAt(workSegX(0, false)+1, workBarY()+1)
		want := rgbaOf(c.want)
		if got.R != want.R || got.G != want.G || got.B != want.B {
			t.Errorf("outcome %q segment = %v, want %v", c.outcome, got, want)
		}
	}
}

func TestDrawTab_NoWorkBarWhenIdle(t *testing.T) {
	p := silent()
	img := DrawTab(TabState{Expanded: false}, nil, p)
	// With no background work at all, no segments are drawn — the pixel keeps
	// the capsule gradient, which is not the off colour.
	off := rgbaOf(p.WorkOff)
	got := img.RGBA.RGBAAt(workSegX(0, false)+1, workBarY()+1)
	if got.R == off.R && got.G == off.G && got.B == off.B {
		t.Errorf("work bar drawn with no background work: %v", got)
	}
}

func TestDrawTab_NilFacesSkipsText(t *testing.T) {
	img := DrawTab(TabState{Expanded: true, Name: "ignored without faces"}, nil, silent())
	if img.Overflow {
		t.Errorf("overflow=true with nil faces; want false")
	}
}

func TestDrawTab_OverflowOnLongName(t *testing.T) {
	f, err := LoadFaces()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	img := DrawTab(TabState{Expanded: true, Name: long}, f, silent())
	if !img.Overflow {
		t.Errorf("overflow=false for 200-char name; want true")
	}
	short := DrawTab(TabState{Expanded: true, Name: "visor"}, f, silent())
	if short.Overflow {
		t.Errorf("overflow=true for short name; want false")
	}
}

func TestDrawTab_ElapsedRendersWithoutPanic(t *testing.T) {
	f, err := LoadFaces()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	DrawTab(TabState{
		Expanded: true, Name: "deepdub-platform", Glyph: "D", Path: "~/P/deepdub",
		Activity: "waiting", Attention: "needs", Waiting: "user",
		Elapsed: 4*time.Minute + 12*time.Second,
	}, f, silent())
}
