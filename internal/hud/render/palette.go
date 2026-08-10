package render

import "sort"

// StateColors is the per-state colour token group. Top/Base/Bot are the three
// vertical gradient stops of the capsule (Base sits at 62%). Halo is the glow
// colour used only when Glow is set.
type StateColors struct {
	Top  uint32
	Base uint32
	Bot  uint32
	Halo uint32
	Glow bool
}

// Palette is the COMPLETE set of colour decisions for one theme — not just the
// five state hues. A theme that only re-tinted the capsules would leave the
// panel and work-bar hardcoded, and `silent` in particular only reads correctly
// because its panel and work-bar are tuned with it.
type Palette struct {
	Name string

	Permission StateColors
	Needs      StateColors
	Working    StateColors
	Ack        StateColors
	Dismissed  StateColors

	// Background-work micro-bar.
	WorkRunning uint32
	WorkDone    uint32
	WorkFailed  uint32
	WorkOff     uint32 // unfilled segment

	// Expanded panel.
	PanelTop     uint32
	PanelBot     uint32
	PanelBorder  uint32
	PanelName    uint32
	PanelMeta    uint32
	PanelElapsed uint32

	// Capsule glyph. GlyphLumThreshold replaces the magic 140 that used to
	// live in contrastFG.
	GlyphLumThreshold int
	GlyphDark         uint32
	GlyphLight        uint32
}

// panelDefaults are shared by all themes; a theme may override any field after
// embedding them.
func panelDefaults(p *Palette) {
	p.PanelTop = 0x1a1e27
	p.PanelBot = 0x12151c
	p.PanelBorder = 0x2b2f39
	p.PanelName = 0xe8eef6
	p.PanelMeta = 0x8b95a6
	p.PanelElapsed = 0x7b8595
	p.WorkRunning = 0x8be0d0
	p.WorkDone = 0xa3d977
	p.WorkFailed = 0xff7a7a
	p.WorkOff = 0x1d2129
	p.GlyphLumThreshold = 140
	p.GlyphDark = 0x10141c
	p.GlyphLight = 0xe5e9f0
}

var themes = map[string]Palette{}

// register applies panelDefaults to fill any field the theme literal left at
// its zero value, then stores the merged palette. Unlike a two-field
// "re-apply the overrides" shim, this survives a theme overriding *any*
// shared token — not just the two fields a hand-written override list
// happened to anticipate. Non-zero fields declared directly on p always win;
// defaults only backfill what's zero.
//
// Coupling: this list must have one branch per shared scalar field on
// Palette. Adding a new shared token (WorkX, PanelX, GlyphX) requires adding
// a matching backfill branch here, or the new field will stay zero for every
// theme that doesn't set it explicitly.
func register(p Palette) {
	d := Palette{}
	panelDefaults(&d)

	if p.WorkRunning == 0 {
		p.WorkRunning = d.WorkRunning
	}
	if p.WorkDone == 0 {
		p.WorkDone = d.WorkDone
	}
	if p.WorkFailed == 0 {
		p.WorkFailed = d.WorkFailed
	}
	if p.WorkOff == 0 {
		p.WorkOff = d.WorkOff
	}
	if p.PanelTop == 0 {
		p.PanelTop = d.PanelTop
	}
	if p.PanelBot == 0 {
		p.PanelBot = d.PanelBot
	}
	if p.PanelBorder == 0 {
		p.PanelBorder = d.PanelBorder
	}
	if p.PanelName == 0 {
		p.PanelName = d.PanelName
	}
	if p.PanelMeta == 0 {
		p.PanelMeta = d.PanelMeta
	}
	if p.PanelElapsed == 0 {
		p.PanelElapsed = d.PanelElapsed
	}
	if p.GlyphLumThreshold == 0 {
		p.GlyphLumThreshold = d.GlyphLumThreshold
	}
	if p.GlyphDark == 0 {
		p.GlyphDark = d.GlyphDark
	}
	if p.GlyphLight == 0 {
		p.GlyphLight = d.GlyphLight
	}

	themes[p.Name] = p
}

func init() {
	// tuned (default) — today's hue family, ordering fixed. Red becomes the
	// brightest and most saturated element; amber drops below it; cyan is
	// dimmed so "busy, needs nothing" stops shouting.
	register(Palette{
		Name:       "tuned",
		Permission: StateColors{Top: 0xff8a7d, Base: 0xff5a4f, Bot: 0xe04338, Halo: 0xff5a4f, Glow: true},
		Needs:      StateColors{Top: 0xf7c95e, Base: 0xe8ad2e, Bot: 0xc9911c},
		Working:    StateColors{Top: 0x7fa8bd, Base: 0x5e8fa8, Bot: 0x4a7690},
		Ack:        StateColors{Top: 0x5b6472, Base: 0x4a5260, Bot: 0x3c4351},
		Dismissed:  StateColors{Top: 0x333949, Base: 0x2c3140, Bot: 0x242936},
	})

	// silent — colour spent only on "a human is needed". Working, ack and
	// dismissed are neutrals separated by luminance alone; activity is
	// carried by the wobble and the work-bar, not hue.
	register(Palette{
		Name:       "silent",
		Permission: StateColors{Top: 0xff8a7d, Base: 0xff4d43, Bot: 0xdd3a30, Halo: 0xff4d43, Glow: true},
		Needs:      StateColors{Top: 0xffc86b, Base: 0xffa826, Bot: 0xe08b12},
		Working:    StateColors{Top: 0x49525f, Base: 0x3d4653, Bot: 0x333b47},
		Ack:        StateColors{Top: 0x3d434e, Base: 0x343a45, Bot: 0x2c313b},
		Dismissed:  StateColors{Top: 0x2b303a, Base: 0x252a33, Bot: 0x1f242b},
	})

	// traffic — saturation pushed everywhere; working goes vivid mint. Needs a
	// dark WorkRunning because teal segments vanish on a light capsule.
	register(Palette{
		Name:        "traffic",
		Permission:  StateColors{Top: 0xff7b6e, Base: 0xf43f31, Bot: 0xd22a1d, Halo: 0xf43f31, Glow: true},
		Needs:       StateColors{Top: 0xffd24d, Base: 0xfbbf24, Bot: 0xe0a509},
		Working:     StateColors{Top: 0x7fe3d0, Base: 0x34d3b4, Bot: 0x1fb598},
		Ack:         StateColors{Top: 0x8b95a5, Base: 0x71809a, Bot: 0x5c6a82},
		Dismissed:   StateColors{Top: 0x454c5c, Base: 0x3a4150, Bot: 0x2f3541},
		WorkRunning: 0x0d3b33,
	})
}

// DefaultTheme is used when config is absent or names an unknown theme.
//
// It was "silent" until the mockup review; the real dock disagreed. Silent's
// working (#3d4653) and ack (#343a45) states differ by ~11 Rec.601 luminance —
// indistinguishable at a glance on a real screen, even though that same pair
// read fine through the blurred peripheral-vision mockup panel used to judge
// it. "tuned" is now the default; silent and traffic still ship as opt-in
// themes with unchanged hex values.
const DefaultTheme = "tuned"

// Themes returns the registered theme names in sorted order.
func Themes() []string {
	out := make([]string, 0, len(themes))
	for n := range themes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ThemeByName looks up a theme, reporting whether it exists.
func ThemeByName(name string) (Palette, bool) {
	p, ok := themes[name]
	return p, ok
}

// Theme looks up a theme, falling back to DefaultTheme for unknown names so a
// typo in the config file degrades instead of failing to start.
func Theme(name string) Palette {
	if p, ok := themes[name]; ok {
		return p
	}
	return themes[DefaultTheme]
}

// For maps session state to its colour group. Mirrors the precedence the old
// package-level ColorFor used: permission outranks needs, needs outranks
// dismissed, dismissed outranks activity.
func (p Palette) For(activity, attention, waiting string) StateColors {
	switch {
	case attention == "needs" && waiting == "permission":
		return p.Permission
	case attention == "needs":
		return p.Needs
	case attention == "dismissed":
		return p.Dismissed
	case activity == "working":
		return p.Working
	default:
		return p.Ack
	}
}

// GlyphFG picks the capsule glyph colour that reads against `base`, using the
// theme's own luminance threshold.
func (p Palette) GlyphFG(base uint32) uint32 {
	r := int((base >> 16) & 0xff)
	g := int((base >> 8) & 0xff)
	b := int(base & 0xff)
	if (r*299+g*587+b*114)/1000 > p.GlyphLumThreshold {
		return p.GlyphDark
	}
	return p.GlyphLight
}
