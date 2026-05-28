# Wayland HUD Backend — Design

Status: Draft (brainstorm output, pre-implementation)
Date: 2026-05-27

## Goal

Add a third HUD backend, `wlr`, that renders the Visor dock natively on
wlr-layer-shell compositors (Niri, sway, hyprland, river, wayfire, labwc, KDE).
The backend mirrors the existing X11 backend's behavior: one tab per session,
anchored to the right screen edge, hover to expand, click to ack/dismiss/jump.

Out of scope for this iteration: GNOME (no layer-shell), multi-output selection,
fractional-scale, keyboard accelerators, auto-detection of compositor.

## Non-goals

- A general-purpose Wayland UI toolkit. We only need a fixed dock.
- Replacing the X11 or eww backends.
- A cgo dependency on `libwayland-client`. The single-static-binary promise
  from `CLAUDE.md` is load-bearing.

## Dependencies

- **`codeberg.org/tesselslate/wl`** (pinned to a specific commit/tag) —
  pure-Go Wayland protocol runtime + core bindings. Single transitive dep
  (`golang.org/x/sys`). Includes `cmd/scanner` for generating bindings from
  protocol XML.
- **Generated bindings** checked into the repo (no runtime code generation):
  - `wlr-layer-shell-unstable-v1` — generated locally by running
    `tesselslate/wl/cmd/scanner` against the XML from
    `gitlab.freedesktop.org/wlroots/wlr-protocols`. The generated file is
    checked in; the source XML is also checked in next to it for
    reproducibility.

No cgo. No system Wayland libraries required at build or run time.

## Package layout

```
internal/hud/render/        # NEW — shared backend-agnostic drawing
  tab.go                 # DrawTab(state, dims) → *image.RGBA
  font.go                   # Font loading (moved from x11/font.go)

internal/hud/wlr/           # NEW — Wayland backend
  wlr.go                    # Backend impl (Name/Install/Open/Close)
  dock.go                   # Display connect, registry, event loop, lifecycle
  surface.go                # One layer_surface per session-tab
  buffer.go                 # wl_shm pool, double-buffered RGBA frames
  input.go                  # wl_pointer: hover → expand, click → ack/dismiss/jump
  subscribe.go              # Consumes snapshot stream from daemon
  protocol/                 # Generated, do-not-edit-by-hand
    layer_shell_v1.go
    layer_shell_v1.xml      # Source XML for reproducibility

internal/hud/x11/           # Modified to call internal/hud/render
  dock.go                   # unchanged
  tab.go                 # slimmed: delegates pixel drawing to render package
  font.go                   # DELETED (moved to render)
  ...

cmd/visor/hud.go            # pickBackend gains a "wlr" case
```

## Shared render extraction

X11 currently draws each tab into an `xgraphics.Image`, which wraps an
`image.RGBA`. The pixel-level drawing (background fill, accent stripe, status
glyph, session label, hover-expand layout) is identical to what the Wayland
backend needs. Without extraction we'd duplicate ~200 LOC.

The new `internal/hud/render` package exposes a small API:

```go
type TabState struct {
    Activity   state.Activity
    Waiting    state.Waiting
    Attention  state.Attention
    DisplayCwd string
    Expanded   bool
}

type TabDims struct {
    CollapsedW, ExpandedW int
    Height                int
    Scale                 int  // for HiDPI, pass 1 for now
}

func DrawTab(s TabState, d TabDims, fnt *truetype.Font) *image.RGBA
```

- `DrawTab` is pure: takes inputs, returns a fresh `*image.RGBA`. No I/O,
  no global state. Trivial to unit-test (compare a known state to a golden
  image).
- The X11 backend wraps the result in an `xgraphics.Image` and uploads as a
  pixmap. The Wayland backend copies the pixels into a `wl_shm` buffer.
- Font loading also moves to `render`. Both backends call
  `render.LoadFont()` once at startup.

This is the scoped cleanup we discussed: it serves the current goal (avoid
duplicating drawing in the Wayland backend) and improves the X11 backend by
making the drawing layer testable in isolation. We do not touch unrelated
parts of X11.

## Architecture

### Connection and registry

`dock.Run(ctx)` connects to `$WAYLAND_DISPLAY` via `wl.ConnectDisplay`. On the
registry it binds:

- `wl_compositor` (for surfaces)
- `wl_shm` (for buffers)
- `wl_seat` (for pointer input)
- `zwlr_layer_shell_v1` (the dock surfaces)
- `wl_output` (one — first announced, see Output selection below)

If `zwlr_layer_shell_v1` is missing from the registry, `Open` returns an error
naming the protocol; the user can fall back to `--backend=x11`.

### Surfaces

One `zwlr_layer_surface_v1` per session, role `OVERLAY`, anchored to the right
edge of the output. Each surface:

- Sets `keyboard_interactivity = NONE`.
- Sets `exclusive_zone = -1` (don't reserve, don't be pushed by exclusives).
- Sets a fixed initial size `(collapsedW, tabHeight)`. The compositor sends
  `configure` with the size it actually wants; we ack and use that.
- Sets `margin.top` to position the tab vertically. Tabs stack
  top-to-bottom by `Snapshot` order — same as X11.

Hover-expand: on `wl_pointer.enter` we request the surface to resize to
`(expandedW, tabHeight)` by `set_size` + commit. On `leave` we resize back.
The compositor's `configure` response is authoritative for the final size.

### Buffers

`wl_shm_pool` per surface, two `wl_buffer`s of size `expandedW * tabHeight *
4` bytes (always sized for the larger state — we render the actual content
inside, padding with transparent pixels when collapsed).

Ping-pong: never reuse a buffer until the compositor sends `wl_buffer.release`.
Tracked with a `released bool` per buffer. If both buffers are in-flight when
a redraw is requested, we drop the redraw — the next compositor frame callback
will pick up the latest state.

### Input

Single `wl_pointer` bound from the seat:

- `enter` / `leave` → toggle hover state on the targeted surface, mark for
  redraw.
- `button` press of `BTN_LEFT` → look up the session id stored in the surface's
  userdata, send `ack` (or `jump` if already acked — same heuristic as X11).
- `button` press of `BTN_MIDDLE` or `BTN_RIGHT` → send `dismiss`.

The IPC call uses the same one-shot `ipc.Call` helper the X11 backend uses.

### State subscription

The subscription loop currently lives in `internal/hud/x11/subscribe.go`. Both
backends do the same thing: spawn `visor ctl watch`, parse newline-delimited
JSON, diff against the current surface set, create/destroy/redraw as needed.

Decision: keep `subscribe.go` duplicated for now (it's ~95 LOC and the
diff-against-surfaces logic is backend-specific in the parts that matter). If a
third caller appears we extract it. This avoids dragging a refactor into the
Wayland PR that isn't necessary.

### Event loop and shutdown

The Wayland event loop is `wl.Display.Dispatch()` blocking on the wayland fd.
Shutdown via context cancellation: a goroutine watching `ctx.Done()` calls
`Display.Close()`, which unblocks `Dispatch` with EOF. This mirrors X11's
synthetic-ClientMessage trick from `CLAUDE.md`.

Two goroutines per backend instance:

1. Main: Wayland event dispatch.
2. Subscriber: reads `visor ctl watch`, posts state updates to the main
   goroutine over a channel. The main goroutine owns all Wayland objects.

State updates from the subscriber are serialized through a channel — Wayland
objects are not goroutine-safe.

### Wiring into `visor hud`

- `cmd/visor/hud.go::pickBackend` adds a `case "wlr"`.
- Default selection is unchanged: `eww` remains the default. The user
  selects `--backend=wlr` explicitly. Auto-detection (pick `wlr` if
  `WAYLAND_DISPLAY` is set and no explicit backend was requested) is a
  separate follow-up after the backend is proven.

## Known pitfalls

- **Buffer release timing.** The compositor owns the buffer between
  `attach`+`commit` and the `wl_buffer.release` event. Touching it earlier is
  a use-after-free that often manifests as silent pixel corruption rather
  than a crash. Two buffers, strict release tracking.

- **Configure ack ordering.** A layer surface is not mapped until we ack the
  first `configure`. The initial commit (with an empty buffer or no buffer)
  must come *before* the configure, then we ack with `set_size` to our
  desired dimensions, then we attach the real buffer and commit again.

- **Hover resize is not free.** Some compositors take a few frames to honor a
  `set_size`; treat the configure event as authoritative and re-render at the
  size the compositor gave us, not the size we asked for.

- **fsnotify-style "first event lost" gotchas don't apply here**, but
  `wl_callback` (frame callbacks) do not auto-rearm — we have to request a new
  one on every commit if we want vsynced redraws.

- **Output hot-plug.** Initial implementation binds the first announced
  output. If the user disconnects that output, the surfaces go invalid. We
  log and exit; the user restarts the HUD. Multi-output support is a
  separate task.

## Testing

- **`internal/hud/render` unit tests** (table-driven, no Wayland required):
  for each combination of `(Activity, Waiting, Attention, Expanded)`, render
  to RGBA and assert key invariants — accent-stripe color matches state,
  expanded layout contains a non-empty label region, etc. Optional golden
  image comparison with `go test -update` style flag.

- **No automated Wayland integration test.** Visor has no CI and no test
  suite per CLAUDE.md; adding a headless compositor harness is out of scope.
  Manual verification: run `./bin/visor daemon` + `./bin/visor hud open
  --backend=wlr` under Niri, sway, and hyprland; verify tab placement,
  hover-expand, click-to-ack, click-to-dismiss, clean shutdown on SIGINT.

## Implementation order (rough)

1. Extract `internal/hud/render` from `internal/hud/x11`. X11 keeps working;
   no behavior change.
2. Generate and check in `wlr-layer-shell-unstable-v1` bindings via
   `tesselslate/wl/cmd/scanner`. Vendor `xdg-shell` bindings.
3. `internal/hud/wlr/dock.go` — connect, bind globals, dispatch loop, clean
   shutdown.
4. `internal/hud/wlr/surface.go` + `buffer.go` — one static tab, fixed
   size, drawn from `render.DrawTab`, mapped on the right edge.
5. Subscribe loop — create/destroy/redraw surfaces as the snapshot changes.
6. `internal/hud/wlr/input.go` — hover-expand and click-to-ack/dismiss/jump.
7. `cmd/visor/hud.go` — `pickBackend` case.
8. Manual verification on Niri (primary), sway, hyprland.

Each step lands as its own commit; the writing-plans skill will turn this
into a concrete checklist with TDD gates where applicable.
