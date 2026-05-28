package render

import (
	"image/color"
	"testing"
)

func TestDrawTab_BackgroundFill(t *testing.T) {
	// Expanded=true: both the tab strip and panel region should be opaque.
	img := DrawTab(TabState{Color: 0x223344, Label: "", Expanded: true}, nil)
	if img.RGBA.Bounds().Dx() != ExpandedW || img.RGBA.Bounds().Dy() != TabH {
		t.Fatalf("size = %v, want %dx%d", img.RGBA.Bounds(), ExpandedW, TabH)
	}
	// Sample a pixel in the collapsed-tab region (x=2) and one in the panel (x=200).
	for _, x := range []int{2, 200} {
		got := img.RGBA.RGBAAt(x, TabH/2)
		want := color.RGBA{R: 0x22, G: 0x33, B: 0x44, A: 0xff}
		if got != want {
			t.Errorf("pixel at (%d,%d) = %v, want %v", x, TabH/2, got, want)
		}
	}
}

func TestDrawTab_NoFontSkipsText(t *testing.T) {
	img := DrawTab(TabState{Color: 0x000000, Label: "ignored without font"}, nil)
	if img.Overflow {
		t.Errorf("overflow=true with nil font; want false")
	}
}

func TestDrawTab_OverflowOnLongLabel(t *testing.T) {
	font, err := LoadFont()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	img := DrawTab(TabState{Color: 0x445566, Label: long, Expanded: true}, font)
	if !img.Overflow {
		t.Errorf("overflow=false for 200-char label; want true")
	}
}

func TestDrawTab_CollapsedHasTransparentPanel(t *testing.T) {
	img := DrawTab(TabState{Color: 0x223344, Expanded: false}, nil)
	// Tab strip (x=2) should be opaque bg.
	got := img.RGBA.RGBAAt(2, TabH/2)
	if got.A != 0xff {
		t.Errorf("tab strip alpha = %#x, want 0xff", got.A)
	}
	// Panel region (x=150) should be transparent.
	got = img.RGBA.RGBAAt(150, TabH/2)
	if got.A != 0 {
		t.Errorf("panel alpha = %#x, want 0", got.A)
	}
}

func TestDrawTab_ExpandedHasOpaquePanel(t *testing.T) {
	img := DrawTab(TabState{Color: 0x223344, Expanded: true}, nil)
	got := img.RGBA.RGBAAt(150, TabH/2)
	if got.A != 0xff {
		t.Errorf("expanded panel alpha = %#x, want 0xff", got.A)
	}
}

func TestDrawTab_TabRight_CollapsedHasTransparentLeft(t *testing.T) {
	img := DrawTab(TabState{Color: 0x223344, Expanded: false, TabRight: true}, nil)
	// Tab strip at right edge (x=ExpandedW-2) should be opaque.
	got := img.RGBA.RGBAAt(ExpandedW-2, TabH/2)
	if got.A != 0xff {
		t.Errorf("right-edge tab strip alpha = %#x, want 0xff", got.A)
	}
	// Panel region (x=150) should be transparent.
	got = img.RGBA.RGBAAt(150, TabH/2)
	if got.A != 0 {
		t.Errorf("panel alpha = %#x, want 0", got.A)
	}
	// Far-left should be transparent too.
	got = img.RGBA.RGBAAt(2, TabH/2)
	if got.A != 0 {
		t.Errorf("far-left alpha = %#x, want 0", got.A)
	}
}

func TestDrawTab_TabRight_ExpandedHasOpaqueFull(t *testing.T) {
	img := DrawTab(TabState{Color: 0x223344, Expanded: true, TabRight: true}, nil)
	for _, x := range []int{2, 150, ExpandedW - 2} {
		got := img.RGBA.RGBAAt(x, TabH/2)
		if got.A != 0xff {
			t.Errorf("x=%d alpha = %#x, want 0xff", x, got.A)
		}
	}
}

func TestContrastFG(t *testing.T) {
	cases := []struct {
		bg   color.RGBA
		want uint8 // R component of expected fg
	}{
		{color.RGBA{0xff, 0xff, 0xff, 0xff}, 0x10}, // bright → dark fg
		{color.RGBA{0x10, 0x10, 0x10, 0xff}, 0xe5}, // dark → bright fg
	}
	for _, c := range cases {
		got := contrastFG(c.bg)
		if got.R != c.want {
			t.Errorf("contrastFG(%v).R = %#x, want %#x", c.bg, got.R, c.want)
		}
	}
}
