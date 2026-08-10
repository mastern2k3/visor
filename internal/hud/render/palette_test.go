package render

import "testing"

// lum is Rec.601 luminance of a packed 0xRRGGBB, matching the metric used in
// the spec to diagnose the original salience inversion.
func lum(c uint32) int {
	r := int((c >> 16) & 0xff)
	g := int((c >> 8) & 0xff)
	b := int(c & 0xff)
	return (r*299 + g*587 + b*114) / 1000
}

// chroma is a cheap saturation proxy: the spread between the strongest and
// weakest RGB channel. Unlike Rec.601 luminance it is comparable across hues,
// which is what makes it the right metric for "is this colour shouting".
func chroma(c uint32) int {
	r := int((c >> 16) & 0xff)
	g := int((c >> 8) & 0xff)
	b := int(c & 0xff)
	hi, lo := r, r
	for _, v := range []int{g, b} {
		if v > hi {
			hi = v
		}
		if v < lo {
			lo = v
		}
	}
	return hi - lo
}

func TestThemes_AllRegistered(t *testing.T) {
	got := Themes()
	want := []string{"silent", "traffic", "tuned"}
	if len(got) != len(want) {
		t.Fatalf("Themes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Themes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTheme_UnknownFallsBackToSilent(t *testing.T) {
	if _, ok := ThemeByName("nope"); ok {
		t.Errorf("ThemeByName(\"nope\") reported ok; want false")
	}
	if Theme("nope").Name != "silent" {
		t.Errorf("Theme(\"nope\").Name = %q, want \"silent\"", Theme("nope").Name)
	}
}

// Every token must be non-zero in every theme. A half-filled theme would
// otherwise render black regions with no error.
func TestPalette_AllTokensPopulated(t *testing.T) {
	for _, name := range Themes() {
		p := Theme(name)
		states := map[string]StateColors{
			"permission": p.Permission,
			"needs":      p.Needs,
			"working":    p.Working,
			"ack":        p.Ack,
			"dismissed":  p.Dismissed,
		}
		for sname, sc := range states {
			if sc.Top == 0 || sc.Base == 0 || sc.Bot == 0 {
				t.Errorf("%s/%s: zero gradient stop: %+v", name, sname, sc)
			}
		}
		if p.Permission.Halo == 0 || !p.Permission.Glow {
			t.Errorf("%s: permission must have a halo and glow enabled", name)
		}
		scalars := map[string]uint32{
			"WorkRunning": p.WorkRunning, "WorkDone": p.WorkDone,
			"WorkFailed": p.WorkFailed, "WorkOff": p.WorkOff,
			"PanelTop": p.PanelTop, "PanelBot": p.PanelBot,
			"PanelBorder": p.PanelBorder, "PanelName": p.PanelName,
			"PanelMeta": p.PanelMeta, "PanelElapsed": p.PanelElapsed,
			"GlyphDark": p.GlyphDark, "GlyphLight": p.GlyphLight,
		}
		for tname, v := range scalars {
			if v == 0 {
				t.Errorf("%s: token %s is zero", name, tname)
			}
		}
		if p.GlyphLumThreshold == 0 {
			t.Errorf("%s: GlyphLumThreshold is zero", name)
		}
	}
}

// The neutral ramp must descend: a working session is more present than an
// acknowledged one, which is more present than a dismissed one. This is the
// half of the original salience bug that was real — cyan "working" used to
// outshine both greys by a wide margin, and dismissed must recede furthest.
func TestPalette_NeutralRampDescends(t *testing.T) {
	for _, name := range Themes() {
		p := Theme(name)
		w, a, d := lum(p.Working.Base), lum(p.Ack.Base), lum(p.Dismissed.Base)
		if w < a {
			t.Errorf("%s: working lum %d must be >= ack lum %d", name, w, a)
		}
		if a < d {
			t.Errorf("%s: ack lum %d must be >= dismissed lum %d", name, a, d)
		}
	}
}

// Colour is reserved for "a human is needed". Both attention states must be
// more saturated than every state that needs nothing — this is the invariant
// that makes `silent` work and that keeps any future theme honest.
func TestPalette_AttentionStatesAreMostSaturated(t *testing.T) {
	for _, name := range Themes() {
		p := Theme(name)
		quiet := map[string]uint32{
			"working": p.Working.Base, "ack": p.Ack.Base, "dismissed": p.Dismissed.Base,
		}
		for _, attn := range []struct {
			label string
			c     uint32
		}{{"permission", p.Permission.Base}, {"needs", p.Needs.Base}} {
			for qname, q := range quiet {
				if chroma(attn.c) <= chroma(q) {
					t.Errorf("%s: %s chroma %d must exceed %s chroma %d",
						name, attn.label, chroma(attn.c), qname, chroma(q))
				}
			}
		}
	}
}

// Permission is the only state that pulses. Glow is the mechanism that makes
// red dominate despite amber being more luminous, so no other state may take
// it — a second glowing state would flatten the hierarchy.
func TestPalette_OnlyPermissionGlows(t *testing.T) {
	for _, name := range Themes() {
		p := Theme(name)
		if !p.Permission.Glow {
			t.Errorf("%s: permission must glow", name)
		}
		others := map[string]StateColors{
			"needs": p.Needs, "working": p.Working,
			"ack": p.Ack, "dismissed": p.Dismissed,
		}
		for oname, sc := range others {
			if sc.Glow {
				t.Errorf("%s: %s must not glow", name, oname)
			}
			if sc.Halo != 0 {
				t.Errorf("%s: %s must not define a halo (got %#06x)", name, oname, sc.Halo)
			}
		}
	}
}

// TestPalette_RegisterMergesOverridesAndDefaults exercises both directions of
// register()'s zero-fill merge. A regression that made register() ignore
// theme overrides (e.g. reverting to a hardcoded two-field re-apply) would
// still pass TestPalette_AllTokensPopulated, since the shared default is also
// non-zero — this test is the one that would actually catch it.
func TestPalette_RegisterMergesOverridesAndDefaults(t *testing.T) {
	traffic := Theme("traffic")
	if traffic.WorkRunning != 0x0d3b33 {
		t.Errorf("traffic.WorkRunning = %#06x, want declared override %#06x", traffic.WorkRunning, 0x0d3b33)
	}
	if traffic.WorkDone != 0xa3d977 {
		t.Errorf("traffic.WorkDone = %#06x, want shared default %#06x", traffic.WorkDone, 0xa3d977)
	}

	silent := Theme("silent")
	if silent.WorkRunning != 0x8be0d0 {
		t.Errorf("silent.WorkRunning = %#06x, want shared default %#06x", silent.WorkRunning, 0x8be0d0)
	}
}

func TestPalette_For(t *testing.T) {
	p := Theme("silent")
	cases := []struct {
		activity, attention, waiting string
		want                         uint32
	}{
		{"waiting", "needs", "permission", p.Permission.Base},
		{"waiting", "needs", "user", p.Needs.Base},
		{"working", "ack", "", p.Working.Base},
		{"waiting", "dismissed", "", p.Dismissed.Base},
		{"waiting", "ack", "", p.Ack.Base},
		{"unknown", "ack", "", p.Ack.Base},
	}
	for _, c := range cases {
		got := p.For(c.activity, c.attention, c.waiting).Base
		if got != c.want {
			t.Errorf("For(%q,%q,%q).Base = %#06x, want %#06x",
				c.activity, c.attention, c.waiting, got, c.want)
		}
	}
}

func TestPalette_GlyphFG(t *testing.T) {
	p := Theme("silent")
	if got := p.GlyphFG(0xffffff); got != p.GlyphDark {
		t.Errorf("GlyphFG(white) = %#06x, want GlyphDark %#06x", got, p.GlyphDark)
	}
	if got := p.GlyphFG(0x101010); got != p.GlyphLight {
		t.Errorf("GlyphFG(near-black) = %#06x, want GlyphLight %#06x", got, p.GlyphLight)
	}
}
