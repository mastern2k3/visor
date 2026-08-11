package wlr

import (
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/hud/wlr/protocol"
)

const (
	// topOffset shifts the whole dock down from the top of the screen so it
	// doesn't sit under a top bar or overlap chrome the user wants to see.
	topOffset = 256

	// Animation constants come from render so both backends share them.
	// Working tabs breathe leftward (toward the centre of the screen) with
	// cosine easing, each with a randomized phase so adjacent tabs don't pulse
	// in lockstep. A session with attention=needs sits alertProtrusion px
	// further from the right edge so it's distinguishable by shape alone.
	wobbleAmp       = render.WobbleAmp
	wobblePeriod    = render.WobblePeriod
	alertProtrusion = render.AlertProtrusion

	// tabOverflow is how many pixels the surface extends past the screen's
	// right edge at the rest position, so that even at peak shift (alert +
	// wobble) the capsule's right edge is still flush with or beyond the screen
	// edge — wobble/alert always grow the visible tab leftward instead of
	// revealing empty space.
	//
	// It is exactly render.MaxProtrusion, which is also the amount by which
	// render draws the capsule wider than CapsuleW. Those two have to be the
	// same number or the overhang and the overflow disagree, so this aliases
	// the render constant rather than recomputing the sum.
	tabOverflow = render.MaxProtrusion
)

// layerSurface is one tab: a wl_surface + zwlr_layer_surface_v1 pair plus
// the shm pool that backs its frames.
type layerSurface struct {
	surface wl.Surface
	ls      protocol.LayerSurfaceV1
	pool    *shmPool
	log     *slog.Logger

	// State used to (re)paint on configure and pointer events.
	state render.TabState

	// sessionID is the daemon session ID used to route IPC commands (ack,
	// dismiss, jump) from pointer click events.
	sessionID string

	// Raw daemon state needed to drive animation. Kept alongside the rendered
	// TabState so the renderer stays pure.
	activity  string
	attention string

	// Slot and current applied right margin (in px from the screen edge). Tracked
	// so the animation tick can detect changes and avoid unnecessary commits.
	slot        int
	rightMargin int32
	wobbleStart time.Time
	wobblePhase float64

	// stateSince is the raw daemon timestamp state.Elapsed was last derived
	// from. Kept separately from state.Elapsed (which is a computed,
	// quantised value baked into the TabState) so tickElapsed can recompute
	// "now - stateSince" on every animation tick without a full snapshot.
	stateSince time.Time
	// lastElapsed is the last elapsed-time string actually painted, so
	// tickElapsed only repaints when the visible text would change.
	lastElapsed string
	// lastHaloStep is the last-painted render.HaloSteps index of the
	// permission halo pulse, so tickHalo only repaints when the quantised
	// step actually advances. wobbleStart doubles as the halo's phase epoch —
	// working (wobble) and permission (halo) are mutually exclusive states
	// per Palette.For's precedence, so sharing one reference time causes no
	// interference between the two animations.
	lastHaloStep int

	// dirty is true when a state change happened but the most recent repaint
	// couldn't acquire a buffer (both were in-flight). The next wl_buffer.release
	// event will retry the repaint via the pool's onRelease callback.
	// Only touched on the Wayland dispatch goroutine.
	dirty bool

	// d is a back-pointer to the dock, needed so the pool's onRelease callback
	// can call repaint without an extra closure argument.
	d *dock

	// Input regions: the surface is the full buffer wide but most of it is
	// transparent when collapsed. Without an input region the compositor would
	// fire pointer Enter when the cursor crossed the invisible panel area,
	// expanding the tab before the cursor reached the visible strip.
	//   regionTab: the capsule only (active while collapsed).
	//   regionFull:   entire surface (active while expanded, so the cursor
	//                 can move onto the panel without firing Leave).
	regionTab  wl.Region
	regionFull wl.Region
}

// newLayerSurface creates a wl_surface + zwlr_layer_surface_v1, configures
// layer-shell properties (anchor, size, etc.), sets the configure listener,
// and commits with no buffer attached to trigger the first configure event.
// The compositor calls our configure handler before mapping the surface; we
// ack there and attach the first frame.
func newLayerSurface(d *dock, slot int, id, activity, attention string, st render.TabState) (*layerSurface, error) {
	surf := d.compositor.CreateSurface()
	ls := d.layerShell.GetLayerSurface(
		surf,
		d.output,
		protocol.LayerShellV1LayerOverlay,
		"visor-tab",
	)

	// Anchor to the top-right corner; margin_top stacks tabs vertically.
	// ExclusiveZone -1: float above all reserved struts, don't push others.
	ls.SetAnchor(protocol.LayerSurfaceV1AnchorTop | protocol.LayerSurfaceV1AnchorRight)
	// The buffer includes render.ShadowPad of transparent padding on the left,
	// top and bottom, so the surface must be BufW x BufH — sizing it to the
	// capsule/panel content height alone would not match the attached buffer.
	//
	// It is exactly BufW: render now draws the capsule tabOverflow px wider than
	// it shows at rest, so the overhang lives inside the rendered buffer. It
	// used to be BufW+tabOverflow with those columns synthesised by replicating
	// the last rendered one; doing both would double-count the overflow and show
	// CapsuleDrawW rather than CapsuleW at rest.
	ls.SetSize(uint32(render.BufW), uint32(render.BufH))
	ls.SetExclusiveZone(-1)
	initialRight := restRightMargin(attention)
	ls.SetMargin(int32(slotTopMargin(slot)), initialRight, 0, 0) // top, right, bottom, left
	ls.SetKeyboardInteractivity(protocol.LayerSurfaceV1KeyboardInteractivityNone)

	// Pre-build the two input regions used to gate pointer Enter/Leave.
	// The surface is BufW wide and the capsule occupies its rightmost
	// CapsuleDrawW columns, running out past the screen edge. Input is only
	// sensitive in surface-local coordinates that correspond to the *opaque*
	// region — which also excludes the transparent shadow padding above/below.
	regionTab := d.compositor.CreateRegion()
	regionTab.Add(
		int32(render.BufW-render.CapsuleDrawW), int32(render.ShadowPad),
		int32(render.CapsuleDrawW), int32(render.ContentH),
	)
	regionFull := d.compositor.CreateRegion()
	regionFull.Add(
		0, int32(render.ShadowPad),
		int32(render.BufW), int32(render.ContentH),
	)

	ps := &layerSurface{
		surface:     surf,
		ls:          ls,
		state:       st,
		sessionID:   id,
		activity:    activity,
		attention:   attention,
		log:         d.log,
		d:           d,
		regionTab:   regionTab,
		regionFull:  regionFull,
		slot:        slot,
		rightMargin: initialRight,
		wobbleStart: time.Now(),
		wobblePhase: rand.Float64() * 2 * math.Pi,
	}

	// Start with the tab-only input region — newly-created surfaces are
	// always collapsed.
	surf.SetInputRegion(regionTab)

	// The configure handler: ack the serial and paint the first frame.
	// Subsequent configure events (e.g. output scale changes) also repaint.
	ls.SetListener(protocol.LayerSurfaceV1Listener{
		Configure: func(_ any, _ protocol.LayerSurfaceV1, serial uint32, w uint32, h uint32) error {
			ps.ls.AckConfigure(serial)
			d.log.Debug("layer surface configure",
				"session", ps.sessionID,
				"want_w", render.BufW,
				"want_h", render.BufH,
				"got_w", w, "got_h", h)
			ps.repaint(d)
			return nil
		},
		Closed: func(_ any, _ protocol.LayerSurfaceV1) error {
			// Compositor told us to go away. For now, log and ignore — Task 5
			// will plumb this into the dock's surface map for proper cleanup.
			d.log.Info("layer surface closed by compositor")
			return nil
		},
	}, nil)

	// Initial commit with no buffer attached triggers the first configure event
	// from the compositor.  We must not attach a buffer before this.
	surf.Commit()

	pool, err := newShmPool(&d.shm)
	if err != nil {
		ls.Destroy()
		surf.Destroy()
		return nil, fmt.Errorf("shm pool: %w", err)
	}
	ps.pool = pool

	// Wire retry-on-release. When a buffer is returned by the compositor,
	// if ps has a pending dirty repaint, retry it now.
	pool.onRelease = func() {
		if ps.dirty {
			ps.repaint(ps.d)
		}
	}

	return ps, nil
}

// repaint acquires a buffer, renders the current state via render.DrawTab,
// attaches it, damages the full surface, and commits.  A nil Acquire means
// both buffers are still in-flight; we mark dirty=true so the next
// wl_buffer.release event retries via pool.onRelease.
func (s *layerSurface) repaint(d *dock) {
	buf := s.pool.Acquire()
	if buf == nil {
		s.dirty = true
		d.log.Debug("both shm buffers in-flight; will retry on release",
			"session", s.sessionID, "expanded", s.state.Expanded)
		return
	}
	img := render.DrawTab(s.state, d.faces, d.palette)
	buf.CopyRGBA(img.RGBA)
	s.surface.Attach(buf.Wl, 0, 0)
	// DamageBuffer uses buffer-local coords (no scale/transform mapping) and is
	// the recommended request for modern clients. Cover the full buffer — it is
	// render.BufW × render.BufH, which grew when the capsule became
	// CapsuleDrawW wide, so this must stay expressed in the constants.
	s.surface.DamageBuffer(0, 0, int32(bufW), int32(bufH))
	s.setInputRegion(d)
	s.surface.Commit()
	s.dirty = false
}

// setInputRegion matches the sensitive area to what this surface should
// currently catch. It does NOT commit; callers batch it with whatever else
// they are committing.
//
// Collapsed and disarmed, only the capsule is sensitive — without that, the
// compositor would fire pointer Enter as soon as the cursor crossed the
// invisible panel area and expand the tab before the cursor reached the
// visible strip.
//
// The exception is armed browsing (see internal/hud/browse). Once the user has
// deliberately hovered one tab, every collapsed surface widens to its full
// width so the cursor can move straight down from an open panel onto the next
// row instead of travelling back to the screen edge. Because the surfaces
// occupy distinct, contiguous y bands, the compositor's own pointer focus does
// all the row hit-testing for us — this is the entire wlr side of the feature,
// and it is why there is no counterpart to the x11 backend's catch window.
//
// This must consult the dock's armed flag rather than state.Expanded alone: a
// repaint for any other reason (state change, elapsed tick, theme reload) would
// otherwise silently narrow an armed surface back to the capsule mid-browse.
func (s *layerSurface) setInputRegion(d *dock) {
	if s.state.Expanded || d.armed {
		s.surface.SetInputRegion(s.regionFull)
		return
	}
	s.surface.SetInputRegion(s.regionTab)
}

// setSlot updates the surface's vertical position. Each surface commits
// independently — wl_surface.commit is per-surface by protocol; there is no
// batch primitive at this layer.
// Must be called from the Wayland dispatch goroutine.
func (s *layerSurface) setSlot(slot int) {
	s.slot = slot
	s.ls.SetMargin(int32(slotTopMargin(slot)), s.rightMargin, 0, 0)
	s.surface.Commit()
}

// animateTick recomputes the right-margin based on the current activity /
// attention state and the elapsed time, then commits if it changed. Returns
// true when a commit was issued. Called from the dock's event loop.
func (s *layerSurface) animateTick(now time.Time) bool {
	target := s.computeRightMargin(now)
	if target == s.rightMargin {
		return false
	}
	s.rightMargin = target
	s.ls.SetMargin(int32(slotTopMargin(s.slot)), target, 0, 0)
	s.surface.Commit()
	return true
}

// computeRightMargin returns the right-margin (in protocol units — positive
// values push the surface away from the right anchor, negative push it past).
// Starts at -tabOverflow (surface overflows the screen edge) and moves
// rightward (toward the screen edge) as alert/wobble shifts grow. Because the
// capsule is drawn CapsuleDrawW = CapsuleW + tabOverflow wide, the visible
// capsule width is CapsuleDrawW plus this margin, and its right edge is welded
// to the screen edge throughout:
//
//	rest:        -tabOverflow                 → visible width = CapsuleW
//	needs:       -tabOverflow + alertProtrusion → CapsuleW + alertProtrusion
//	working:     adds cosine-eased wobble [0, wobbleAmp] on top of base;
//	             at the peak the margin reaches 0 and the full CapsuleDrawW shows
func (s *layerSurface) computeRightMargin(now time.Time) int32 {
	base := -int32(tabOverflow)
	if s.attention == "needs" {
		base += alertProtrusion
	}
	if s.activity == "working" {
		elapsed := now.Sub(s.wobbleStart).Seconds()
		// (1 - cos)/2 maps to [0, 1] with zero derivative at the endpoints.
		t01 := (1 - math.Cos(elapsed*2*math.Pi/wobblePeriod+s.wobblePhase)) / 2
		return base + int32(math.Round(wobbleAmp*t01))
	}
	return base
}

// stateElapsed/elapsedChanged/haloPhaseStep used to live here, duplicated
// verbatim in internal/hud/x11/tab.go. Task 8 review flagged the duplication
// (none of the three touch anything wlr-specific) and asked for the logic to
// be hoisted into internal/hud/render alongside ElapsedString/HaloPeriod/
// HaloSteps, which it already depended on. See render.Elapsed /
// render.ElapsedChanged / render.HaloPhaseStep.

// tickElapsed repaints an expanded surface when its elapsed label would
// change. Collapsed surfaces draw no panel text (state.Expanded gates
// drawPanelText in render.DrawTab), so they never need this — which keeps
// the steady-state cost at zero when nothing is hovered.
func (s *layerSurface) tickElapsed(now time.Time, d *dock) {
	if !s.state.Expanded {
		return
	}
	changed, str := render.ElapsedChanged(s.stateSince, now, s.lastElapsed)
	if !changed {
		return
	}
	s.lastElapsed = str
	s.state.Elapsed = render.Elapsed(s.stateSince, now)
	s.repaint(d)
}

// tickHalo repaints a surface whose current state glows (permission only)
// when the quantised halo step advances. Unlike tickElapsed this is not
// gated on Expanded: the halo pulses on the capsule itself, which is visible
// at rest. Non-glowing surfaces return immediately without even computing a
// step, so a collapsed, non-permission surface never repaints on the
// animation tick.
func (s *layerSurface) tickHalo(now time.Time, d *dock) {
	if !d.palette.For(s.state.Activity, s.state.Attention, s.state.Waiting).Glow {
		return
	}
	step, phase := render.HaloPhaseStep(s.wobbleStart, now)
	if step == s.lastHaloStep {
		return
	}
	s.lastHaloStep = step
	s.state.HaloPhase = phase
	s.repaint(d)
}

// restRightMargin is the static right-margin used at surface creation time
// before animation kicks in.
func restRightMargin(attention string) int32 {
	base := -int32(tabOverflow)
	if attention == "needs" {
		base += alertProtrusion
	}
	return base
}

// slotTopMargin converts a slot index into a top-margin in px, including the
// global topOffset. render.RowPitch already leaves room for each tab's shadow
// padding, so no extra gap is added here.
func slotTopMargin(slot int) int {
	return topOffset + slot*render.RowPitch
}

// destroy tears down the layer surface and releases the shm pool.
// Destroy order matters: destroy the layer_surface protocol object before the
// underlying wl_surface to avoid a protocol error.
func (s *layerSurface) destroy() {
	if s.pool != nil {
		s.pool.close()
		s.pool = nil
	}
	s.regionTab.Destroy()
	s.regionFull.Destroy()
	// Destroy layer_surface before wl_surface (protocol requirement).
	s.ls.Destroy()
	s.surface.Destroy()
	s.log.Debug("layerSurface destroyed")
}
