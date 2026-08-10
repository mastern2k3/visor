# HUD Visual Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace visor's hand-rolled pixel layer with an antialiased capsule-tab renderer, make the palette a themeable token set with a correct salience order, give x11 per-pixel alpha, and retire the eww backend.

**Architecture:** `internal/hud/render` stays the single shared renderer both backends call, but is rewritten on `fogleman/gg` (paths, gradients, antialiasing) and `x/image/font/opentype` (text). Colour moves out of a package-level switch into a `Palette` token struct with three registered themes, selected from a watched config file. The x11 backend gains a depth-32 ARGB visual with a raw `PutImage` upload path and an XShape input region; wlr changes are geometry-only.

**Tech Stack:** Go 1.26.1, `github.com/fogleman/gg`, `golang.org/x/image/font/opentype` + `sfnt`, `github.com/jezek/xgb` (+ `shape` extension), `codeberg.org/tesselslate/wl`, `github.com/fsnotify/fsnotify`.

**Spec:** `docs/superpowers/specs/2026-08-05-hud-visual-redesign-design.md`

## Global Constraints

- **No cgo.** Every dependency must be pure Go; the deliverable is a single static binary.
- **Go 1.26.1** per `go.mod`.
- **Never widen the broadcast digest with high-frequency fields.** `hudDigest` in `internal/state/notify.go` must not gain `LastUpdate` or `StateSince`. Adding `Glyph` is explicitly correct (low-frequency, HUD-observable); adding a timestamp is not.
- **One renderer.** Both backends call the same `render.DrawTab`. Do not fork per-backend drawing code.
- **Hooks must stay silent on failure** — unchanged by this work, but do not regress it.
- **Logging uses `log/slog`.**
- **Geometry constants live in `render` only.** Backends import them; they never redeclare literals.
- **Every task must leave `go build ./... && go test ./...` green.**
- Exact palette hex values are in the spec's "Themes" section and must be copied verbatim.

## File Structure

**Created:**
- `internal/hud/render/palette.go` — `Palette`, `StateColors`, the three themes, registry lookup.
- `internal/hud/render/faces.go` — opentype face loading (`Faces` = name face + meta face).
- `internal/hud/render/format.go` — `StateWords`, `ElapsedString` (pure string helpers).
- `internal/hud/render/palette_test.go`, `faces_test.go`, `format_test.go`, `golden_test.go`
- `internal/hud/render/testdata/*.png` — golden images.
- `internal/hud/config/config.go` — theme + shadow config file, parse/write/resolve.
- `internal/hud/config/config_test.go`
- `internal/hud/x11/argb.go` — depth-32 visual/colormap/window, raw `PutImage`, compositor detection, XShape helpers.

**Modified:**
- `internal/hud/render/tab.go` — full rewrite of `DrawTab`; new geometry constants; `fillDot`/`drawBackgroundDots`/`contrastFG`/`ColorFor` deleted.
- `internal/hud/render/font.go` — `LoadFont` deleted once `faces.go` lands.
- `internal/hud/render/tab_test.go` — updated to new geometry and signature.
- `internal/hud/x11/tab.go`, `dock.go`, `subscribe.go`, `help.go`, `x11.go`
- `internal/hud/wlr/surface.go`, `dock.go`, `subscribe.go`
- `internal/state/state.go` — `StateSince`, `DisplayName`, `Glyph`.
- `internal/state/notify.go` — `Glyph` into the digest.
- `cmd/visor/hud.go` — `pickBackend` auto-detect, `theme`/`shadow` subcommands.
- `CLAUDE.md`

**Deleted:** `internal/hud/eww/` (whole package), `cmd/visorprobe/` (last task).

---

### Task 1: Palette token set and three themes

Pure data + lookup. No rendering yet. `render.ColorFor` is deliberately left in place so the tree keeps building; Task 5 removes it.

**Files:**
- Create: `internal/hud/render/palette.go`
- Test: `internal/hud/render/palette_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type StateColors struct { Top, Base, Bot, Halo uint32; Glow bool }`
  - `type Palette struct { ... }` (fields listed in Step 3)
  - `func Themes() []string` — sorted registered theme names.
  - `func ThemeByName(name string) (Palette, bool)`
  - `func Theme(name string) Palette` — falls back to `tuned` for unknown names.
    (Note: this plan and its Step-1 test snippets below were written when
    `silent` was still the default. A follow-up changed the default to
    `tuned` — see `render.DefaultTheme`'s doc comment — because the mockup
    review judged `silent`'s working/ack contrast through a blurred
    peripheral-vision panel, which flatters it; the real dock disagreed. The
    code blocks below are left as the historical record of what Step 1
    actually shipped and are no longer literally accurate.)
  - `func (p Palette) For(activity, attention, waiting string) StateColors`
  - `func (p Palette) GlyphFG(base uint32) uint32`

- [ ] **Step 1: Write the failing tests**

```go
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

// This is the bug that motivated the redesign: amber "needs" outshone red
// "permission", and cyan "working" outshone both greys. Encode the ordering so
// it cannot regress.
func TestPalette_SalienceOrdering(t *testing.T) {
	for _, name := range Themes() {
		p := Theme(name)
		if lum(p.Permission.Base) <= lum(p.Needs.Base) {
			t.Errorf("%s: permission lum %d must exceed needs lum %d",
				name, lum(p.Permission.Base), lum(p.Needs.Base))
		}
		if lum(p.Needs.Base) <= lum(p.Working.Base) {
			t.Errorf("%s: needs lum %d must exceed working lum %d",
				name, lum(p.Needs.Base), lum(p.Working.Base))
		}
		if lum(p.Working.Base) < lum(p.Dismissed.Base) {
			t.Errorf("%s: working lum %d must be >= dismissed lum %d",
				name, lum(p.Working.Base), lum(p.Dismissed.Base))
		}
		if lum(p.Ack.Base) < lum(p.Dismissed.Base) {
			t.Errorf("%s: ack lum %d must be >= dismissed lum %d",
				name, lum(p.Ack.Base), lum(p.Dismissed.Base))
		}
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hud/render/ -run 'TestThemes|TestTheme_|TestPalette' -v`
Expected: FAIL — `undefined: Themes`, `undefined: StateColors`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/hud/render/palette.go`. Hex values copied verbatim from the spec's Themes tables.

```go
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

func register(p Palette) {
	panelApply := p
	panelDefaults(&panelApply)
	// Re-apply any non-zero overrides the theme declared.
	if p.WorkRunning != 0 {
		panelApply.WorkRunning = p.WorkRunning
	}
	if p.PanelTop != 0 {
		panelApply.PanelTop = p.PanelTop
	}
	themes[p.Name] = panelApply
}

func init() {
	// tuned — today's hue family, ordering fixed. Red becomes the brightest
	// and most saturated element; amber drops below it; cyan is dimmed so
	// "busy, needs nothing" stops shouting.
	register(Palette{
		Name:       "tuned",
		Permission: StateColors{Top: 0xff8a7d, Base: 0xff5a4f, Bot: 0xe04338, Halo: 0xff5a4f, Glow: true},
		Needs:      StateColors{Top: 0xf7c95e, Base: 0xe8ad2e, Bot: 0xc9911c},
		Working:    StateColors{Top: 0x7fa8bd, Base: 0x5e8fa8, Bot: 0x4a7690},
		Ack:        StateColors{Top: 0x5b6472, Base: 0x4a5260, Bot: 0x3c4351},
		Dismissed:  StateColors{Top: 0x333949, Base: 0x2c3140, Bot: 0x242936},
	})

	// silent (default) — colour spent only on "a human is needed". Working,
	// ack and dismissed are neutrals separated by luminance alone; activity is
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
const DefaultTheme = "silent"

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hud/render/ -run 'TestThemes|TestTheme_|TestPalette' -v`
Expected: PASS (7 tests)

If `TestPalette_SalienceOrdering` fails, do **not** relax the assertion — the hex values are wrong. Compare against the spec tables.

- [ ] **Step 5: Verify nothing else broke and commit**

```bash
go build ./... && go test ./...
git add internal/hud/render/palette.go internal/hud/render/palette_test.go
git commit -m "feat(render): add Palette token set with tuned/silent/traffic themes"
```

---

### Task 2: HUD config file (theme + shadow)

**Files:**
- Create: `internal/hud/config/config.go`
- Test: `internal/hud/config/config_test.go`

**Interfaces:**
- Consumes: `render.Themes`, `render.DefaultTheme` (Task 1).
- Produces:
  - `type Config struct { Theme string; Shadow bool }`
  - `func Path() string`
  - `func Load() Config` — never errors; missing/corrupt file yields defaults.
  - `func Resolve(flagTheme string, flagShadow *bool) Config`
  - `func Save(c Config) error`
  - `func Parse(r io.Reader) Config`

Precedence per key: flag → env (`VISOR_THEME`, `VISOR_SHADOW`) → file → default
(`tuned`, `true`) — the default theme changed from `silent` to `tuned` in a
follow-up after the real dock showed `silent`'s working/ack states as the same
dark grey; the code blocks below still show `silent` as the historical record
of what this step originally shipped.

- [ ] **Step 1: Write the failing tests**

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_KeysAndComments(t *testing.T) {
	in := "# a comment\n\ntheme = traffic\nshadow = false\n"
	got := Parse(strings.NewReader(in))
	if got.Theme != "traffic" {
		t.Errorf("Theme = %q, want \"traffic\"", got.Theme)
	}
	if got.Shadow {
		t.Errorf("Shadow = true, want false")
	}
}

func TestParse_DefaultsWhenEmpty(t *testing.T) {
	got := Parse(strings.NewReader(""))
	if got.Theme != "silent" || !got.Shadow {
		t.Errorf("Parse(empty) = %+v, want {silent true}", got)
	}
}

func TestParse_UnknownThemeFallsBack(t *testing.T) {
	got := Parse(strings.NewReader("theme = nonsense\n"))
	if got.Theme != "silent" {
		t.Errorf("Theme = %q, want \"silent\" for unknown theme", got.Theme)
	}
}

func TestParse_IgnoresJunkLines(t *testing.T) {
	got := Parse(strings.NewReader("garbage\ntheme=tuned\nshadow\n"))
	if got.Theme != "tuned" {
		t.Errorf("Theme = %q, want \"tuned\"", got.Theme)
	}
	if !got.Shadow {
		t.Errorf("Shadow = false, want default true when value is malformed")
	}
}

func TestResolve_Precedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "visor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("theme = tuned\nshadow = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// File only.
	if got := Resolve("", nil); got.Theme != "tuned" || got.Shadow {
		t.Errorf("file-only Resolve = %+v, want {tuned false}", got)
	}

	// Env beats file.
	t.Setenv("VISOR_THEME", "traffic")
	t.Setenv("VISOR_SHADOW", "true")
	if got := Resolve("", nil); got.Theme != "traffic" || !got.Shadow {
		t.Errorf("env Resolve = %+v, want {traffic true}", got)
	}

	// Flag beats env.
	no := false
	if got := Resolve("silent", &no); got.Theme != "silent" || got.Shadow {
		t.Errorf("flag Resolve = %+v, want {silent false}", got)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Save(Config{Theme: "traffic", Shadow: false}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.Theme != "traffic" || got.Shadow {
		t.Errorf("Load after Save = %+v, want {traffic false}", got)
	}
}

func TestLoad_MissingFileYieldsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := Load()
	if got.Theme != "silent" || !got.Shadow {
		t.Errorf("Load(missing) = %+v, want {silent true}", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hud/config/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

```go
// Package config reads and writes the HUD's on-disk preferences: which
// palette theme to use, and whether visor draws its own drop shadow.
//
// The format is deliberately flat `key = value` rather than TOML — there are
// two keys and no foreseeable nesting.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nitzanz/visor/internal/hud/render"
)

// Config is the resolved HUD preference set.
type Config struct {
	Theme  string
	Shadow bool
}

// Default is used when no file, env, or flag says otherwise.
func Default() Config {
	return Config{Theme: render.DefaultTheme, Shadow: true}
}

// Path returns the config file location: $XDG_CONFIG_HOME/visor/hud.conf,
// falling back to ~/.config/visor/hud.conf.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "visor-hud.conf"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "visor", "hud.conf")
}

// Parse reads flat key = value lines. Unknown keys, malformed lines and
// unknown theme names are ignored in favour of defaults — a bad config must
// degrade, never prevent the HUD from starting.
func Parse(r io.Reader) Config {
	c := Default()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "theme":
			if _, known := render.ThemeByName(v); known {
				c.Theme = v
			}
		case "shadow":
			switch v {
			case "true", "on", "yes", "1":
				c.Shadow = true
			case "false", "off", "no", "0":
				c.Shadow = false
			}
		}
	}
	return c
}

// Load reads the config file, returning defaults if it is missing or unreadable.
func Load() Config {
	f, err := os.Open(Path())
	if err != nil {
		return Default()
	}
	defer f.Close()
	return Parse(f)
}

// Resolve applies precedence: flag → env → file → default. A nil flagShadow
// or empty flagTheme means "the flag was not set".
func Resolve(flagTheme string, flagShadow *bool) Config {
	c := Load()
	if v := os.Getenv("VISOR_THEME"); v != "" {
		if _, known := render.ThemeByName(v); known {
			c.Theme = v
		}
	}
	if v := os.Getenv("VISOR_SHADOW"); v != "" {
		switch v {
		case "true", "on", "yes", "1":
			c.Shadow = true
		case "false", "off", "no", "0":
			c.Shadow = false
		}
	}
	if flagTheme != "" {
		if _, known := render.ThemeByName(flagTheme); known {
			c.Theme = flagTheme
		}
	}
	if flagShadow != nil {
		c.Shadow = *flagShadow
	}
	return c
}

// Save writes both keys, creating the directory if needed.
func Save(c Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# visor HUD config. Themes: %s\ntheme = %s\nshadow = %t\n",
		strings.Join(render.Themes(), ", "), c.Theme, c.Shadow)
	return os.WriteFile(p, []byte(body), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hud/config/ -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/config
git commit -m "feat(hud): add theme/shadow config file with flag>env>file precedence"
```

---

### Task 3: Opentype font faces

Adds the new face loader alongside the existing `LoadFont`. Nothing consumes it yet, so the tree keeps building. `LoadFont` and the `freetype-go` dependency are removed in Task 5.

**Files:**
- Create: `internal/hud/render/faces.go`
- Test: `internal/hud/render/faces_test.go`
- Modify: `internal/hud/render/font.go` (extract path resolution for reuse)

**Interfaces:**
- Consumes: `fontCandidates`, `loadViaFontconfig` behaviour from `font.go`.
- Produces:
  - `type Faces struct { Name, Meta font.Face }`
  - `func LoadFaces() (*Faces, error)`
  - `func FontPath() (string, error)` — extracted from the existing resolution order.
  - Constants `NamePt = 12.5`, `MetaPt = 9.5`.

- [ ] **Step 1: Write the failing test**

```go
package render

import "testing"

func TestLoadFaces(t *testing.T) {
	f, err := LoadFaces()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	if f.Name == nil || f.Meta == nil {
		t.Fatalf("LoadFaces returned nil face: %+v", f)
	}
	// The name face must be visibly larger than the meta face.
	nameH := f.Name.Metrics().Height.Ceil()
	metaH := f.Meta.Metrics().Height.Ceil()
	if nameH <= metaH {
		t.Errorf("name face height %d must exceed meta face height %d", nameH, metaH)
	}
	// Ascent must be positive — this is what replaces the hardcoded baseline.
	if f.Name.Metrics().Ascent.Ceil() <= 0 {
		t.Errorf("name ascent = %d, want > 0", f.Name.Metrics().Ascent.Ceil())
	}
}

func TestFontPath(t *testing.T) {
	p, err := FontPath()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	if p == "" {
		t.Errorf("FontPath returned empty path with nil error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hud/render/ -run 'TestLoadFaces|TestFontPath' -v`
Expected: FAIL — `undefined: LoadFaces`, `undefined: FontPath`

- [ ] **Step 3: Write the implementation**

Create `internal/hud/render/faces.go`:

```go
package render

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// Text sizes for the two panel lines. Rendered at 72 DPI so points map 1:1
// to pixels, matching the geometry constants in tab.go.
const (
	NamePt = 12.5 // line 1: project name
	MetaPt = 9.5  // line 2: state words, elapsed, path
	GlyphPt = 11  // capsule glyph
)

// Faces holds the parsed faces the renderer needs. Baselines are derived from
// these metrics rather than hardcoded, replacing the old empirical constant.
type Faces struct {
	Name  font.Face
	Meta  font.Face
	Glyph font.Face
}

// FontPath resolves a monospaced TrueType/OpenType font path, preferring
// fontconfig and falling back to the hardcoded candidates in font.go.
func FontPath() (string, error) {
	out, err := exec.Command("fc-match", "-f", "%{file}\n",
		"monospace:fontformat=TrueType").Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			if _, serr := os.Stat(p); serr == nil {
				return p, nil
			}
		}
	}
	for _, p := range fontCandidates {
		if _, serr := os.Stat(p); serr == nil {
			return p, nil
		}
	}
	return "", errors.New("no mono TTF font found: fc-match not on PATH (or returned non-TTF) and no fallback paths under /usr/share/fonts matched")
}

// LoadFaces resolves the system mono font and derives the three faces.
func LoadFaces() (*Faces, error) {
	p, err := FontPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read font %s: %w", p, err)
	}
	sf, err := opentype.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("parse font %s: %w", p, err)
	}
	mk := func(pt float64) (font.Face, error) {
		return opentype.NewFace(sf, &opentype.FaceOptions{
			Size: pt, DPI: 72, Hinting: font.HintingFull,
		})
	}
	nameFace, err := mk(NamePt)
	if err != nil {
		return nil, err
	}
	metaFace, err := mk(MetaPt)
	if err != nil {
		return nil, err
	}
	glyphFace, err := mk(GlyphPt)
	if err != nil {
		return nil, err
	}
	return &Faces{Name: nameFace, Meta: metaFace, Glyph: glyphFace}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hud/render/ -run 'TestLoadFaces|TestFontPath' -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/render/faces.go internal/hud/render/faces_test.go
git commit -m "feat(render): add opentype face loading with metric-derived sizes"
```

---

### Task 4: State words and elapsed formatting

Small pure helpers, split from the renderer so their edge cases get their own test cycle.

**Files:**
- Create: `internal/hud/render/format.go`
- Test: `internal/hud/render/format_test.go`

**Interfaces:**
- Produces:
  - `func StateWords(activity, attention, waiting string) string`
  - `func ElapsedString(d time.Duration) string`

- [ ] **Step 1: Write the failing tests**

```go
package render

import (
	"testing"
	"time"
)

func TestStateWords(t *testing.T) {
	cases := []struct {
		activity, attention, waiting, want string
	}{
		{"waiting", "needs", "permission", "blocked on approval"},
		{"waiting", "needs", "user", "waiting for you"},
		{"waiting", "dismissed", "", "dismissed"},
		{"working", "ack", "", "working"},
		{"waiting", "ack", "", "idle"},
		{"unknown", "ack", "", "idle"},
		// permission outranks everything, even while working.
		{"working", "needs", "permission", "blocked on approval"},
	}
	for _, c := range cases {
		if got := StateWords(c.activity, c.attention, c.waiting); got != c.want {
			t.Errorf("StateWords(%q,%q,%q) = %q, want %q",
				c.activity, c.attention, c.waiting, got, c.want)
		}
	}
}

func TestElapsedString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{4*time.Minute + 12*time.Second, "4m 12s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"},
		{time.Hour + 4*time.Minute, "1h 04m"},
		{26*time.Hour + 3*time.Minute, "26h 03m"},
		// Negative durations (clock skew between daemon and HUD) must not
		// render as garbage like "-1m -3s".
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := ElapsedString(c.d); got != c.want {
			t.Errorf("ElapsedString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hud/render/ -run 'TestStateWords|TestElapsedString' -v`
Expected: FAIL — `undefined: StateWords`, `undefined: ElapsedString`

- [ ] **Step 3: Write the implementation**

```go
package render

import (
	"fmt"
	"time"
)

// StateWords is the human-readable state shown on the expanded panel's second
// line. Precedence matches Palette.For so the words and the colour can never
// disagree.
func StateWords(activity, attention, waiting string) string {
	switch {
	case waiting == "permission":
		return "blocked on approval"
	case attention == "needs":
		return "waiting for you"
	case attention == "dismissed":
		return "dismissed"
	case activity == "working":
		return "working"
	default:
		return "idle"
	}
}

// ElapsedString renders time-in-state compactly. Two-digit zero padding on the
// trailing unit keeps the string a stable width so the tabular-figure counter
// does not jitter as it ticks.
func ElapsedString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh %02dm", h, m)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hud/render/ -run 'TestStateWords|TestElapsedString' -v`
Expected: PASS (2 tests, 18 sub-cases)

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/render/format.go internal/hud/render/format_test.go
git commit -m "feat(render): add state-words and elapsed-time formatting"
```

---

### Task 5: Rewrite DrawTab as an antialiased capsule

The core change. `DrawTab`'s signature changes, so both backends' call sites move in this task to keep the build green — but their positioning/animation logic is untouched.

**Heads-up on intermediate state:** after this task the x11 backend will render a black box around each capsule, because a depth-24 window discards alpha. That is expected and is fixed in Task 6. Do not "fix" it by removing the shadow.

**Files:**
- Modify: `internal/hud/render/tab.go` (full rewrite)
- Modify: `internal/hud/render/font.go` (delete `LoadFont`, `loadViaFontconfig`, `loadFromPath`, `parseFont`; keep `fontCandidates` and `FontCandidates`)
- Modify: `internal/hud/render/tab_test.go` (rewrite for new geometry/signature)
- Modify: `internal/hud/x11/tab.go:406-419` (the `render.DrawTab` call), `internal/hud/x11/dock.go`, `internal/hud/x11/help.go`
- Modify: `internal/hud/wlr/surface.go` (the `render.DrawTab` call in `repaint`), `internal/hud/wlr/dock.go`

**Interfaces:**
- Consumes: `Palette`, `StateColors`, `Palette.For`, `Palette.GlyphFG` (Task 1); `Faces` (Task 3); `StateWords`, `ElapsedString` (Task 4).
- Produces:
  - Constants: `CapsuleW = 18`, `ContentH = 44`, `ShadowPad = 5`, `BufH = 54`, `ExpandedW = 300`, `BufW = 305`, `Radius = 10`, `RowPitch = 54`, `AlertProtrusion = 8`, `WobbleAmp = 4.0`, `WobblePeriod = 0.9`
  - `type TabState struct { ... }` (fields in Step 3)
  - `func DrawTab(s TabState, f *Faces, p Palette) TabImage`
  - `TabImage` keeps `RGBA *image.RGBA` and `Overflow bool`
- **Removed:** `TabW`, `TabH`, `TextPad`, `FontPt`, `TextYBaseline`, `ColorFor`, `contrastFG`, `fillDot`, `drawBackgroundDots`, `unpackRGBA` (now internal helper `rgbaOf`), `LoadFont`.

- [ ] **Step 1: Write the failing tests**

Replace the whole of `internal/hud/render/tab_test.go`:

```go
package render

import (
	"testing"
	"time"
)

func silent() Palette { return Theme("silent") }

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/hud/render/ -v`
Expected: FAIL to compile — `undefined: BufW`, `undefined: rgbaOf`, `DrawTab` arity mismatch.

- [ ] **Step 3: Rewrite tab.go**

Replace the entire contents of `internal/hud/render/tab.go`:

```go
package render

import (
	"image"
	"image/color"
	"math"
	"time"

	"github.com/fogleman/gg"
)

// Geometry, shared by both backends. Backends import these; they never
// redeclare the literals.
const (
	// CapsuleW is the visible width of a tab at rest.
	CapsuleW = 18
	// ContentH is the height of the capsule and the expanded panel.
	ContentH = 44
	// ShadowPad is transparent padding reserved for the drop shadow on the
	// left, top and bottom. There is none on the right: that edge is flush
	// with (or past) the screen edge.
	ShadowPad = 5
	// ExpandedW is the visible width of the panel when hovered.
	ExpandedW = 300

	// BufW/BufH are the rendered buffer dimensions.
	BufW = ShadowPad + ExpandedW
	BufH = ShadowPad*2 + ContentH

	// Radius is the corner radius of the capsule and panel. Only the left
	// corners are visibly rounded; the shape is drawn Radius wider than the
	// element so its right corners fall outside the buffer.
	Radius = 10

	// RowPitch is the vertical distance between adjacent tabs. It equals BufH
	// so each tab's shadow lives inside its own buffer and no neighbour clips
	// it — which matters because x11 gives every tab a separate window.
	RowPitch = BufH

	// AlertProtrusion: an attention=needs tab sits this many px further from
	// the screen edge, so it is distinguishable by shape alone. Chosen >
	// WobbleAmp so a needs tab is unambiguously further out than any working
	// tab at its wobble peak.
	AlertProtrusion = 8
	// Wobble animation for working tabs.
	WobbleAmp    = 4.0
	WobblePeriod = 0.9

	// Work-bar: a segmented strip along the capsule's bottom inside edge,
	// replacing the old stacked dots which cramped at this width.
	workSegs   = 3
	workBarH   = 2
	workInset  = 3
	workGap    = 2
	workBottom = 3 // px from the capsule's bottom edge to the bar

	// Text layout inside the panel.
	textPad      = 12 // x offset from the panel's inner edge to the text
	textRightPad = 8  // reserved between text and the panel's far edge
	lineGap      = 2  // vertical gap between the name and meta lines
)

// TabState is everything the renderer needs. Colour is resolved from the
// state fields via the Palette, so backends no longer pass a packed colour.
type TabState struct {
	// Objective state, used for both colour and the panel's state words.
	Activity  string // "working" | "waiting" | "unknown"
	Attention string // "ack" | "needs" | "dismissed"
	Waiting   string // "" | "user" | "permission"

	Glyph string // 1-2 chars centred in the capsule
	Name  string // panel line 1
	Path  string // panel line 2 tail (already $HOME-abbreviated)

	// Elapsed is time in the current state, derived from StateSince by the
	// caller so the renderer stays free of clocks.
	Elapsed time.Duration

	Expanded bool
	// TabRight puts the capsule on the buffer's right edge instead of its
	// left. False (x11): capsule at x=ShadowPad, panel extends rightward.
	// True (wlr): capsule at the right edge, panel extends leftward.
	TabRight bool
	// Shadow enables visor's own drop shadow. Disabled by config when the
	// user prefers their compositor's shadow, or none.
	Shadow bool

	BackgroundRunning int
	BackgroundOutcome string // "" | "done" | "failed"

	// HaloPhase in [0,1) drives the permission pulse. Ignored for states
	// whose StateColors.Glow is false.
	HaloPhase float64
}

// TabImage is the rendered output plus the metadata both backends need.
type TabImage struct {
	RGBA     *image.RGBA
	Overflow bool // Name was wider than the panel could show
}

// capsuleX returns the buffer x of the capsule's left edge.
func capsuleX(tabRight bool) int {
	if tabRight {
		return BufW - CapsuleW
	}
	return ShadowPad
}

// panelRect is the region that is opaque only when expanded.
func panelRect(tabRight bool) image.Rectangle {
	if tabRight {
		return image.Rect(0, ShadowPad, BufW-CapsuleW, ShadowPad+ContentH)
	}
	return image.Rect(ShadowPad+CapsuleW, ShadowPad, BufW, ShadowPad+ContentH)
}

// workBarY is the buffer y of the work bar's top edge.
func workBarY() int {
	return ShadowPad + ContentH - workBottom - workBarH
}

// workSegX is the buffer x of segment i's left edge.
func workSegX(i int, tabRight bool) int {
	cx := capsuleX(tabRight)
	avail := CapsuleW - 2*workInset - (workSegs-1)*workGap
	segW := avail / workSegs
	return cx + workInset + i*(segW+workGap)
}

func workSegW() int {
	avail := CapsuleW - 2*workInset - (workSegs-1)*workGap
	return avail / workSegs
}

// DrawTab renders one tab into a BufW x BufH RGBA buffer.
//
// Layout (x11, TabRight=false):
//
//	0            .. ShadowPad          transparent shadow padding
//	ShadowPad    .. +CapsuleW          the capsule (always opaque)
//	+CapsuleW    .. BufW              the panel (opaque only when Expanded)
//
// `f` may be nil, in which case all text is skipped and Overflow is false.
func DrawTab(s TabState, f *Faces, p Palette) TabImage {
	sc := p.For(s.Activity, s.Attention, s.Waiting)
	dc := gg.NewContext(BufW, BufH)

	cx := float64(capsuleX(s.TabRight))
	cy := float64(ShadowPad)
	ch := float64(ContentH)

	// --- panel (drawn first so the capsule overlaps its edge) ---------------
	if s.Expanded {
		pr := panelRect(s.TabRight)
		px, pw := float64(pr.Min.X), float64(pr.Dx())
		if s.Shadow {
			drawShadow(dc, px, cy, pw, ch)
		}
		// Extend by Radius on the side that runs off-buffer so only the
		// visible corners are rounded.
		rx, rw := px, pw+Radius
		if s.TabRight {
			rx, rw = px-Radius, pw+Radius
		}
		grad := gg.NewLinearGradient(0, cy, 0, cy+ch)
		grad.AddColorStop(0, rgbaOf(p.PanelTop))
		grad.AddColorStop(1, rgbaOf(p.PanelBot))
		dc.DrawRoundedRectangle(rx, cy, rw, ch, Radius)
		dc.SetFillStyle(grad)
		dc.Fill()

		dc.DrawRoundedRectangle(rx+0.5, cy+0.5, rw-1, ch-1, Radius)
		dc.SetColor(rgbaOf(p.PanelBorder))
		dc.SetLineWidth(1)
		dc.Stroke()
	}

	// --- capsule ----------------------------------------------------------
	if s.Shadow {
		drawShadow(dc, cx, cy, float64(CapsuleW), ch)
	}
	if sc.Glow {
		drawHalo(dc, cx, cy, float64(CapsuleW), ch, sc.Halo, s.HaloPhase)
	}

	rx, rw := cx, float64(CapsuleW)+Radius
	if s.TabRight {
		rx, rw = cx-Radius, float64(CapsuleW)+Radius
	}
	grad := gg.NewLinearGradient(0, cy, 0, cy+ch)
	grad.AddColorStop(0, rgbaOf(sc.Top))
	grad.AddColorStop(0.62, rgbaOf(sc.Base))
	grad.AddColorStop(1, rgbaOf(sc.Bot))
	dc.DrawRoundedRectangle(rx, cy, rw, ch, Radius)
	dc.SetFillStyle(grad)
	dc.Fill()

	// Specular hairline inset along the top edge, plus a fainter one on the
	// outer vertical edge. Both are what make the capsule read as an object.
	dc.DrawRoundedRectangle(rx+0.5, cy+0.5, rw-1, ch-1, Radius)
	dc.SetRGBA(1, 1, 1, 0.30)
	dc.SetLineWidth(1)
	dc.Stroke()

	// --- work bar ---------------------------------------------------------
	drawWorkBar(dc, s, p)

	out := TabImage{RGBA: dc.Image().(*image.RGBA)}
	if f == nil {
		return out
	}

	// --- glyph ------------------------------------------------------------
	if s.Glyph != "" {
		dc.SetFontFace(f.Glyph)
		dc.SetColor(rgbaOf(p.GlyphFG(sc.Base)))
		dc.DrawStringAnchored(s.Glyph, cx+float64(CapsuleW)/2, cy+ch/2, 0.5, 0.4)
	}

	// --- panel text -------------------------------------------------------
	if s.Expanded {
		out.Overflow = drawPanelText(dc, s, f, p)
	}
	out.RGBA = dc.Image().(*image.RGBA)
	return out
}

// drawPanelText renders the two panel lines and reports whether the name
// overflowed the available width. Text is clipped to the panel so a long name
// can never bleed into the capsule.
func drawPanelText(dc *gg.Context, s TabState, f *Faces, p Palette) (overflow bool) {
	pr := panelRect(s.TabRight)
	dc.Push()
	dc.DrawRectangle(float64(pr.Min.X), float64(pr.Min.Y), float64(pr.Dx()), float64(pr.Dy()))
	dc.Clip()
	defer dc.Pop()

	textX := float64(pr.Min.X + textPad)
	limit := float64(pr.Max.X - textRightPad)
	if s.TabRight {
		// Panel is on the left; keep text clear of the capsule on the right.
		limit = float64(pr.Max.X - textRightPad)
	}

	// Vertical layout: centre the two-line block in the content height.
	nm := f.Name.Metrics()
	mm := f.Meta.Metrics()
	nameH := float64(nm.Ascent.Ceil() + nm.Descent.Ceil())
	metaH := float64(mm.Ascent.Ceil() + mm.Descent.Ceil())
	blockH := nameH + lineGap + metaH
	top := float64(ShadowPad) + (float64(ContentH)-blockH)/2
	nameBase := top + float64(nm.Ascent.Ceil())
	metaBase := nameBase + float64(nm.Descent.Ceil()) + lineGap + float64(mm.Ascent.Ceil())

	// Line 1: project name.
	dc.SetFontFace(f.Name)
	dc.SetColor(rgbaOf(p.PanelName))
	dc.DrawString(s.Name, textX, nameBase)
	nw, _ := dc.MeasureString(s.Name)
	overflow = textX+nw > limit

	// Line 2: state words (tinted) · elapsed · path.
	sc := p.For(s.Activity, s.Attention, s.Waiting)
	dc.SetFontFace(f.Meta)
	x := textX
	words := StateWords(s.Activity, s.Attention, s.Waiting)
	dc.SetColor(rgbaOf(sc.Base))
	dc.DrawString(words, x, metaBase)
	w, _ := dc.MeasureString(words)
	x += w

	sep := " · "
	dc.SetColor(rgbaOf(p.PanelMeta))
	dc.DrawString(sep, x, metaBase)
	w, _ = dc.MeasureString(sep)
	x += w

	el := ElapsedString(s.Elapsed)
	dc.SetColor(rgbaOf(p.PanelElapsed))
	dc.DrawString(el, x, metaBase)
	w, _ = dc.MeasureString(el)
	x += w

	if s.Path != "" {
		dc.SetColor(rgbaOf(p.PanelMeta))
		dc.DrawString(sep+s.Path, x, metaBase)
	}
	return overflow
}

// drawWorkBar paints the segmented background-work indicator. Running work
// fills that many segments; otherwise a single segment carries the outcome.
// Nothing is drawn when there is neither.
func drawWorkBar(dc *gg.Context, s TabState, p Palette) {
	var fill uint32
	n := 0
	switch {
	case s.BackgroundRunning > 0:
		n = min(s.BackgroundRunning, workSegs)
		fill = p.WorkRunning
	case s.BackgroundOutcome == "done":
		n, fill = 1, p.WorkDone
	case s.BackgroundOutcome == "failed":
		n, fill = 1, p.WorkFailed
	default:
		return
	}
	y := float64(workBarY())
	segW := float64(workSegW())
	for i := 0; i < workSegs; i++ {
		c := p.WorkOff
		if i < n {
			c = fill
		}
		dc.DrawRoundedRectangle(float64(workSegX(i, s.TabRight)), y, segW, workBarH, 1)
		dc.SetColor(rgbaOf(c))
		dc.Fill()
	}
}

// drawShadow paints a blurred dark shape offset 1px down, behind whatever is
// drawn next. The blur is a three-pass box blur, a good enough gaussian
// approximation at this radius; at BufW x BufH the cost is microseconds.
func drawShadow(dc *gg.Context, x, y, w, h float64) {
	sh := gg.NewContext(BufW, BufH)
	sh.DrawRoundedRectangle(x, y+1, w+Radius, h, Radius)
	sh.SetRGBA(0, 0, 0, 0.55)
	sh.Fill()
	dc.DrawImage(boxBlur(sh.Image().(*image.RGBA), 3, 3), 0, 0)
}

// drawHalo paints a soft coloured glow around the capsule, pulsing with phase.
// Only used for the permission state.
func drawHalo(dc *gg.Context, x, y, w, h float64, halo uint32, phase float64) {
	// (1-cos)/2 maps phase to [0,1] with zero derivative at the endpoints, so
	// the pulse eases rather than snapping at its extremes.
	t := (1 - math.Cos(phase*2*math.Pi)) / 2
	alpha := 0.25 + 0.35*t
	c := rgbaOf(halo)
	g := gg.NewContext(BufW, BufH)
	g.DrawRoundedRectangle(x-2, y-2, w+Radius+4, h+4, Radius+2)
	g.SetRGBA(float64(c.R)/255, float64(c.G)/255, float64(c.B)/255, alpha)
	g.Fill()
	dc.DrawImage(boxBlur(g.Image().(*image.RGBA), 4, 2), 0, 0)
}

// boxBlur runs `passes` box-blur passes of the given radius over a
// premultiplied RGBA image.
func boxBlur(src *image.RGBA, r, passes int) *image.RGBA {
	cur := src
	for i := 0; i < passes; i++ {
		cur = boxBlurOnce(cur, r)
	}
	return cur
}

func boxBlurOnce(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// Separable: horizontal pass into tmp, vertical pass into dst. O(n*r)
	// rather than the O(n*r^2) a naive 2D kernel would cost.
	tmp := image.NewRGBA(b)
	dst := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa, n int
			for d := -r; d <= r; d++ {
				px := x + d
				if px < 0 || px >= w {
					continue
				}
				c := src.RGBAAt(px, y)
				sr += int(c.R)
				sg += int(c.G)
				sb += int(c.B)
				sa += int(c.A)
				n++
			}
			tmp.SetRGBA(x, y, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n)})
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa, n int
			for d := -r; d <= r; d++ {
				py := y + d
				if py < 0 || py >= h {
					continue
				}
				c := tmp.RGBAAt(x, py)
				sr += int(c.R)
				sg += int(c.G)
				sb += int(c.B)
				sa += int(c.A)
				n++
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), uint8(sa / n)})
		}
	}
	return dst
}

// rgbaOf converts a packed 0xRRGGBB to an opaque color.RGBA.
func rgbaOf(c uint32) color.RGBA {
	return color.RGBA{
		R: uint8((c >> 16) & 0xff),
		G: uint8((c >> 8) & 0xff),
		B: uint8(c & 0xff),
		A: 0xff,
	}
}
```

Then trim `internal/hud/render/font.go` to just the doc comment, `fontCandidates`, and `FontCandidates()`. Delete `LoadFont`, `loadViaFontconfig`, `loadFromPath`, `parseFont`, and the `freetype-go` import.

- [ ] **Step 4: Update both backends' call sites**

`internal/hud/x11/tab.go` — replace the constant block at lines 37-58 with references to the new names, and the `render.DrawTab` call in `render()`:

```go
const (
	capsuleW  = render.CapsuleW
	contentH  = render.ContentH
	bufW      = render.BufW
	bufH      = render.BufH
	expandedW = render.ExpandedW
)

// Animation constants now come from render so both backends share them.
const (
	wobbleAmp       = render.WobbleAmp
	wobblePeriod    = render.WobblePeriod
	alertProtrusion = render.AlertProtrusion
)
```

In `tab.render()`, replace the `render.DrawTab(...)` call:

```go
	rt := render.DrawTab(render.TabState{
		Activity:          t.sess.Activity,
		Attention:         t.sess.Attention,
		Waiting:           t.sess.Waiting,
		Glyph:             t.sess.Glyph,
		Name:              displayLabel(t.sess),
		Path:              t.sess.DisplayCWD,
		Expanded:          true, // x11 uses positional window-slide
		Shadow:            t.shadow,
		BackgroundRunning: t.sess.BackgroundRunning,
		BackgroundOutcome: t.sess.BackgroundOutcome,
	}, t.faces, t.palette)
```

Add `faces *render.Faces`, `palette render.Palette`, and `shadow bool` fields to `tab`, replacing `font *truetype.Font`. Do the same on `dock` (`internal/hud/x11/dock.go`), have `newDock` call `render.LoadFaces()` and `config.Resolve("", nil)`, and swap every `tabH`/`tabW` reference to `contentH`/`capsuleW` and `dockGap`-based positioning to `render.RowPitch`:

```go
		y := d.mon.y + topMargin + (i+1)*render.RowPitch
```

Delete `colorFor` from `dock.go` and the `swatch` values in `help.go` now come from the palette:

```go
func helpContent(p render.Palette) []helpRow {
	// ... swatch: p.Permission.Base, p.Needs.Base, p.Working.Base, etc.
}
```

`internal/hud/wlr/surface.go` — delete the local `wobbleAmp`, `wobblePeriod`, `alertProtrusion` and `tabGap` constants, aliasing the `render` ones instead, set `tabOverflow = render.AlertProtrusion + int(render.WobbleAmp)`, replace `render.TabH` with `render.ContentH` in `DamageBuffer`/`SetSize`, change `slotTopMargin` to use `render.RowPitch`, and update `repaint`:

```go
	img := render.DrawTab(s.state, d.faces, d.palette)
```

with `s.state.TabRight = true` and `s.state.Shadow` set from config where the state is built in `dock.go`.

- [ ] **Step 5: Run the full test suite**

Run: `go build ./... && go test ./internal/hud/render/ -v`
Expected: PASS — 14 `DrawTab` tests plus the palette/format/faces tests from Tasks 1-4.

- [ ] **Step 6: Look at it**

```bash
go build -o bin/visor ./cmd/visor
./bin/visor hud open --backend=x11
```

Expected: capsules with a **black box** around each one. That is correct at this stage — the x11 window is still depth-24 and discards alpha. Confirm the capsule gradient, glyph, rounded corners and panel-on-hover all look right inside that box.

- [ ] **Step 7: Commit**

```bash
git add internal/hud/render internal/hud/x11 internal/hud/wlr
git commit -m "feat(render): rewrite DrawTab as an antialiased capsule with neutral panel"
```

---

### Task 6: x11 per-pixel alpha

Not unit-testable — it needs a display server. Verification is build + run + look, plus the click test the probe already validated.

**Files:**
- Create: `internal/hud/x11/argb.go`
- Modify: `internal/hud/x11/tab.go` (window creation + upload path), `internal/hud/x11/dock.go` (detect once, pass down)
- Reference: `cmd/visorprobe/main.go` — `newARGBWindow`, `paint`, `setShape`, `reportCompositor`, `reportVisual` are the working originals. Lift, don't reinvent.

**Interfaces:**
- Produces:
  - `func hasCompositor(X *xgbutil.XUtil) bool`
  - `func argbVisual(X *xgbutil.XUtil) (xproto.Visualid, bool)`
  - `func newARGBWindow(X *xgbutil.XUtil, visual xproto.Visualid, x, y, w, h int) (*xwindow.Window, error)`
  - `func uploadRGBA(X *xgbutil.XUtil, win xproto.Window, img *image.RGBA, depth byte) error`
  - `func setInputRegion(X *xgbutil.XUtil, win xproto.Window, rects []xproto.Rectangle) error`

- [ ] **Step 1: Write argb.go**

Lift from `cmd/visorprobe/main.go`, keeping the comments that record the constraints:

```go
package x11

// ARGB support for the x11 backend.
//
// Depth-32 windows REQUIRE an explicit CwBorderPixel and CwColormap;
// inheriting the root's yields BadMatch. xgbutil's xgraphics helper cannot be
// used at all here — its CreatePixmap hardcodes X.Screen().RootDepth and its
// Image is depth-24 by construction — so pixel upload goes through a raw
// depth-32 pixmap + PutImage.
//
// The BOUNDING shape region is deliberately left unset. Shaping it would make
// picom's shadow follow the capsule outline, but a bounding region hard-clips
// rendering and the antialiased corners would become stair-steps. Only the
// INPUT region is shaped, so the transparent shadow padding does not eat
// clicks. Verified on LeftWM + picom: unshaped windows swallow padding
// clicks, input-shaped ones pass them through.
```

Include `hasCompositor` (InternAtom `_NET_WM_CM_S<screen>` + `GetSelectionOwner`, owner != 0), `argbVisual` (scan `AllowedDepths` for `Depth == 32` and `VisualClassTrueColor`), `newARGBWindow` (as in the probe, but returning `*xwindow.Window` so the rest of the backend keeps its current types), `uploadRGBA` (ZPixmap, byte order `B,G,R,A`; `image/RGBA` is already premultiplied, which is what a composited ARGB visual expects), and `setInputRegion` (`shape.RectanglesChecked` with `shape.SoSet`, `shape.SkInput`).

- [ ] **Step 2: Wire it into the dock and tabs**

In `newDock`: call `argbVisual` and `hasCompositor` once, store `visual xproto.Visualid` and `argb bool` on `dock`, where `argb = visualOK && hasCompositor`. Log the fallback exactly once:

```go
	if !d.argb {
		d.log.Info("no compositing manager or ARGB visual; " +
			"falling back to squared corners without shadow")
	}
```

Pass `argb` into `tabOpts`. In `newTab`, branch: `argb` → `newARGBWindow(...)`; else the existing root-depth path. Set `TabState.Shadow = d.cfg.Shadow && d.argb`, and when `!d.argb` also force square corners by rendering with `Radius` effectively zero — add a `Square bool` to `render.TabState` and have `DrawTab` use `0` instead of `Radius` when set.

In `tab.render()`, replace the whole `xgraphics.New` / channel-swap / `XDraw` / `XSurfaceSet` block with:

```go
	depth := byte(32)
	if !t.opt.argb {
		depth = t.X.Screen().RootDepth
	}
	if err := uploadRGBA(t.X, t.win.Id, rt.RGBA, depth); err != nil {
		// Non-fatal: a tab that fails to paint is better than a dead dock.
		return
	}
	if t.opt.argb {
		if err := setInputRegion(t.X, t.win.Id, t.inputRects()); err != nil {
			// Shape is best-effort; without it the padding merely eats clicks.
			_ = err
		}
	}
```

Add `tab.inputRects()` returning the capsule rect when collapsed and capsule + panel when expanded, in window-local coordinates:

```go
// inputRects is the clickable region: never the transparent shadow padding.
func (t *tab) inputRects() []xproto.Rectangle {
	r := []xproto.Rectangle{{
		X: render.ShadowPad, Y: render.ShadowPad,
		Width: render.CapsuleW, Height: render.ContentH,
	}}
	if t.opt.expanded {
		r = append(r, xproto.Rectangle{
			X: render.ShadowPad + render.CapsuleW, Y: render.ShadowPad,
			Width: render.ExpandedW - render.CapsuleW, Height: render.ContentH,
		})
	}
	return r
}
```

Call `setInputRegion` again from `setExpanded` so the panel becomes clickable on hover. Delete the now-unused `expandedImg *xgraphics.Image` field and its `Destroy()` calls.

- [ ] **Step 3: Build and look**

```bash
go build -o bin/visor ./cmd/visor && ./bin/visor hud open --backend=x11
```

Expected: capsules with **transparent** padding, antialiased rounded corners, and a soft shadow. There will be a **second, rectangular shadow** from picom offset up-and-left until Step 4.

- [ ] **Step 4: Verify the click behaviour and add the picom snippet**

Middle-click a capsule → the session is dismissed. Click the transparent padding beside a capsule → the click reaches the desktop, not the tab.

Then add the `shadow-exclude` line to `~/.config/picom.conf`:

```
shadow-exclude = [ ..., "name *= 'visor'" ]
```

Restart picom and confirm only visor's own shadow remains. Ensure tab windows are named so the match works — `ewmh.WmNameSet(X, win.Id, "visor-tab")` in `newTab`.

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/x11
git commit -m "feat(hud/x11): per-pixel alpha via depth-32 visual and XShape input region"
```

---

### Task 7: StateSince, DisplayName and Glyph in the daemon

**Files:**
- Modify: `internal/state/state.go` (`Session`, `Snapshot`, `Snapshot()`, `ApplyTranscript`), `internal/state/hooks.go`, `internal/state/notify.go`
- Test: `internal/state/state_test.go` (create)

**Interfaces:**
- Produces on `Snapshot`: `StateSince time.Time \`json:"state_since"\``, `DisplayName string \`json:"display_name"\``, `Glyph string \`json:"glyph"\``
- `func projectName(title, cwd, id string) string`
- `func assignGlyphs(snaps []Snapshot)` — mutates `Glyph` in place with collision disambiguation.

- [ ] **Step 1: Write the failing tests**

```go
package state

import (
	"testing"
	"time"

	"github.com/nitzanz/visor/internal/transcript"
)

func TestProjectName(t *testing.T) {
	cases := []struct{ title, cwd, id, want string }{
		{"my-feature", "/home/n/Projects/visor", "abcdef12", "my-feature"},
		{"", "/home/n/Projects/visor", "abcdef12", "visor"},
		{"", "", "abcdef1234", "abcdef12"},
		{"", "/", "abcdef12", "abcdef12"},
	}
	for _, c := range cases {
		if got := projectName(c.title, c.cwd, c.id); got != c.want {
			t.Errorf("projectName(%q,%q,%q) = %q, want %q",
				c.title, c.cwd, c.id, got, c.want)
		}
	}
}

// A single initial is enough until two live sessions collide, at which point
// BOTH must widen — otherwise the user sees two identical glyphs.
func TestAssignGlyphs_DisambiguatesCollisions(t *testing.T) {
	snaps := []Snapshot{
		{DisplayName: "visor"},
		{DisplayName: "deepdub"},
		{DisplayName: "dotfiles"},
	}
	assignGlyphs(snaps)
	if snaps[0].Glyph != "V" {
		t.Errorf("unique initial glyph = %q, want \"V\"", snaps[0].Glyph)
	}
	if snaps[1].Glyph != "DE" {
		t.Errorf("colliding glyph = %q, want \"DE\"", snaps[1].Glyph)
	}
	if snaps[2].Glyph != "DO" {
		t.Errorf("colliding glyph = %q, want \"DO\"", snaps[2].Glyph)
	}
}

func TestAssignGlyphs_EmptyNameYieldsEmptyGlyph(t *testing.T) {
	snaps := []Snapshot{{DisplayName: ""}}
	assignGlyphs(snaps)
	if snaps[0].Glyph != "" {
		t.Errorf("glyph for empty name = %q, want \"\"", snaps[0].Glyph)
	}
}

// StateSince must move on an activity transition and stay put otherwise —
// this is what makes the HUD's elapsed counter meaningful and what keeps it
// out of the broadcast digest.
func TestStateSince_MovesOnlyOnTransition(t *testing.T) {
	s := NewStore()
	sess := s.UpsertByPath("/tmp/x.jsonl")
	sess.Activity = transcript.ActivityWorking
	sess.StateSince = time.Now().Add(-time.Hour)
	before := sess.StateSince

	// Same activity again → StateSince unchanged.
	s.ApplyTranscript("/tmp/x.jsonl", []transcript.Line{
		{Type: "assistant", StopReason: "tool_use"},
	}, 10, false)
	if got, _ := s.Get(sess.ID); !got.StateSince.Equal(before) {
		t.Errorf("StateSince moved without a transition: %v → %v", before, got.StateSince)
	}

	// Transition to waiting → StateSince updates.
	s.ApplyTranscript("/tmp/x.jsonl", []transcript.Line{
		{Type: "assistant", StopReason: "end_turn"},
	}, 20, false)
	if got, _ := s.Get(sess.ID); !got.StateSince.After(before) {
		t.Errorf("StateSince did not move on transition: still %v", got.StateSince)
	}
}

// Regression guard for the digest rule in CLAUDE.md.
func TestHudDigest_IgnoresStateSince(t *testing.T) {
	a := []Snapshot{{ID: "x", Activity: "working", StateSince: time.Now()}}
	b := []Snapshot{{ID: "x", Activity: "working", StateSince: time.Now().Add(time.Hour)}}
	if hudDigest(a) != hudDigest(b) {
		t.Errorf("digest changed with StateSince; it must be excluded")
	}
}

func TestHudDigest_IncludesGlyph(t *testing.T) {
	a := []Snapshot{{ID: "x", Glyph: "V"}}
	b := []Snapshot{{ID: "x", Glyph: "VI"}}
	if hudDigest(a) == hudDigest(b) {
		t.Errorf("digest ignored Glyph; it must be included")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run 'TestProjectName|TestAssignGlyphs|TestStateSince|TestHudDigest' -v`
Expected: FAIL — `undefined: projectName`, `undefined: assignGlyphs`, unknown field `StateSince`.

- [ ] **Step 3: Implement**

Add to `Session` (near `LastWaiting`):

```go
	// StateSince is when (Activity, Waiting) last changed. Attention changes
	// do NOT move it: the HUD renders "time in current state", and dismissing
	// a session is not a state change in that sense.
	//
	// Deliberately NOT in the broadcast digest, and it does not need to be —
	// it only moves when a digested field moves. Rendering elapsed from
	// LastUpdate instead would freeze the counter, because a transcript
	// append to an already-working session changes no digested field and so
	// fires no broadcast.
	StateSince time.Time `json:"state_since"`
```

Add to `Snapshot`:

```go
	DisplayName string    `json:"display_name"`
	Glyph       string    `json:"glyph"`
	StateSince  time.Time `json:"state_since"`
```

In `ApplyTranscript`, inside `if sess.Activity != prev {`, add `sess.StateSince = time.Now()` as the first line. Do the same wherever `hooks.go` changes `Activity` or `Waiting`. Initialise `StateSince` to `FirstSeen` when a session is created.

Add the helpers:

```go
// projectName is what the HUD shows on the panel's first line: the session
// title if Claude or the user set one, else the cwd's basename, else a short
// id. Computed server-side so backends stay logic-free.
func projectName(title, cwd, id string) string {
	if title != "" {
		return title
	}
	if base := filepath.Base(cwd); cwd != "" && base != "." && base != "/" {
		return base
	}
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// assignGlyphs fills Glyph with a 1-2 character project identifier. A single
// uppercase initial is used unless two live sessions collide on it, in which
// case every colliding session widens to two letters — a lone widened glyph
// would be more confusing than helpful.
func assignGlyphs(snaps []Snapshot) {
	counts := map[string]int{}
	for _, s := range snaps {
		if s.DisplayName == "" {
			continue
		}
		counts[strings.ToUpper(s.DisplayName[:1])]++
	}
	for i := range snaps {
		n := snaps[i].DisplayName
		if n == "" {
			continue
		}
		first := strings.ToUpper(n[:1])
		if counts[first] > 1 && len(n) > 1 {
			snaps[i].Glyph = strings.ToUpper(n[:2])
			continue
		}
		snaps[i].Glyph = first
	}
}
```

In `Store.Snapshot()`, set `DisplayName: projectName(sess.resolvedTitle(), sess.CWD, sess.ID)` and `StateSince: sess.StateSince` in the literal, then call `assignGlyphs(out)` **after the sort** and before returning.

In `hudDigest`, add `Glyph` (and nothing else):

```go
		h.Write([]byte(s.Glyph))
		h.Write([]byte{0})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state/ -v`
Expected: PASS, including the pre-existing `background_test.go` and `hooks_test.go`.

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/state
git commit -m "feat(state): add StateSince, DisplayName and disambiguated Glyph"
```

---

### Task 8: Wire the panel's second line through both backends

**Files:**
- Modify: `internal/hud/x11/subscribe.go`, `internal/hud/x11/tab.go`, `internal/hud/x11/dock.go`
- Modify: `internal/hud/wlr/subscribe.go`, `internal/hud/wlr/dock.go`

**Interfaces:**
- Consumes: `Snapshot.DisplayName`, `Snapshot.Glyph`, `Snapshot.StateSince` (Task 7); `TabState.Elapsed` (Task 5).

- [ ] **Step 1: Extend both sessionView structs**

In `internal/hud/x11/subscribe.go` and `internal/hud/wlr/subscribe.go` add:

```go
	DisplayName string    `json:"display_name"`
	Glyph       string    `json:"glyph"`
	StateSince  time.Time `json:"state_since"`
```

- [ ] **Step 2: Feed them into TabState**

In `x11/tab.go`'s `render()`, replace `Name: displayLabel(t.sess)` with `Name: t.sess.DisplayName`, keep `displayLabel` only as the fallback when `DisplayName` is empty, and add:

```go
		Glyph:   t.sess.Glyph,
		Elapsed: time.Since(t.sess.StateSince),
```

Do the same where `wlr/dock.go` builds `render.TabState`.

- [ ] **Step 3: Re-render on the animation tick so the counter ticks**

The elapsed string changes once a second with no daemon broadcast. In `x11/dock.go`'s `animate`, re-render any **expanded** tab whose rendered elapsed string has changed:

```go
// tickElapsed re-renders an expanded tab when its elapsed label would change.
// Collapsed tabs show no text, so they never need this — which keeps the
// steady-state cost at zero when nothing is hovered.
func (t *tab) tickElapsed() {
	if !t.opt.expanded {
		return
	}
	s := render.ElapsedString(time.Since(t.sess.StateSince))
	if s == t.lastElapsed {
		return
	}
	t.lastElapsed = s
	t.render()
}
```

Add `lastElapsed string` to `tab` and call `tickElapsed()` from `dock.animate`. Mirror this in `wlr`'s `animateTick` by marking the surface dirty and repainting.

Also drive the permission halo from the same tick: set `TabState.HaloPhase = math.Mod(now.Sub(start).Seconds()/1.6, 1.0)` for sessions whose state has `Glow`, and re-render when the phase changes enough to matter (quantise to ~30 steps to avoid a repaint per frame).

- [ ] **Step 4: Build and look**

```bash
go build -o bin/visor ./cmd/visor && ./bin/visor hud open --backend=x11
```

Expected: hovering a tab shows `deepdub-platform` on line 1 and e.g. `waiting for you · 4m 12s · ~/P/deepdub` on line 2, with the seconds counting up. A permission-blocked tab pulses.

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/x11 internal/hud/wlr
git commit -m "feat(hud): render project name, state words and time-in-state"
```

---

### Task 9: Live theme switching

**Files:**
- Modify: `cmd/visor/hud.go` (add `theme`/`shadow` subcommands and `--theme`/`--shadow` flags)
- Modify: `internal/hud/x11/dock.go`, `internal/hud/wlr/dock.go` (watch the config file)
- Create: `internal/hud/config/watch.go`
- Test: `internal/hud/config/watch_test.go`

**Interfaces:**
- Produces: `func Watch(ctx context.Context, out chan<- Config, log *slog.Logger) error`

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"context"
	"log/slog"
	"io"
	"testing"
	"time"
)

func TestWatch_EmitsOnChange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Config{Theme: "silent", Shadow: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Config, 4)
	go func() {
		_ = Watch(ctx, ch, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	// Give the watcher time to register before mutating the file.
	time.Sleep(100 * time.Millisecond)
	if err := Save(Config{Theme: "traffic", Shadow: false}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.Theme != "traffic" || got.Shadow {
			t.Errorf("watched config = %+v, want {traffic false}", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no config event within 3s")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hud/config/ -run TestWatch -v`
Expected: FAIL — `undefined: Watch`

- [ ] **Step 3: Implement Watch**

Watch the config file's **parent directory**, not the file — editors and `os.WriteFile` replace inodes, and a file watch goes stale. Filter events to `Path()`, debounce 80ms, and re-`Load()` on each event.

- [ ] **Step 4: Add the CLI surface**

In `cmd/visor/hud.go`, add `--theme` (string, default `""` = unset) and `--shadow` (parsed via a `*bool` so "unset" is distinguishable), pass `config.Resolve(...)` into the backend, and add two subcommands:

```go
	case "theme":
		if fs.NArg() != 1 {
			fmt.Fprintf(os.Stderr, "hud theme: want one of: %s\n",
				strings.Join(render.Themes(), ", "))
			os.Exit(2)
		}
		name := fs.Arg(0)
		if _, ok := render.ThemeByName(name); !ok {
			fmt.Fprintf(os.Stderr, "hud theme: unknown theme %q (have: %s)\n",
				name, strings.Join(render.Themes(), ", "))
			os.Exit(2)
		}
		c := config.Load()
		c.Theme = name
		if err := config.Save(c); err != nil {
			fmt.Fprintln(os.Stderr, "hud theme:", err)
			os.Exit(1)
		}
		fmt.Printf("theme = %s (written to %s)\n", name, config.Path())
```

and an equivalent `shadow on|off`.

- [ ] **Step 5: Consume config changes in both docks**

Start `config.Watch` in `dock.run`, add its channel to the `select`, and on receipt store the new palette/shadow and re-render every tab. In x11 that is a loop calling `t.render()`; in wlr, mark every surface dirty and repaint. A `--theme` flag pins the theme: skip starting the watcher when it was set.

- [ ] **Step 6: Verify live switching**

```bash
go build -o bin/visor ./cmd/visor && ./bin/visor hud open --backend=x11 &
./bin/visor hud theme traffic   # dock recolours without restarting
./bin/visor hud theme silent
./bin/visor hud shadow off      # visor's own shadow disappears
```

- [ ] **Step 7: Commit**

```bash
go build ./... && go test ./...
git add cmd/visor internal/hud
git commit -m "feat(hud): live theme and shadow switching via watched config file"
```

---

### Task 10: Retire eww, auto-detect the backend, delete the probe

**Files:**
- Delete: `internal/hud/eww/` (whole directory), `cmd/visorprobe/`
- Modify: `cmd/visor/hud.go`, `internal/hud/hud.go` (doc comment), `internal/hud/x11/x11.go` (`Install` prints the picom snippet), `CLAUDE.md`

- [ ] **Step 1: Delete eww and rewire pickBackend**

```go
// pickBackend resolves a backend name to an implementation. An empty name
// auto-detects: a Wayland session with layer-shell gets the wlr backend,
// everything else gets x11 (which also covers GNOME via XWayland, since
// GNOME has no layer-shell).
func pickBackend(name string) (hud.Backend, error) {
	switch name {
	case "":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return wlr.New(), nil
		}
		return x11.New(), nil
	case "x11":
		return x11.New(), nil
	case "wlr":
		return wlr.New(), nil
	case "eww":
		return nil, errors.New("the eww backend was removed; use --backend=x11 or --backend=wlr")
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}
```

Change the flag default from `"eww"` to `""` and its help text to `HUD backend (x11|wlr; default: auto-detect)`. An explicit `eww` gets a real explanation rather than "unknown backend".

- [ ] **Step 2: Print the picom snippet from x11 Install**

```go
func (b *Backend) Install() (string, error) {
	return `x11 backend is built into visor; nothing to install.
Run ` + "`visor hud open`" + ` (or --backend=x11 explicitly).

If you use picom with shadows enabled, add visor to shadow-exclude so its
own shadow isn't doubled by picom's rectangular one:

  shadow-exclude = [ "name *= 'visor'" ]

Alternatively, turn visor's own shadow off with ` + "`visor hud shadow off`" + `.
`, nil
}
```

- [ ] **Step 3: Delete the probe**

```bash
git rm -r cmd/visorprobe
```

`gg` and `x/image` stay direct dependencies — `render` consumes both now. Confirm with `go mod tidy && git diff --stat go.mod` showing no change.

- [ ] **Step 4: Update CLAUDE.md**

- Remove `eww` from the Build & run backend list; document auto-detect.
- Replace the eww paragraph in **HUD backends** with the two-backend description and the new geometry.
- Rewrite the digest note to mention `Glyph` is included while `LastUpdate`/`StateSince` are not.
- Keep the "Eww has no input-shape option" gotcha but reframe it as the historical reason the x11 backend exists.
- Replace it in **Things that will bite you** with the three new ones: depth-32 windows need explicit border pixel + colormap; `xgraphics` cannot do depth 32; a bounding shape region would kill the antialiased corners.
- Drop the eww line from **Conventions**.
- Delete the "No Wayland backend yet" item from **Pending / known WIP** (stale — wlr exists).

- [ ] **Step 5: Full verification**

```bash
go build ./... && go test ./... && go vet ./...
grep -rn "eww" --include=*.go . ; echo "expected: only the removal message in hud.go"
./bin/visor hud install
./bin/visor hud open
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(hud): retire eww backend, auto-detect x11/wlr, drop the probe"
```

---

### Task 11: Golden image tests

Last, so the goldens are generated from settled pixels rather than regenerated every task.

**Files:**
- Create: `internal/hud/render/golden_test.go`, `internal/hud/render/testdata/*.png`

- [ ] **Step 1: Write the golden test with an update flag**

```go
package render

import (
	"bytes"
	"flag"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden images")

func TestGolden(t *testing.T) {
	f, err := LoadFaces()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	cases := []struct {
		name  string
		state TabState
	}{
		{"collapsed-permission", TabState{
			Activity: "waiting", Attention: "needs", Waiting: "permission",
			Glyph: "V", Shadow: true,
		}},
		{"collapsed-working-bgwork", TabState{
			Activity: "working", Attention: "ack",
			Glyph: "X", Shadow: true, BackgroundRunning: 2,
		}},
		{"collapsed-noshadow", TabState{
			Activity: "waiting", Attention: "ack", Glyph: "N",
		}},
		{"expanded-needs", TabState{
			Activity: "waiting", Attention: "needs", Waiting: "user",
			Glyph: "D", Name: "deepdub-platform", Path: "~/P/deepdub",
			Elapsed: 4*time.Minute + 12*time.Second, Expanded: true, Shadow: true,
		}},
		{"expanded-tabright", TabState{
			Activity: "working", Attention: "ack",
			Glyph: "V", Name: "visor", Path: "~/Projects/visor",
			Elapsed: 12 * time.Second, Expanded: true, Shadow: true, TabRight: true,
		}},
	}
	for _, theme := range Themes() {
		for _, c := range cases {
			name := theme + "-" + c.name
			t.Run(name, func(t *testing.T) {
				img := DrawTab(c.state, f, Theme(theme))
				var buf bytes.Buffer
				if err := png.Encode(&buf, img.RGBA); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join("testdata", name+".png")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("missing golden %s (run: go test ./internal/hud/render -update)", path)
				}
				if !bytes.Equal(want, buf.Bytes()) {
					t.Errorf("rendered output differs from %s; inspect it, then "+
						"re-run with -update if the change is intended", path)
				}
			})
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/hud/render/ -run TestGolden -v`
Expected: FAIL — `missing golden testdata/silent-collapsed-permission.png` (15 subtests).

- [ ] **Step 3: Generate the goldens**

Run: `go test ./internal/hud/render/ -run TestGolden -update`

- [ ] **Step 4: Eyeball them, then confirm they pass**

Open a few of the PNGs and confirm they look like the approved design — particularly `silent-expanded-needs` (neutral panel, amber capsule, two text lines) and `traffic-collapsed-working-bgwork` (dark work segments on the mint capsule).

Run: `go test ./internal/hud/render/ -run TestGolden -v`
Expected: PASS (15 subtests)

- [ ] **Step 5: Commit**

```bash
go build ./... && go test ./...
git add internal/hud/render/golden_test.go internal/hud/render/testdata
git commit -m "test(render): golden images for every theme and tab configuration"
```

---

## Self-Review

**Spec coverage** — every section maps to a task:

| Spec section | Task |
| --- | --- |
| Geometry | 5 |
| The capsule (gradient, hairline, shadow, halo) | 5 |
| Glyph + server-side disambiguation | 7 |
| Background work micro-bar | 5 |
| Expanded panel, state words, elapsed | 4, 5, 8 |
| Palette token set + three themes | 1 |
| Rendering stack (gg, opentype, blur) | 3, 5 |
| x11 ARGB, PutImage, compositor detection, XShape | 6 |
| picom `shadow-exclude` snippet | 10 |
| wlr geometry | 5 |
| Config, precedence, live reload, subcommands | 2, 9 |
| `StateSince`, `display_name`, `glyph` in digest | 7 |
| Retiring eww, auto-detect | 10 |
| Testing (tokens, salience, geometry, goldens) | 1, 5, 11 |
| Delete the probe | 10 |

**Gaps found and fixed while reviewing:**
- The spec's fallback for "no compositor" says squared corners; that needed a mechanism, so `TabState.Square` was added in Task 6, Step 2.
- Nothing initialised `StateSince` for newly created sessions — added to Task 7, Step 3.
- The elapsed counter needs a repaint driver independent of daemon broadcasts; that is Task 8, Step 3, and it is the reason `tickElapsed` only fires for expanded tabs.

**Type consistency:** `Faces`/`LoadFaces` (Task 3) match the `f *Faces` parameter in Task 5. `Palette`/`Theme`/`ThemeByName`/`For`/`GlyphFG` (Task 1) match every later call. `Config`/`Load`/`Resolve`/`Save`/`Path`/`Watch` (Tasks 2, 9) are consistent. `workSegX`/`workBarY`/`workSegW`/`rgbaOf` are defined in Task 5 and used by its own tests only.

**Known intermediate breakage, deliberate and documented:** after Task 5 the x11 dock renders a black box per tab until Task 6 lands. Called out in Task 5's heads-up and Step 6.

**Verification limit worth stating plainly:** the wlr backend cannot be visually verified on this machine — it is LeftWM on X11, and wlr needs a layer-shell Wayland compositor. Its changes in Tasks 5, 8 and 9 are build- and review-verified only, and should be exercised on a sway/Niri box before any release.
