# Wayland HUD Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a native Wayland HUD backend (`wlr`) that runs on wlr-layer-shell compositors (Niri, sway, hyprland, river, wayfire, labwc, KDE), with feature parity to the existing X11 backend (tongue per session, hover-expand, click-to-ack/dismiss/jump, text rendering).

**Architecture:** Extract the X11 backend's pure-pixel drawing into a new backend-agnostic `internal/hud/render` package returning `*image.RGBA`. Build `internal/hud/wlr` on top of `codeberg.org/tesselslate/wl` (pure-Go Wayland client) plus locally-generated `wlr-layer-shell-unstable-v1` bindings. One `zwlr_layer_surface_v1` per session, anchored to the right edge with double-buffered `wl_shm` frames. Subscription/click dispatch reuses the existing daemon IPC.

**Tech Stack:** Go 1.22+, `codeberg.org/tesselslate/wl`, freetype-go (already vendored transitively), `golang.org/x/sys/unix` for shm fds. No cgo.

**Spec:** `docs/superpowers/specs/2026-05-27-wayland-backend-design.md`

---

## File Structure

**New files:**
- `internal/hud/render/font.go` — Font loader (moved from `internal/hud/x11/font.go`), returns `*truetype.Font`.
- `internal/hud/render/tongue.go` — `DrawTongue(state, dims, font) *image.RGBA`. Pure function.
- `internal/hud/render/tongue_test.go` — Unit tests for `DrawTongue` and overflow detection.
- `internal/hud/wlr/wlr.go` — `Backend` impl (Name/Install/Open/Close).
- `internal/hud/wlr/dock.go` — Display connect, registry binding, event dispatch loop, shutdown.
- `internal/hud/wlr/surface.go` — One `layerSurface` per session: create, configure, redraw, destroy.
- `internal/hud/wlr/buffer.go` — `wl_shm` pool, double-buffered RGBA frames, release tracking.
- `internal/hud/wlr/input.go` — Pointer handlers (enter/leave/button).
- `internal/hud/wlr/subscribe.go` — Snapshot stream consumer (copied verbatim from x11/subscribe.go; we accept the duplication per the spec).
- `internal/hud/wlr/protocol/layer_shell_v1.go` — Generated bindings (do not edit).
- `internal/hud/wlr/protocol/layer_shell_v1.xml` — Source XML, checked in for reproducibility.
- `internal/hud/wlr/protocol/gen.go` — `//go:generate` directive documenting how to regenerate.

**Modified files:**
- `internal/hud/x11/font.go` — Deleted.
- `internal/hud/x11/tongue.go` — `render()` delegates pixel drawing to `render.DrawTongue`, wraps result in `xgraphics.Image` for X upload. Tooltip drawing stays in x11 (X-only feature).
- `internal/hud/x11/dock.go` — `loadFont()` call replaced with `render.LoadFont()`.
- `cmd/visor/hud.go` — `pickBackend` gains a `"wlr"` case.
- `go.mod` / `go.sum` — Adds `codeberg.org/tesselslate/wl` dependency.

---

## Conventions for this plan

- The project has **no test suite today** (per `CLAUDE.md`). Per the user's standing instructions and the spec, we add unit tests *only* for the new pure-Go `render` package (cheap, valuable, zero infra needed). Wayland integration code is validated by manual run on Niri.
- Each task ends with an explicit commit. Use Conventional Commits format (`feat:`, `refactor:`, `chore:`).
- "Run: `go build ./...`" is the cheapest correctness gate and is the minimum verification after structural changes.

---

## Task 1: Extract pure-Go `render` package — preserve x11 behavior

**Goal:** Move font loading and tongue pixel drawing out of `internal/hud/x11` into a new `internal/hud/render` package. After this task, x11 must look pixel-identical to before. No Wayland code yet.

**Files:**
- Create: `internal/hud/render/font.go`
- Create: `internal/hud/render/tongue.go`
- Create: `internal/hud/render/tongue_test.go`
- Modify: `internal/hud/x11/tongue.go`
- Modify: `internal/hud/x11/dock.go`
- Delete: `internal/hud/x11/font.go`

### Steps

- [ ] **Step 1.1: Create `internal/hud/render/font.go`**

Move font discovery out of x11. The font is loaded via `xgraphics.ParseFont` today; that helper just wraps `truetype.Parse`. Use `truetype.Parse` directly so this file has no X11 deps.

```go
// Package render produces backend-agnostic tongue images consumed by the
// x11 and wlr HUD backends.
package render

import (
	"errors"
	"io"
	"os"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
)

// fontCandidates is the search order for a monospaced TrueType font on the
// system. First match wins; we open + parse once at backend startup.
var fontCandidates = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/dejavu/DejaVuSansMono.ttf",
	"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/liberation2/LiberationMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSansMono-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoMono-Regular.ttf",
}

// LoadFont returns the first system-installed mono TTF that parses.
// Returns an error if none of the candidate paths are readable.
func LoadFont() (*truetype.Font, error) {
	for _, p := range fontCandidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		ft, perr := parseFont(f)
		f.Close()
		if perr == nil {
			return ft, nil
		}
	}
	return nil, errors.New("no mono TTF font found on system (tried DejaVu / Liberation / Noto)")
}

func parseFont(r io.Reader) (*truetype.Font, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return truetype.Parse(b)
}
```

- [ ] **Step 1.2: Create `internal/hud/render/tongue.go`**

This is the core extraction. The x11 code currently uses `xgraphics.Image.Text` (which wraps freetype-go's glyph rasterizer) and `xgraphics.Extents` for measuring. We replace those with direct freetype-go calls on `*image.RGBA`. The output of this function is what x11 would historically have drawn into the window's background pixmap, minus the X-specific upload step.

```go
package render

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/BurntSushi/freetype-go/freetype"
	"github.com/BurntSushi/freetype-go/freetype/truetype"
)

// Shared with both backends. Keep in sync with the x11 backend's window sizing.
const (
	TongueW   = 10  // visible width when collapsed
	TongueH   = 36  // window height
	ExpandedW = 300 // visible width when hovered
	TextPad   = 18  // x where the cwd label begins
	FontPt    = 13.5
	// TextYBaseline is the freetype baseline; picked so the cap height sits
	// centred-ish in TongueH. Empirically matched to the previous x11 layout.
	TextYBaseline = 24
)

// TongueState is the subset of session data the renderer needs.
type TongueState struct {
	Color uint32 // 0x00RRGGBB background
	Label string // already-resolved display label (Title || DisplayCWD || ID[:8])
}

// TongueImage is the rendered output plus metadata x11/wlr both need.
type TongueImage struct {
	RGBA     *image.RGBA
	Overflow bool // true if Label was wider than the panel could show
}

// DrawTongue produces a TongueW-by-TongueH (wait, ExpandedW-wide) RGBA buffer
// with a solid background and the label rendered starting at TextPad. The
// returned image is fully opaque; the caller decides how to display the
// collapsed-only portion vs the expanded portion.
//
// `font` may be nil — in that case the label is skipped and Overflow is false.
func DrawTongue(s TongueState, font *truetype.Font) TongueImage {
	img := image.NewRGBA(image.Rect(0, 0, ExpandedW, TongueH))
	bg := unpackRGBA(s.Color)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	out := TongueImage{RGBA: img}
	if font == nil || s.Label == "" {
		return out
	}

	fg := contrastFG(bg)
	out.Overflow = drawText(img, font, FontPt, TextPad, TextYBaseline, fg, s.Label)
	return out
}

// drawText renders `text` into img using freetype directly. Returns true if
// the rendered text width exceeded the visible label region (ExpandedW - TextPad - 8).
func drawText(img *image.RGBA, font *truetype.Font, ptSize float64, x, yBaseline int, fg color.Color, text string) (overflow bool) {
	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(font)
	c.SetFontSize(ptSize)
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(&image.Uniform{C: fg})

	pt := freetype.Pt(x, yBaseline)
	end, err := c.DrawString(text, pt)
	if err != nil {
		return false
	}
	textRightPx := int(end.X >> 6) // fixed-point 26.6 → integer pixels
	return textRightPx > (ExpandedW - 8)
}

// unpackRGBA converts a packed 0xRRGGBB to an opaque color.RGBA.
func unpackRGBA(c uint32) color.RGBA {
	return color.RGBA{
		R: uint8((c >> 16) & 0xff),
		G: uint8((c >> 8) & 0xff),
		B: uint8(c & 0xff),
		A: 0xff,
	}
}

// contrastFG picks a near-black or near-white foreground based on bg luminance.
func contrastFG(bg color.RGBA) color.RGBA {
	lum := (int(bg.R)*299 + int(bg.G)*587 + int(bg.B)*114) / 1000
	if lum > 140 {
		return color.RGBA{0x10, 0x14, 0x1c, 0xff}
	}
	return color.RGBA{0xe5, 0xe9, 0xf0, 0xff}
}
```

- [ ] **Step 1.3: Create `internal/hud/render/tongue_test.go`**

Pure-function tests. No X server, no Wayland, no font discovery — these tests skip if the system font isn't available.

```go
package render

import (
	"image/color"
	"testing"
)

func TestDrawTongue_BackgroundFill(t *testing.T) {
	img := DrawTongue(TongueState{Color: 0x223344, Label: ""}, nil)
	if img.RGBA.Bounds().Dx() != ExpandedW || img.RGBA.Bounds().Dy() != TongueH {
		t.Fatalf("size = %v, want %dx%d", img.RGBA.Bounds(), ExpandedW, TongueH)
	}
	// Sample a pixel in the collapsed-tongue region (x=2) and one in the panel (x=200).
	for _, x := range []int{2, 200} {
		got := img.RGBA.RGBAAt(x, TongueH/2)
		want := color.RGBA{R: 0x22, G: 0x33, B: 0x44, A: 0xff}
		if got != want {
			t.Errorf("pixel at (%d,%d) = %v, want %v", x, TongueH/2, got, want)
		}
	}
}

func TestDrawTongue_NoFontSkipsText(t *testing.T) {
	img := DrawTongue(TongueState{Color: 0x000000, Label: "ignored without font"}, nil)
	if img.Overflow {
		t.Errorf("overflow=true with nil font; want false")
	}
}

func TestDrawTongue_OverflowOnLongLabel(t *testing.T) {
	font, err := LoadFont()
	if err != nil {
		t.Skipf("no system font: %v", err)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	img := DrawTongue(TongueState{Color: 0x445566, Label: long}, font)
	if !img.Overflow {
		t.Errorf("overflow=false for 200-char label; want true")
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
```

- [ ] **Step 1.4: Run the tests, expect them to pass**

```
go test ./internal/hud/render/...
```
Expected: all tests pass (or `TestDrawTongue_OverflowOnLongLabel` skips if no system font; that's acceptable on minimal CI/sandbox environments).

- [ ] **Step 1.5: Modify `internal/hud/x11/tongue.go::render()` to call `render.DrawTongue`**

Replace the inline drawing loop + `im.Text` call with a delegation. We still need an `xgraphics.Image` to upload to X, but we build it *from* the `*image.RGBA` returned by `render.DrawTongue`.

Find the current `render()` method (lines ~405-436) and replace its body:

```go
func (t *tongue) render() {
	if t.expandedImg != nil {
		t.expandedImg.Destroy()
		t.expandedImg = nil
	}

	rt := render.DrawTongue(render.TongueState{
		Color: t.opt.color,
		Label: displayLabel(t.sess),
	}, t.font)
	t.overflow = rt.Overflow

	// Wrap the RGBA into an xgraphics.Image for X upload. xgraphics.Image
	// embeds image.RGBA, so we copy pixels into a freshly-constructed one.
	im := xgraphics.New(t.X, rt.RGBA.Bounds())
	copy(im.Pix, rt.RGBA.Pix)

	im.CreatePixmap()
	im.XDraw()
	im.XSurfaceSet(t.win.Id)
	xproto.ClearArea(t.X.Conn(), false, t.win.Id, 0, 0, expandedW, tongueH)
	t.expandedImg = im
}
```

Add the import: `"github.com/nitzanz/visor/internal/hud/render"`.

Delete the now-unused `rgba` and `contrastFG` helpers from `tongue.go` (they live in `render` now).

The tooltip drawing in `showTooltip` still uses `im.Text` and `rgba` — leave the tooltip alone for this task. Re-add a local `rgba` helper in tongue.go *only if* the tooltip needs it, or rewrite the tooltip to use `render.unpackRGBA` (it's unexported — easier to just keep a local helper for now). Concretely: keep the `rgba` function but mark it as tooltip-only by moving it next to `showTooltip`.

Also replace the size constants used in `newTongue` (`expandedW`, `tongueH`) with `render.ExpandedW` and `render.TongueH` so both backends share the same dimensions. Keep the lowercase aliases at the top of `tongue.go`:

```go
const (
	tongueW   = render.TongueW
	tongueH   = render.TongueH
	expandedW = render.ExpandedW
	textPad   = render.TextPad
	fontPt    = render.FontPt
)
```

- [ ] **Step 1.6: Modify `internal/hud/x11/dock.go` to use `render.LoadFont`**

Replace:
```go
if f, ferr := loadFont(); ferr != nil {
```
with:
```go
if f, ferr := render.LoadFont(); ferr != nil {
```
Add the `render` import.

- [ ] **Step 1.7: Delete `internal/hud/x11/font.go`**

```
rm internal/hud/x11/font.go
```

- [ ] **Step 1.8: Build and smoke-test x11**

```
go build ./...
go vet ./...
```
Expected: no errors. If you have an X session handy, run `./bin/visor daemon &` + `./bin/visor hud open --backend=x11` and confirm tongues still render the same as before. Pixel-identical output is the success criterion.

- [ ] **Step 1.9: Commit**

```
git add internal/hud/render/ internal/hud/x11/tongue.go internal/hud/x11/dock.go
git rm internal/hud/x11/font.go
git commit -m "refactor: extract internal/hud/render from x11 backend"
```

---

## Task 2: Add `tesselslate/wl` dependency and generate layer-shell bindings

**Goal:** Vendor the Wayland client library and produce checked-in Go bindings for `wlr-layer-shell-unstable-v1`.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/hud/wlr/protocol/layer_shell_v1.xml`
- Create: `internal/hud/wlr/protocol/layer_shell_v1.go`
- Create: `internal/hud/wlr/protocol/gen.go`

### Steps

- [ ] **Step 2.1: Add the `tesselslate/wl` module**

```
go get codeberg.org/tesselslate/wl@latest
```

Verify it landed by reading `go.mod` — there should be a `codeberg.org/tesselslate/wl vX.Y.Z` require line. Pin to the exact version that came back.

- [ ] **Step 2.2: Fetch the wlr-layer-shell protocol XML**

Source of truth: `https://gitlab.freedesktop.org/wlroots/wlr-protocols/-/raw/master/unstable/wlr-layer-shell-unstable-v1.xml`.

```
mkdir -p internal/hud/wlr/protocol
curl -sSL -o internal/hud/wlr/protocol/layer_shell_v1.xml \
  https://gitlab.freedesktop.org/wlroots/wlr-protocols/-/raw/master/unstable/wlr-layer-shell-unstable-v1.xml
```

Sanity check: `grep -c '<interface name="zwlr_layer_shell_v1"' internal/hud/wlr/protocol/layer_shell_v1.xml` should print `1`.

- [ ] **Step 2.3: Generate the Go bindings**

```
go run codeberg.org/tesselslate/wl/cmd/scanner \
  -pkg protocol \
  internal/hud/wlr/protocol/layer_shell_v1.xml \
  > internal/hud/wlr/protocol/layer_shell_v1.go
```

(If the scanner's CLI flags differ from `-pkg`, read `go doc codeberg.org/tesselslate/wl/cmd/scanner` and adapt. Confirm the generated file declares `package protocol` and references `codeberg.org/tesselslate/wl` for the runtime types.)

- [ ] **Step 2.4: Create `internal/hud/wlr/protocol/gen.go`**

Documents how to regenerate. No runtime code.

```go
// Package protocol holds generated Wayland protocol bindings used by the
// wlr HUD backend.
//
// To regenerate after updating the source XML files:
//
//   go run codeberg.org/tesselslate/wl/cmd/scanner \
//     -pkg protocol \
//     internal/hud/wlr/protocol/layer_shell_v1.xml \
//     > internal/hud/wlr/protocol/layer_shell_v1.go
//
// The .xml source files in this directory are the canonical inputs. Do not
// edit the generated .go files by hand.
package protocol
```

- [ ] **Step 2.5: Verify it compiles**

```
go build ./internal/hud/wlr/protocol/...
```
Expected: no errors.

- [ ] **Step 2.6: Commit**

```
git add go.mod go.sum internal/hud/wlr/protocol/
git commit -m "chore: vendor tesselslate/wl and generate wlr-layer-shell bindings"
```

---

## Task 3: `wlr` backend skeleton — connect, register, dispatch, shutdown

**Goal:** Land the smallest possible `Backend` impl that opens a Wayland connection, binds the required globals from the registry, runs an event-dispatch loop, and shuts down cleanly on SIGINT. No surfaces yet.

**Files:**
- Create: `internal/hud/wlr/wlr.go`
- Create: `internal/hud/wlr/dock.go`

### Steps

- [ ] **Step 3.1: Create `internal/hud/wlr/wlr.go`**

```go
// Package wlr is the native Wayland HUD backend.
//
// One zwlr_layer_surface_v1 per Claude session, anchored to the right edge of
// the primary output. Surfaces are drawn into wl_shm buffers; double-buffered
// so a frame is never modified while the compositor holds it.
//
// Compositor requirements: zwlr_layer_shell_v1 must be in the registry.
// Tested on Niri (primary), sway, and hyprland. Will NOT work on GNOME —
// use --backend=x11 (Xwayland) there.
package wlr

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
)

// Backend implements hud.Backend by running an in-process Wayland client
// that subscribes to the visor daemon and manages one layer surface per
// session.
type Backend struct{}

func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return "wlr" }

func (b *Backend) Install() (string, error) {
	return "wlr backend is built into visor; nothing to install. Run `visor hud open --backend=wlr`.\n", nil
}

func (b *Backend) Open() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d, err := newDock(ctx)
	if err != nil {
		return fmt.Errorf("connect wayland: %w", err)
	}
	defer d.close()
	return d.run(ctx)
}

func (b *Backend) Close() error {
	return fmt.Errorf("wlr backend runs in-foreground; send SIGTERM (Ctrl-C / kill) to stop it")
}
```

- [ ] **Step 3.2: Create `internal/hud/wlr/dock.go`**

The skeleton dock. Binds globals, runs `Dispatch` in a loop, exits on ctx cancel. No surfaces yet — that's task 4.

```go
package wlr

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/hud/wlr/protocol"
)

type dock struct {
	log *slog.Logger

	display    *wl.Display
	registry   *wl.Registry
	compositor *wl.Compositor
	shm        *wl.Shm
	seat       *wl.Seat
	output     *wl.Output
	layerShell *protocol.ZwlrLayerShellV1
}

func newDock(ctx context.Context) (*dock, error) {
	d := &dock{
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	disp, err := wl.Connect("")
	if err != nil {
		return nil, fmt.Errorf("wl.Connect: %w", err)
	}
	d.display = disp
	d.registry = disp.GetRegistry()

	// Listen for global announcements. The registry callback fires once per
	// global currently in the compositor's registry, then again for any later
	// additions. We bind synchronously here using a roundtrip.
	d.registry.SetGlobalHandler(d.onGlobal)

	if err := d.display.Roundtrip(); err != nil {
		return nil, fmt.Errorf("registry roundtrip: %w", err)
	}

	if d.compositor == nil {
		return nil, fmt.Errorf("compositor missing wl_compositor")
	}
	if d.shm == nil {
		return nil, fmt.Errorf("compositor missing wl_shm")
	}
	if d.seat == nil {
		return nil, fmt.Errorf("compositor missing wl_seat")
	}
	if d.layerShell == nil {
		return nil, fmt.Errorf("compositor missing zwlr_layer_shell_v1 (GNOME? try --backend=x11)")
	}
	if d.output == nil {
		return nil, fmt.Errorf("no wl_output advertised")
	}

	d.log.Info("wayland connected")
	return d, nil
}

// onGlobal is invoked for every wl_registry.global event during the initial
// roundtrip and any time the compositor announces a new global afterwards.
// We bind the first instance of each global we care about; later announcements
// (e.g. a second output) are logged and ignored — multi-output is a follow-up.
func (d *dock) onGlobal(name uint32, iface string, version uint32) {
	switch iface {
	case "wl_compositor":
		if d.compositor == nil {
			d.compositor = wl.BindCompositor(d.registry, name, version)
		}
	case "wl_shm":
		if d.shm == nil {
			d.shm = wl.BindShm(d.registry, name, version)
		}
	case "wl_seat":
		if d.seat == nil {
			d.seat = wl.BindSeat(d.registry, name, version)
		}
	case "wl_output":
		if d.output == nil {
			d.output = wl.BindOutput(d.registry, name, version)
		}
	case "zwlr_layer_shell_v1":
		if d.layerShell == nil {
			d.layerShell = protocol.BindZwlrLayerShellV1(d.registry, name, version)
		}
	}
}

func (d *dock) close() {
	if d.display != nil {
		d.display.Close()
	}
}

// run pumps the Wayland event loop until ctx is cancelled. The cancel triggers
// a Display.Close from a watcher goroutine, which unblocks Dispatch with EOF.
func (d *dock) run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		d.display.Close()
	}()

	for {
		if err := d.display.Dispatch(); err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("dispatch: %w", err)
		}
	}
}
```

> **Note on `wl.BindCompositor` and friends:** The exact symbol names depend on what the `tesselslate/wl` package exposes. Verify with `go doc codeberg.org/tesselslate/wl | grep -i bind` after Step 2.1; adjust names if they differ (e.g. `wl.RegistryBindCompositor`, or a typed `registry.Bind(name, &wl.Compositor{}, version)` pattern). Same applies to event-handler setters: `SetGlobalHandler` may be `AddListener` or `OnGlobal` depending on the generator's idiom. Resolve by reading the package docs once, then apply consistently across the file.

- [ ] **Step 3.3: Build**

```
go build ./internal/hud/wlr/...
```
Expected: clean compile. If symbol-name mismatches, fix per the note above.

- [ ] **Step 3.4: Manual smoke test**

Temporarily add a `case "wlr":` to `cmd/visor/hud.go::pickBackend` (we'll do this properly in Task 6). Then under Niri:

```
go build -o bin/visor ./cmd/visor
./bin/visor hud open --backend=wlr
```
Expected: process starts, logs `wayland connected`, blocks. Ctrl-C exits cleanly with no panics. No surfaces appear — that's task 4.

Revert the temporary `pickBackend` edit.

- [ ] **Step 3.5: Commit**

```
git add internal/hud/wlr/wlr.go internal/hud/wlr/dock.go
git commit -m "feat(wlr): backend skeleton — connect, bind globals, dispatch loop"
```

---

## Task 4: One static tongue surface

**Goal:** Show a single hard-coded layer surface (fixed colour, fixed position) on the right edge of the screen. No snapshot subscription, no input handling. This proves the buffer/configure dance works before we layer on the rest.

**Files:**
- Create: `internal/hud/wlr/surface.go`
- Create: `internal/hud/wlr/buffer.go`
- Modify: `internal/hud/wlr/dock.go`

### Steps

- [ ] **Step 4.1: Create `internal/hud/wlr/buffer.go`**

A double-buffered `wl_shm` pool sized for the larger (expanded) tongue. Each `Buffer` tracks whether the compositor still holds it. Uses `golang.org/x/sys/unix` for `memfd_create`.

First add the dep:
```
go get golang.org/x/sys/unix@latest
```

```go
package wlr

import (
	"fmt"
	"image"
	"syscall"

	"codeberg.org/tesselslate/wl"
	"golang.org/x/sys/unix"

	"github.com/nitzanz/visor/internal/hud/render"
)

const (
	bufW    = render.ExpandedW
	bufH    = render.TongueH
	bufStri = bufW * 4 // 4 bytes per pixel, ARGB8888
	bufSize = bufStri * bufH
)

// shmPool owns a single mmap'd memfd shared with the compositor. It holds two
// buffers; the dock picks whichever is currently released.
type shmPool struct {
	pool *wl.ShmPool
	mmap []byte
	fd   int

	buffers [2]*Buffer
}

// Buffer is one half of the double-buffered pool. Pix is the writable slice
// the renderer fills; Wl is the wl_buffer handed to the compositor.
type Buffer struct {
	Wl       *wl.Buffer
	Pix      []byte // length == bufSize
	released bool   // true if compositor has released the buffer
}

func newShmPool(shm *wl.Shm) (*shmPool, error) {
	// memfd_create avoids needing a /dev/shm path. MFD_CLOEXEC keeps the fd
	// from leaking into child processes (we don't fork, but be defensive).
	fd, err := unix.MemfdCreate("visor-wlr", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	if err := syscall.Ftruncate(fd, int64(bufSize*2)); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("ftruncate: %w", err)
	}
	mmap, err := syscall.Mmap(fd, 0, bufSize*2, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("mmap: %w", err)
	}

	pool := shm.CreatePool(uintptr(fd), int32(bufSize*2))
	p := &shmPool{pool: pool, mmap: mmap, fd: fd}

	for i := 0; i < 2; i++ {
		off := int32(i * bufSize)
		wb := pool.CreateBuffer(off, int32(bufW), int32(bufH), int32(bufStri), wl.ShmFormatArgb8888)
		buf := &Buffer{
			Wl:       wb,
			Pix:      mmap[off : off+int32(bufSize) : off+int32(bufSize)],
			released: true,
		}
		// Mark released when the compositor finishes with the buffer.
		wb.SetReleaseHandler(func() { buf.released = true })
		p.buffers[i] = buf
	}
	return p, nil
}

// Acquire returns a released buffer ready for writing, or nil if both are
// still in-flight (in which case the caller should drop the frame).
func (p *shmPool) Acquire() *Buffer {
	for _, b := range p.buffers {
		if b.released {
			b.released = false
			return b
		}
	}
	return nil
}

func (p *shmPool) close() {
	if p.mmap != nil {
		syscall.Munmap(p.mmap)
	}
	if p.fd > 0 {
		syscall.Close(p.fd)
	}
}

// CopyRGBA copies an *image.RGBA (R,G,B,A byte order) into the buffer as
// ARGB8888 little-endian, which is what wl_shm expects.
func (b *Buffer) CopyRGBA(img *image.RGBA) {
	src := img.Pix
	dst := b.Pix
	// RGBA → BGRA byte swap, alpha preserved.
	for i := 0; i+3 < len(src) && i+3 < len(dst); i += 4 {
		dst[i+0] = src[i+2] // B
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i+0] // R
		dst[i+3] = src[i+3] // A
	}
}

```

- [ ] **Step 4.2: Create `internal/hud/wlr/surface.go`**

One static surface, fixed colour, no input handling yet.

```go
package wlr

import (
	"fmt"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/hud/wlr/protocol"
)

// layerSurface is one tongue: a wl_surface + zwlr_layer_surface_v1 pair plus
// the shm pool that backs its frames.
type layerSurface struct {
	surface      *wl.Surface
	layerSurface *protocol.ZwlrLayerSurfaceV1
	pool         *shmPool

	// Last size acked from a configure event. Zero until first configure.
	width, height int32

	// Pending pixels to draw on next configure-ack. For the static surface
	// in this task, we paint once and never again.
	state render.TongueState
	font  *font // placeholder pointer; assigned in task 5
}

type font = struct{ /* placeholder; real type in task 5 */ }

// newLayerSurface creates the surface, sets layer-shell properties, and waits
// for the first configure (via a roundtrip) before painting.
func newLayerSurface(d *dock, slot int, st render.TongueState) (*layerSurface, error) {
	surf := d.compositor.CreateSurface()
	ls := d.layerShell.GetLayerSurface(surf, d.output,
		protocol.ZwlrLayerShellV1LayerOverlay,
		"visor-tongue",
	)

	// Anchor to the top-right corner (anchor right + top); margin_top stacks
	// tongues vertically. Exclusive zone -1: don't reserve space, don't be
	// pushed by exclusives.
	ls.SetAnchor(protocol.ZwlrLayerSurfaceV1AnchorTop | protocol.ZwlrLayerSurfaceV1AnchorRight)
	ls.SetSize(uint32(render.ExpandedW), uint32(render.TongueH))
	ls.SetExclusiveZone(-1)
	ls.SetMargin(int32(slot*render.TongueH), 0, 0, 0) // top, right, bottom, left
	ls.SetKeyboardInteractivity(0)                    // none

	ps := &layerSurface{surface: surf, layerSurface: ls, state: st}

	// Configure handler: ack and store size.
	ls.SetConfigureHandler(func(serial uint32, w, h uint32) {
		ls.AckConfigure(serial)
		ps.width = int32(w)
		ps.height = int32(h)
		ps.repaint(d)
	})
	ls.SetClosedHandler(func() {
		// Compositor told us to go away. For now, log and ignore — Task 5
		// will plumb this into the dock's surface map for proper cleanup.
	})

	// Initial commit with no buffer attached triggers the first configure.
	surf.Commit()

	pool, err := newShmPool(d.shm)
	if err != nil {
		return nil, fmt.Errorf("shm pool: %w", err)
	}
	ps.pool = pool

	return ps, nil
}

// repaint acquires a buffer, fills it from render.DrawTongue, attaches and
// commits.
func (s *layerSurface) repaint(d *dock) {
	buf := s.pool.Acquire()
	if buf == nil {
		return // both buffers in-flight; the next compositor frame will retry
	}
	img := render.DrawTongue(s.state, d.font)
	buf.CopyRGBA(img.RGBA)
	s.surface.Attach(buf.Wl, 0, 0)
	s.surface.Damage(0, 0, render.ExpandedW, render.TongueH)
	s.surface.Commit()
}

func (s *layerSurface) destroy() {
	if s.pool != nil {
		s.pool.close()
	}
	if s.layerSurface != nil {
		s.layerSurface.Destroy()
	}
	if s.surface != nil {
		s.surface.Destroy()
	}
}
```

- [ ] **Step 4.3: Modify `internal/hud/wlr/dock.go` to load the font and create one static surface**

Add to the `dock` struct:
```go
font *truetype.Font
test *layerSurface // temporary — replaced in Task 5
```
Imports:
```go
"github.com/BurntSushi/freetype-go/freetype/truetype"
"github.com/nitzanz/visor/internal/hud/render"
```

In `newDock`, after globals are bound, load the font:
```go
if f, err := render.LoadFont(); err != nil {
	d.log.Warn("font load failed; tongues will be blank", "err", err)
} else {
	d.font = f
}
```

After font load, create a static surface for smoke-testing:
```go
ls, err := newLayerSurface(d, 0, render.TongueState{
	Color: 0xff5566,
	Label: "visor wlr smoke test",
})
if err != nil {
	d.close()
	return nil, fmt.Errorf("create test surface: %w", err)
}
d.test = ls
```

In `close()`, destroy the test surface:
```go
if d.test != nil {
	d.test.destroy()
}
```

- [ ] **Step 4.4: Build**

```
go build ./internal/hud/wlr/...
```
Fix any symbol-mismatch errors against the generated bindings — these may use slightly different names (e.g. `LayerOverlay` vs `ZwlrLayerShellV1LayerOverlay`). Look at `internal/hud/wlr/protocol/layer_shell_v1.go` to confirm the exact constants and method names; adapt the code in `surface.go` to match. Do not change the generated file.

- [ ] **Step 4.5: Manual verification under Niri**

Wire the wlr backend temporarily into `pickBackend` (as in Step 3.4), build, and run:

```
go build -o bin/visor ./cmd/visor
./bin/visor hud open --backend=wlr
```

Expected: a single 300×36 pinkish-red rectangle pinned to the top-right of the screen, with the label "visor wlr smoke test" rendered in white. Ctrl-C exits cleanly.

Revert the temporary `pickBackend` edit.

- [ ] **Step 4.6: Commit**

```
git add internal/hud/wlr/surface.go internal/hud/wlr/buffer.go internal/hud/wlr/dock.go
git commit -m "feat(wlr): render one static layer surface"
```

---

## Task 5: Subscription loop — surfaces driven by daemon snapshots

**Goal:** Replace the static test surface with a dynamic surface map keyed by session id. The dock subscribes to `visor ctl watch`, diffs snapshots, and creates/destroys/redraws layer surfaces accordingly.

**Files:**
- Create: `internal/hud/wlr/subscribe.go` (verbatim copy of `internal/hud/x11/subscribe.go`)
- Modify: `internal/hud/wlr/dock.go`
- Modify: `internal/hud/wlr/surface.go`

### Steps

- [ ] **Step 5.1: Copy `subscribe.go` from x11**

```
cp internal/hud/x11/subscribe.go internal/hud/wlr/subscribe.go
```

Open `internal/hud/wlr/subscribe.go` and change the package declaration from `package x11` to `package wlr`. Leave the rest alone — `sessionView`, `subscribe`, `subscribeLoop` are all backend-agnostic.

(Duplication is intentional, per the spec. If a third caller appears later we'll extract.)

- [ ] **Step 5.2: Modify `internal/hud/wlr/dock.go` to consume the snapshot stream**

Replace the static test surface with a map:

```go
type dock struct {
	// ... existing fields ...
	font     *truetype.Font
	surfaces map[string]*layerSurface // session id → surface
}
```

In `newDock`, after the font load, initialise `d.surfaces = map[string]*layerSurface{}`. Remove the static-surface creation from Task 4.

Update `close()`:
```go
for _, s := range d.surfaces {
	s.destroy()
}
```

Modify `run(ctx)` to drive a select between Wayland events and snapshot updates. Wayland events must be dispatched on the same goroutine that owns the wl objects — we use `Display.PrepareRead`/`ReadEvents`/`DispatchPending` if the library exposes them, otherwise we run `Dispatch` in a worker goroutine and post updates over a channel.

Pick the simplest pattern that works for `tesselslate/wl`. If it offers `PrepareRead`/`DispatchPending` (it almost certainly does — it's standard libwayland API):

```go
func (d *dock) run(ctx context.Context) error {
	snaps := make(chan []sessionView, 4)
	go subscribeLoop(snaps, d.log)

	go func() {
		<-ctx.Done()
		d.display.Close()
	}()

	for {
		// Drain any already-queued events before blocking.
		if err := d.display.DispatchPending(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("dispatch pending: %w", err)
		}
		if err := d.display.Flush(); err != nil {
			return fmt.Errorf("flush: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case snap := <-snaps:
			d.applySnapshot(snap)
		default:
			// Block on the Wayland fd until events arrive or ctx cancels.
			// Implementation depends on what tesselslate/wl exposes:
			//   - If it offers DispatchOnce() that blocks one round, use that.
			//   - Otherwise PrepareRead + select on the fd + ReadEvents.
			if err := d.display.Dispatch(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("dispatch: %w", err)
			}
		}
	}
}
```

> **Reality check:** If `tesselslate/wl` only exposes a blocking `Dispatch()`, we can't `select` between it and a channel directly. Workaround: run `Dispatch()` in a goroutine and post snapshot updates via a thread-safe queue that the main goroutine reads after each dispatch. Read the library's docs (`go doc codeberg.org/tesselslate/wl.Display`) and pick the pattern that fits. The diff-against-snapshot logic below doesn't change either way.

Add `applySnapshot`:

```go
// applySnapshot diffs the incoming session list against the current surface
// map and creates/destroys/updates surfaces to match.
func (d *dock) applySnapshot(snap []sessionView) {
	seen := map[string]bool{}
	for i, s := range snap {
		seen[s.ID] = true
		st := render.TongueState{
			Color: colorFor(s),
			Label: labelFor(s),
		}
		if ls, ok := d.surfaces[s.ID]; ok {
			if ls.state != st {
				ls.state = st
				ls.repaint(d)
			}
			// Re-stack: slot may have changed.
			ls.setSlot(i)
		} else {
			ls, err := newLayerSurface(d, i, st)
			if err != nil {
				d.log.Warn("create surface", "id", s.ID, "err", err)
				continue
			}
			d.surfaces[s.ID] = ls
		}
	}
	for id, ls := range d.surfaces {
		if !seen[id] {
			ls.destroy()
			delete(d.surfaces, id)
		}
	}
}

// labelFor mirrors x11.displayLabel: prefer ai-title, then cwd, then id[:8].
func labelFor(s sessionView) string {
	if s.Title != "" {
		return s.Title
	}
	if s.DisplayCWD != "" {
		return s.DisplayCWD
	}
	if len(s.ID) >= 8 {
		return s.ID[:8]
	}
	return s.ID
}

// colorFor maps activity+attention to the same RGB the x11 backend uses.
// Match x11/color.go (or its equivalent). For this task, ship a minimal
// mapping; refine in a follow-up if the x11 mapping is more nuanced.
func colorFor(s sessionView) uint32 {
	switch s.Attention {
	case "needs":
		switch s.Waiting {
		case "permission":
			return 0xff_55_66 // red — permission prompt
		case "user":
			return 0xff_b3_4d // amber — waiting on user
		}
	case "ack":
		return 0x4a_90_d9 // blue
	case "dismissed":
		return 0x3a_3f_4a // muted grey
	}
	switch s.Activity {
	case "working":
		return 0x6e_cb_8a // green
	}
	return 0x60_66_72 // default neutral
}
```

> **Colour mapping:** if `internal/hud/x11` already exports or defines a colour function, reuse it. If colours live behind unexported names, lift them into `render` as part of this task. Don't let the wlr and x11 backends drift.

- [ ] **Step 5.3: Add `setSlot` to `layerSurface`**

```go
// setSlot updates the vertical position of the surface within the dock by
// re-issuing set_margin and committing.
func (s *layerSurface) setSlot(slot int) {
	s.layerSurface.SetMargin(int32(slot*render.TongueH), 0, 0, 0)
	s.surface.Commit()
}
```

- [ ] **Step 5.4: Build**

```
go build ./...
go vet ./...
```

- [ ] **Step 5.5: Manual verification**

Same temporary `pickBackend` patch, then:
```
./bin/visor daemon &
./bin/visor hud open --backend=wlr
# In another terminal: open and close Claude sessions, watch tongues appear and disappear.
```

Expected: each live session gets a tongue, label is correct, colour changes on attention transitions, tongues vanish when sessions end. Ctrl-C exits cleanly.

- [ ] **Step 5.6: Commit**

```
git add internal/hud/wlr/
git commit -m "feat(wlr): drive surfaces from daemon snapshot stream"
```

---

## Task 6: Pointer input — hover-expand and click-to-act

**Goal:** Add `wl_pointer` handling: hover expands the surface (already at `ExpandedW`, so no resize needed — the visual cue is positional only? See note below), click sends `ack` / `dismiss` / `jump` via IPC.

**Files:**
- Create: `internal/hud/wlr/input.go`
- Modify: `internal/hud/wlr/dock.go`
- Modify: `internal/hud/wlr/surface.go`

> **Design note — hover-expand on Wayland:** The x11 backend uses fixed-width windows and slides them sideways to "expand". Layer surfaces are anchored, not free-moving, so the cleanest analogue on Wayland is to render the *collapsed* state (only the leftmost `TongueW` opaque, rest transparent) by default and switch to the *expanded* state (full opaque panel) on pointer enter. This requires the buffer to carry alpha pixels — the `ARGB8888` format already supports it; we just paint the transparent strip at draw time when collapsed.
>
> Concretely: pass `Expanded bool` into `render.TongueState`, and in `DrawTongue` clear `x = TongueW .. ExpandedW` to transparent when collapsed. Update the unit tests accordingly (a new test: "collapsed state has transparent panel region").

### Steps

- [ ] **Step 6.1: Extend `render.TongueState` and `DrawTongue` for collapsed state**

In `internal/hud/render/tongue.go`, add a field:
```go
type TongueState struct {
	Color    uint32
	Label    string
	Expanded bool
}
```

In `DrawTongue`, after the background fill:
```go
if !s.Expanded {
	// Clear the panel region (x = TongueW..ExpandedW) to fully transparent.
	transparent := color.RGBA{0, 0, 0, 0}
	clearRect := image.Rect(TongueW, 0, ExpandedW, TongueH)
	draw.Draw(img, clearRect, &image.Uniform{C: transparent}, image.Point{}, draw.Src)
	out := TongueImage{RGBA: img}
	return out
}
```
(Note: when collapsed we skip text rendering entirely — there's no panel to draw on.)

- [ ] **Step 6.2: Add a render test for the collapsed state**

In `internal/hud/render/tongue_test.go`:

```go
func TestDrawTongue_CollapsedHasTransparentPanel(t *testing.T) {
	img := DrawTongue(TongueState{Color: 0x223344, Expanded: false}, nil)
	// Tongue strip (x=2) should be opaque bg.
	got := img.RGBA.RGBAAt(2, TongueH/2)
	if got.A != 0xff {
		t.Errorf("tongue strip alpha = %#x, want 0xff", got.A)
	}
	// Panel region (x=150) should be transparent.
	got = img.RGBA.RGBAAt(150, TongueH/2)
	if got.A != 0 {
		t.Errorf("panel alpha = %#x, want 0", got.A)
	}
}

func TestDrawTongue_ExpandedHasOpaquePanel(t *testing.T) {
	img := DrawTongue(TongueState{Color: 0x223344, Expanded: true}, nil)
	got := img.RGBA.RGBAAt(150, TongueH/2)
	if got.A != 0xff {
		t.Errorf("expanded panel alpha = %#x, want 0xff", got.A)
	}
}
```

Run: `go test ./internal/hud/render/... -run TongueCollapsed -v`. Expected: PASS (both new tests).

- [ ] **Step 6.3: Update `internal/hud/x11/tongue.go` to keep working**

The x11 backend now needs to pass `Expanded` too — it always wants the expanded state because its window is full-width and uses positional sliding. Set `Expanded: true` in the x11 `render()` call:

```go
rt := render.DrawTongue(render.TongueState{
	Color:    t.opt.color,
	Label:    displayLabel(t.sess),
	Expanded: true,
}, t.font)
```

Build and verify x11 still draws correctly.

- [ ] **Step 6.4: Create `internal/hud/wlr/input.go`**

Pointer setup is conceptually:
1. Bind `wl_seat`'s pointer capability via `wl_seat.get_pointer`.
2. Listen for `enter`, `leave`, `button` events.
3. On `enter`: find the `layerSurface` whose `wl_surface` matches; mark expanded, repaint.
4. On `leave`: mark collapsed, repaint.
5. On `button`: same, but route to IPC.

```go
package wlr

import (
	"log/slog"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
)

const (
	btnLeft   = 0x110 // BTN_LEFT
	btnRight  = 0x111
	btnMiddle = 0x112
)

// pointer wires up wl_pointer event handlers. It looks up surfaces via the
// dock's surfaces map.
type pointer struct {
	d       *dock
	wp      *wl.Pointer
	focused *layerSurface // surface currently under the cursor, nil if none
}

func newPointer(d *dock) *pointer {
	wp := d.seat.GetPointer()
	p := &pointer{d: d, wp: wp}
	wp.SetEnterHandler(p.onEnter)
	wp.SetLeaveHandler(p.onLeave)
	wp.SetButtonHandler(p.onButton)
	return p
}

func (p *pointer) onEnter(serial uint32, surf *wl.Surface, sx, sy wl.Fixed) {
	ls := p.d.findSurface(surf)
	if ls == nil {
		return
	}
	p.focused = ls
	if !ls.state.Expanded {
		ls.state.Expanded = true
		ls.repaint(p.d)
	}
}

func (p *pointer) onLeave(serial uint32, surf *wl.Surface) {
	ls := p.d.findSurface(surf)
	if ls == nil {
		return
	}
	if p.focused == ls {
		p.focused = nil
	}
	if ls.state.Expanded {
		ls.state.Expanded = false
		ls.repaint(p.d)
	}
}

func (p *pointer) onButton(serial, time, button, state uint32) {
	const statePressed = 1
	if state != statePressed || p.focused == nil {
		return
	}
	cmd := ""
	switch button {
	case btnLeft:
		cmd = "jump"
	case btnMiddle:
		cmd = "ack"
	case btnRight:
		cmd = "dismiss"
	}
	if cmd == "" || p.focused.sessionID == "" {
		return
	}
	id := p.focused.sessionID
	go func() {
		_, err := ipc.Call(paths.Socket(), ipc.Request{Cmd: cmd, ID: id})
		if err != nil {
			slog.Warn("wlr ipc", "cmd", cmd, "err", err)
		}
	}()
}
```

- [ ] **Step 6.5: Store the session id on each `layerSurface`**

Modify `internal/hud/wlr/surface.go`:
```go
type layerSurface struct {
	// ... existing fields ...
	sessionID string
}
```

Pass it in via `newLayerSurface`:
```go
func newLayerSurface(d *dock, slot int, id string, st render.TongueState) (*layerSurface, error) {
	// ...
	ps := &layerSurface{surface: surf, layerSurface: ls, state: st, sessionID: id}
	// ...
}
```

Update `applySnapshot` in `dock.go` to pass the id:
```go
ls, err := newLayerSurface(d, i, s.ID, st)
```

- [ ] **Step 6.6: Add `findSurface` to dock**

```go
func (d *dock) findSurface(s *wl.Surface) *layerSurface {
	for _, ls := range d.surfaces {
		if ls.surface == s {
			return ls
		}
	}
	return nil
}
```

- [ ] **Step 6.7: Initialise the pointer in `newDock`**

After globals are bound:
```go
d.pointer = newPointer(d)
```
Add `pointer *pointer` to the `dock` struct.

- [ ] **Step 6.8: Build and manually verify**

```
go build ./...
./bin/visor daemon &
./bin/visor hud open --backend=wlr
```
Hover a tongue: the panel appears (full label visible). Move away: panel disappears. Right-click a needs-attention tongue: it transitions to dismissed. Middle-click: ack. Left-click: focus warps to the session via `internal/focus` (EWMH ClientMessage and/or tmux select-pane, depending on what was captured at SessionStart).

- [ ] **Step 6.9: Commit**

```
git add internal/hud/render/ internal/hud/wlr/ internal/hud/x11/tongue.go
git commit -m "feat(wlr): pointer input — hover expand, click ack/dismiss/jump"
```

---

## Task 7: Wire `wlr` into `pickBackend` and document

**Goal:** Make `--backend=wlr` officially selectable. Update `CLAUDE.md` to remove the "No Wayland backend" line.

**Files:**
- Modify: `cmd/visor/hud.go`
- Modify: `CLAUDE.md`

### Steps

- [ ] **Step 7.1: Add the `wlr` case to `pickBackend`**

```go
import (
	// ... existing imports ...
	"github.com/nitzanz/visor/internal/hud/wlr"
)

func pickBackend(name string) (hud.Backend, error) {
	switch name {
	case "", "eww":
		return eww.New(), nil
	case "x11":
		return x11.New(), nil
	case "wlr":
		return wlr.New(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}
```

Update the `--backend` flag's usage string:
```go
backendName := fs.String("backend", "eww", "HUD backend (eww|x11|wlr)")
```

- [ ] **Step 7.2: Update `CLAUDE.md`**

The "HUD backends." paragraph currently ends after the x11 description. Append a sentence describing the new backend:

> Add to the **HUD backends** paragraph in `CLAUDE.md` — at the end, before the `pickBackend` sentence:
>
> ```
> The `wlr` backend (`internal/hud/wlr/`) is a pure-Go Wayland-native dock using
> `codeberg.org/tesselslate/wl` + locally-generated `wlr-layer-shell-unstable-v1`
> bindings: one `zwlr_layer_surface_v1` per session, anchored right, ARGB8888
> wl_shm buffers, double-buffered with release tracking. Works on Niri, sway,
> hyprland, river, wayfire, labwc, KDE. GNOME has no layer-shell — use
> `--backend=x11` (Xwayland) there.
> ```

Update the HUD usage example block:
```
# HUD — picks a backend with --backend=eww (default), --backend=x11, or --backend=wlr
```

Also append a "Things that will bite you" bullet for the Wayland backend:
```
- **wlr buffer ownership.** The compositor owns each `wl_buffer` from `attach`+`commit` until it sends `wl_buffer.release`. Reusing a buffer earlier corrupts pixels silently. The shmPool tracks a `released` bool per buffer; never bypass it.
```

- [ ] **Step 7.3: Build, run the full test suite**

```
go build -o bin/visor ./cmd/visor
go test ./...
go vet ./...
```

- [ ] **Step 7.4: Manual verification matrix**

Run under each available compositor and confirm:

| Compositor | Tongue visible | Hover-expand | Right-click dismiss | Middle-click ack | Clean shutdown |
|---|---|---|---|---|---|
| Niri | ☐ | ☐ | ☐ | ☐ | ☐ |
| sway (optional) | ☐ | ☐ | ☐ | ☐ | ☐ |
| hyprland (optional) | ☐ | ☐ | ☐ | ☐ | ☐ |

Niri is the required target; sway/hyprland are nice-to-have for this PR.

- [ ] **Step 7.5: Commit**

```
git add cmd/visor/hud.go CLAUDE.md
git commit -m "feat: expose wlr as a HUD backend and update docs"
```

---

## Task 8: Final cleanup and PR

**Goal:** Run formatters, ensure go.sum is tidy, verify nothing else is dirty.

### Steps

- [ ] **Step 8.1: Tidy modules**
```
go mod tidy
```

- [ ] **Step 8.2: Format**
```
gofmt -w .
goimports -w .
```

- [ ] **Step 8.3: Lint**
```
go vet ./...
```
Expected: no output.

- [ ] **Step 8.4: Final commit if tidy changed anything**
```
git status
# If changes:
git add -A
git commit -m "chore: tidy modules and formatting"
```

- [ ] **Step 8.5: Open PR (optional, follow user's standing PR workflow)**
