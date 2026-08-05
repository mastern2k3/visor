# HUD visual redesign — capsule tabs, palette themes, x11 alpha

Date: 2026-08-05
Status: approved, ready for planning

## Why

The HUD works but looks crude. The diagnosis is not the language: it is that
`internal/hud/render/tab.go` is a ~250-line hand-rolled pixel layer standing in
for a 2D graphics library. It composes solid `image.Uniform` rectangles, hand-rolls
circles with an integer distance test under an explicit `// No anti-aliasing`
comment, and draws text through `BurntSushi/freetype-go` (a 2016 fork). That layer
has no concept of a path, so rounded corners, gradients, shadows and antialiased
edges are not hard — they are inexpressible.

Two secondary problems compound it:

- **A 10px flat sliver has no surface to be beautiful on.** Antialiasing alone
  makes it cleaner, not better.
- **The palette has a salience inversion.** Rec.601 luminance of today's colours:
  `needs` amber `#ebcb8b` = 205, `permission` red `#ff7a7a` = 161, `working`
  cyan `#88c0d0` = 176. The less-urgent state is the loudest thing on screen, and
  a session that needs nothing outshines both greys. The five colours also come
  from three unrelated sources (Nord, Tailwind gray-500, ad hoc).

## Goals

1. Raise craft: antialiased rounded capsules with gradient, hairline and shadow.
2. Widen the resting tab to 18px so there is canvas for identity and state.
3. Make the palette a coherent, themeable token set with a correct salience order.
4. Give the expanded panel enough room to explain itself in words.
5. Collapse to one rendering path and one colour source of truth.

## Non-goals

- Unifying x11's positional window-slide onto wlr's render-alpha model. Alpha
  makes it possible; it stays a separate change. The slide is cheaper to animate.
- Action buttons in the expanded panel. Valuable, but they would be the first
  thing in this codebase to need sub-tab hit regions. Separable follow-up.
- Backdrop blur. There is no standard Wayland blur protocol, and picom's
  `blur-background-exclude` already excludes `window_type = 'dock'`.
- GTK4/QML. Both cost cgo or a second runtime and would break the single static
  binary the project is built around.

## Validated on hardware

A throwaway probe (`cmd/visorprobe`) was run on the target machine — LeftWM on
X11 with picom (`backend = "glx"`, `vsync = false`, `shadow = true`,
`wintypes: dock = { shadow = true }`). Results:

| check | result |
| --- | --- |
| `_NET_WM_CM_S0` selection owner | present under picom — compositor detection is sound |
| depth-32 TrueColor visual | present (`0x203`, masks `r=ff0000 g=ff00 b=ff`) |
| SHAPE extension | v1.1 |
| 4 ARGB override-redirect dock windows + depth-24 control | mapped, zero `BadMatch` |
| LeftWM interference | none — override-redirect windows are never tiled |
| transparency | confirmed by eye: padding shows the desktop through it |
| picom's dock shadow | drawn around the *rectangular* window bounds, padding included |
| own in-buffer shadow + picom's | double shadow, as predicted |
| XShape input region | clicks on padding pass through (V4); unshaped variants swallow them (V1/V2: 17 padding hits logged) |
| 30 Hz ARGB window-move | 6 s clean, no X errors |
| `gg` output | antialiased corners, gradient, hairline, glyph all correct |

Two findings that constrain the implementation:

- **`xgbutil/xgraphics` cannot be reused.** `CreatePixmap` hardcodes
  `X.Screen().RootDepth` and its `Image` is depth-24 by construction. The x11
  backend needs a raw depth-32 pixmap + `PutImage` path. The probe has a working
  one; lift it.
- **picom cannot draw the shadow.** Its shadow follows the XShape *bounding*
  region. Making that region rounded (buildable as ~44 scanline rects) would get
  a correctly shaped shadow, but a bounding region hard-clips rendering, so the
  antialiased corners become stair-steps — discarding the main win of the whole
  change. Therefore visor draws its own shadow and asks to be excluded from
  picom's.

## Geometry

| | today | new |
| --- | --- | --- |
| capsule width, collapsed | `TabW = 10` | `CapsuleW = 18` |
| content height | `TabH = 36` | `ContentH = 44` |
| buffer height | 36 | `BufH = 54` (44 + 5px shadow pad top and bottom) |
| expanded width | `ExpandedW = 300` | 300, unchanged |
| row pitch | 36+8 (x11), 36+4 (wlr) | `54` — one pitch for both backends |
| left-corner radius | none | `10` |
| `needs` protrusion | 8 | 8, unchanged |
| wobble amplitude / period | 4.0 / 0.9s | unchanged |
| shadow pad | n/a | `5` left, top, bottom (none right — that edge is offscreen) |

Pitch equals buffer height deliberately: shadow padding lives inside each tab's
own buffer, so adjacent tabs never overlap and no neighbour clips another's
shadow. This matters because x11 gives every tab a separate window.

When `shadow = false` in config, the pad is still allocated (keeping one pitch
and one buffer size across configurations) but left fully transparent.

## The capsule

- Left-rounded pill, radius 10. The right-hand corners stay square: the shape is
  drawn `CapsuleW + radius` wide so the right corners fall outside the buffer,
  matching a capsule flush to the screen edge.
- Vertical three-stop gradient: `top` at 0.0, `base` at 0.62, `bot` at 1.0.
- 1px specular hairline stroked inset along the top edge, ~30% white.
- 1px lighter inset on the left edge, ~10% white.
- Soft outer drop shadow, offset +1px down, ~55% black, blurred ~3px.
- `permission` is the only state with an animated halo pulse. `working` keeps the
  wobble; `needs` keeps the protrusion. No other state animates.

### Glyph

A project identifier centred in the capsule. One uppercase initial from the
project name; **if another live session shares that initial, every colliding
session shows two letters instead.** Computed in the store and shipped as a
`glyph` field on the snapshot.

`glyph` **does** belong in the broadcast digest: it is HUD-observable and only
changes when sessions appear or disappear, which already changes the snapshot.
This does not violate the digest rule in `CLAUDE.md`, which is about
high-frequency fields such as `LastUpdate`.

### Background work

Replaces today's stacked dots (`fillDot`, `drawBackgroundDots`) with a
**segmented 2px micro-bar** along the bottom inside edge of the capsule, inset
3px each side. Segments read cleanly at 18px where stacked circles cramp.
Segment count matches today's `dotMaxRunning = 3` cap. Running segments use the
theme's `WorkRunning`; unfilled segments use `WorkOff`. When
`BackgroundRunning == 0` and an outcome is set, a single segment is filled with
`WorkDone` or `WorkFailed`.

## Expanded panel

Neutral dark panel, 300 x 44, left-rounded radius 10, 1px hairline border, own
drop shadow. The capsule keeps the colour and stays seated against the panel's
right edge.

**Today the entire 300px panel is filled with the state colour** — a solid amber
or red block with dark text. It becomes neutral, and the capsule alone carries
colour. Text legibility rises sharply and a red session stops flooding a third of
the screen edge on hover.

- **Line 1** — project name, ~12.5pt, `PanelName` colour, full brightness.
- **Line 2** — state in words tinted with the state colour, then elapsed, then
  abbreviated path: `waiting for you · 4m 12s · ~/P/deepdub`.

State words:

| condition | text |
| --- | --- |
| `waiting == permission` | `blocked on approval` |
| `attention == needs` | `waiting for you` |
| `attention == dismissed` | `dismissed` |
| `activity == working` | `working` |
| otherwise | `idle` |

Elapsed format: `12s` under a minute, `4m 12s` under an hour, `1h 04m` beyond.
Rendered with tabular figures so the counter does not jitter.

Baselines are derived from opentype metrics, replacing
`TextYBaseline = 24 // empirically matched`. Existing overflow/tooltip behaviour
is preserved: if line 1 exceeds the panel width, hovering still spawns the
tooltip window.

## Palette as a complete token set

New file `internal/hud/render/palette.go`. `ColorFor` becomes a method on
`Palette`; the current package-level switch statement is removed.

`Palette` carries **every** colour decision, not just the five states:

- per state (`permission`, `needs`, `working`, `ack`, `dismissed`): `Top`,
  `Base`, `Bot`, `Halo`, `Glow bool`
- `WorkRunning`, `WorkDone`, `WorkFailed`, `WorkOff`
- `PanelTop`, `PanelBot`, `PanelBorder`, `PanelName`, `PanelMeta`, `PanelElapsed`
- `GlyphLumThreshold` — replaces the magic `140` in `contrastFG`
- `GlyphDark`, `GlyphLight`

If the struct held only five hues, the result would be themed capsules bolted to
a hardcoded everything-else, and `silent` in particular would break — it only
works if the panel and work-bar are re-tuned with it.

### Themes

Registry keyed by name. Values below are the approved mockup values.

**`tuned`** — same hue family as today, ordering fixed. Red becomes the
brightest, most saturated element and gains a halo; amber drops below it; cyan is
dimmed and desaturated so "busy, needs nothing" stops shouting.

| state | top | base | bot |
| --- | --- | --- | --- |
| permission | `#ff8a7d` | `#ff5a4f` | `#e04338` (halo `#ff5a4f` @ 55%, glow) |
| needs | `#f7c95e` | `#e8ad2e` | `#c9911c` |
| working | `#7fa8bd` | `#5e8fa8` | `#4a7690` |
| ack | `#5b6472` | `#4a5260` | `#3c4351` |
| dismissed | `#333949` | `#2c3140` | `#242936` |

**`silent`** (default) — colour spent only on "a human is needed". Working, ack
and dismissed are neutrals separated by luminance alone; activity is carried by
the wobble and the work-bar, not hue.

| state | top | base | bot |
| --- | --- | --- | --- |
| permission | `#ff8a7d` | `#ff4d43` | `#dd3a30` (halo `#ff4d43` @ 60%, glow) |
| needs | `#ffc86b` | `#ffa826` | `#e08b12` |
| working | `#49525f` | `#3d4653` | `#333b47` |
| ack | `#3d434e` | `#343a45` | `#2c313b` |
| dismissed | `#2b303a` | `#252a33` | `#1f242b` |

**`traffic`** — saturation pushed everywhere; working goes vivid mint so "the
machine is alive" is legible across the room. Best raw glanceability, worst at
receding.

| state | top | base | bot |
| --- | --- | --- | --- |
| permission | `#ff7b6e` | `#f43f31` | `#d22a1d` (halo `#f43f31` @ 60%, glow) |
| needs | `#ffd24d` | `#fbbf24` | `#e0a509` |
| working | `#7fe3d0` | `#34d3b4` | `#1fb598` |
| ack | `#8b95a5` | `#71809a` | `#5c6a82` |
| dismissed | `#454c5c` | `#3a4150` | `#2f3541` |

Shared panel defaults, overridable per theme: `PanelTop` `#1a1e27` @ 97%,
`PanelBot` `#12151c` @ 97%, `PanelBorder` white @ 10%, `PanelName` `#e8eef6`,
`PanelMeta` `#8b95a6`, `PanelElapsed` `#7b8595`. Work-bar defaults:
`WorkRunning` `#8be0d0`, `WorkDone` `#a3d977`, `WorkFailed` `#ff7a7a`, `WorkOff`
black @ 28%. `traffic` overrides `WorkRunning` to `#0d3b33` for contrast against
its light working capsule. `GlyphLumThreshold` 140, `GlyphDark` `#10141c`,
`GlyphLight` `#e5e9f0` — the last two matching today's `contrastFG` returns.

## Rendering stack

- **Shapes:** `github.com/fogleman/gg` — rounded rects, linear gradients,
  clipping, stroke/fill, antialiased. Pure Go, no cgo, single static binary
  intact.
- **Text:** `golang.org/x/image/font/opentype` + `sfnt`, fed to gg via
  `SetFontFace`. **Drops the direct `BurntSushi/freetype-go` dependency.**
  `BurntSushi/graphics-go` remains as an indirect dep — `xgbutil/xgraphics`
  needs it. Font discovery in `render/font.go` keeps its existing candidate list.
- **Shadow:** pure-Go blur over the tab buffer. At 305x54 the cost is
  microseconds and the 30 Hz loop will not notice. The probe used three box-blur
  passes; the implementation should use a proper stack blur.

`fillDot` and its integer distance test are deleted.

`DrawTab` keeps its current signature shape — `TabState` in, `TabImage` out,
`Overflow` preserved — plus the `Palette` and a `Shadow bool`. Both backends keep
calling one function, so the two cannot drift.

## x11 backend

1. **ARGB window.** Depth-32 visual + explicit colormap per tab window.
   Depth-32 windows *require* `CwBorderPixel` and `CwColormap`; inheriting the
   root's yields `BadMatch`.
2. **Raw pixmap upload.** Depth-32 pixmap, GC, `PutImage` with ZPixmap BGRA
   (little-endian byte order `B,G,R,A`), installed as the window background
   pixmap, then `ClearArea`. Go's `image/RGBA` is already alpha-premultiplied,
   which is what a composited ARGB visual expects. 305x54x4 = 65,880 bytes, well
   inside one request.
3. **Compositor detection.** Check for a `_NET_WM_CM_S<screen>` selection owner
   at open. If absent, fall back to squared corners with no shadow and log once —
   without a compositor, alpha renders as black.
4. **XShape input region** set to the visible capsule rect, plus the panel rect
   while expanded, so shadow padding and rounded corners do not eat clicks.
   Bounding region is deliberately left unshaped — shaping it would clip away the
   antialiasing.
5. **Click semantics unchanged:** left `jump`, middle `dismiss`, right `ack`.

### picom interaction

visor's own shadow plus picom's `wintypes: dock = { shadow = true }` produces a
double shadow (confirmed). `visor hud install` prints a snippet for the user's
picom `shadow-exclude`, mirroring how `visor install` prints a `settings.json`
snippet:

```
shadow-exclude = [ "name *= 'visor'" ]
```

Tab windows already set `WM_NAME` via `ewmh.WmNameSet`; names must be
`visor`-prefixed for that match to work. Until the user pastes it the failure
mode is cosmetic, not broken. `shadow = false` in visor's config is the other
escape hatch, for users who prefer picom's shadow or none.

## wlr backend

Mostly mechanical: new geometry constants, and the buffer now carries alpha in
the shadow pad as well as the collapsed panel region. `tabOverflow`,
double-buffering and the `released bool` ownership tracking in `buffer.go` are
untouched. `tabGap` folds into the shared row pitch.

The dispatch loop stays a 20 Hz poller. The animation tick now also drives the
elapsed counter and the permission halo pulse.

## Config

`$XDG_CONFIG_HOME/visor/hud.conf` (fallback `~/.config/visor/hud.conf`). Flat
`key = value`, one per line, `#` comments. Two keys:

```
theme = silent
shadow = true
```

Precedence per key: command-line flag (`--theme`, `--shadow`) → environment
(`VISOR_THEME`, `VISOR_SHADOW`) → config file → default (`silent`, `true`). A
flag pins its key and suppresses live reload for it.

`visor hud theme <name>` and `visor hud shadow on|off` rewrite the file,
preserving the other key. The HUD watches the file with fsnotify — already a
dependency — and re-renders every tab in place. No new IPC verb, and no
presentation state in the daemon's store.

An unknown theme name falls back to `silent` with a logged warning rather than
failing to start.

## State and data additions

- **`StateSince time.Time`** on the session, set only when `(activity, waiting)`
  changes — *not* on attention changes. The HUD ticks the elapsed counter locally
  off the existing animation loop.

  `StateSince` stays **out** of the broadcast digest and does not need to be in
  it: it only moves when a digested field moves. This is precisely why elapsed
  must not be rendered from `LastUpdate` — a transcript append to an
  already-`working` session changes no digested field, so no broadcast fires and
  a `LastUpdate`-derived counter would silently freeze. Widening the digest to
  fix that would reintroduce the flicker `CLAUDE.md` warns about.

- **`display_name`** computed server-side alongside the existing `display_cwd`,
  so backends stay logic-free.

- **`glyph`** computed server-side with collision disambiguation, in the digest.

## Retiring eww

Delete `internal/hud/eww/`, drop the case from `cmd/visor/hud.go::pickBackend`,
remove the `embed.FS` reference. This removes the second colour source of truth,
which is what made "three themes everywhere" expensive.

The default backend becomes **auto-detect**: `WAYLAND_DISPLAY` set and
layer-shell advertised → `wlr`, else `x11`. `--backend` still forces either.
The `Backend` interface is unchanged.

Update `CLAUDE.md`: the eww notes, the "Eww has no input-shape option" gotcha
(now only historical context for why x11 exists), and the conventions paragraph
about eww assets living in `internal/hud/eww/`.

## Testing

`render` is the one package with real tests and stays that way — pure functions
over pixels. Extend `render/tab_test.go` with:

1. A table test asserting every registered theme populates every `Palette`
   token, so a half-filled theme fails loudly rather than rendering black.
2. A salience-ordering assertion: for each theme, capsule `Base` luminance is
   monotonically non-increasing across `permission → needs → working/ack →
   dismissed`. This is the bug that motivated the redesign; encode it so it
   cannot regress.
3. Geometry and overflow assertions at the new dimensions.
4. Golden PNGs: collapsed and expanded capsule per theme, shadow on and off.

No new test infrastructure for the backends — they need a display server and are
not worth faking. `cmd/visorprobe` is deleted once the x11 work lands; its raw
ARGB/`PutImage`/XShape code is the reference to lift.

## Risks

- **No compositor.** Detection covers the common case, but a user running a bare
  WM with no compositor gets the squared fallback. Acceptable and logged.
- **Sub-pixel text at 12.5pt on a neutral panel.** Should be better than today's
  dark-on-amber, but worth a look on the real display before tuning stops.
- **Two-letter glyph collisions** beyond two-way ties (three projects starting
  `de-`) still collide. Stack order is stable and the panel disambiguates on
  hover; not worth solving further.
- **Widening 10px → 18px** permanently consumes 8 more px of screen edge.
  Accepted deliberately: the previous width had no room for craft.
