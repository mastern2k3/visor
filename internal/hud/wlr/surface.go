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
	// tongueGap is the vertical space in pixels between adjacent tongues.
	tongueGap = 4

	// topOffset shifts the whole dock down from the top of the screen so it
	// doesn't sit under a top bar or overlap chrome the user wants to see.
	topOffset = 256

	// Wobble animation for "working" tongues — they breathe leftward (toward
	// the centre of the screen) with cosine easing. Each tongue gets a
	// randomized phase so adjacent tongues don't pulse in lockstep.
	wobbleAmp    = 4.0
	wobblePeriod = 0.9 // seconds for one full cycle

	// alertProtrusion: a session with attention=needs sits this many pixels
	// further from the right edge so it's distinguishable by shape alone, not
	// just colour. Chosen > wobbleAmp so a needs tongue is unambiguously
	// further out than any working tongue at its wobble peak.
	alertProtrusion = 8
)

// layerSurface is one tongue: a wl_surface + zwlr_layer_surface_v1 pair plus
// the shm pool that backs its frames.
//
// The static test surface is removed in Task 5.
type layerSurface struct {
	surface wl.Surface
	ls      protocol.LayerSurfaceV1
	pool    *shmPool
	log     *slog.Logger

	// State used to (re)paint on configure and pointer events.
	state render.TongueState

	// sessionID is the daemon session ID used to route IPC commands (ack,
	// dismiss, jump) from pointer click events.
	sessionID string

	// Raw daemon state needed to drive animation. Kept alongside the rendered
	// TongueState so the renderer stays pure.
	activity  string
	attention string

	// Slot and current applied right margin (in px from the screen edge). Tracked
	// so the animation tick can detect changes and avoid unnecessary commits.
	slot         int
	rightMargin  int32
	wobbleStart  time.Time
	wobblePhase  float64

	// dirty is true when a state change happened but the most recent repaint
	// couldn't acquire a buffer (both were in-flight). The next wl_buffer.release
	// event will retry the repaint via the pool's onRelease callback.
	// Only touched on the Wayland dispatch goroutine.
	dirty bool

	// d is a back-pointer to the dock, needed so the pool's onRelease callback
	// can call repaint without an extra closure argument.
	d *dock

	// Input regions: the surface is ExpandedW wide but most of it is
	// transparent when collapsed. Without an input region the compositor would
	// fire pointer Enter when the cursor crossed the invisible panel area,
	// expanding the tongue before the cursor reached the visible strip.
	//   regionTongue: rightmost TongueW px only (active while collapsed).
	//   regionFull:   entire surface (active while expanded, so the cursor
	//                 can move onto the panel without firing Leave).
	regionTongue wl.Region
	regionFull   wl.Region
}

// newLayerSurface creates a wl_surface + zwlr_layer_surface_v1, configures
// layer-shell properties (anchor, size, etc.), sets the configure listener,
// and commits with no buffer attached to trigger the first configure event.
// The compositor calls our configure handler before mapping the surface; we
// ack there and attach the first frame.
func newLayerSurface(d *dock, slot int, id, activity, attention string, st render.TongueState) (*layerSurface, error) {
	surf := d.compositor.CreateSurface()
	ls := d.layerShell.GetLayerSurface(
		surf,
		d.output,
		protocol.LayerShellV1LayerOverlay,
		"visor-tongue",
	)

	// Anchor to the top-right corner; margin_top stacks tongues vertically.
	// ExclusiveZone -1: float above all reserved struts, don't push others.
	ls.SetAnchor(protocol.LayerSurfaceV1AnchorTop | protocol.LayerSurfaceV1AnchorRight)
	ls.SetSize(uint32(render.ExpandedW), uint32(render.TongueH))
	ls.SetExclusiveZone(-1)
	initialRight := restRightMargin(attention)
	ls.SetMargin(int32(slotTopMargin(slot)), initialRight, 0, 0) // top, right, bottom, left
	ls.SetKeyboardInteractivity(protocol.LayerSurfaceV1KeyboardInteractivityNone)

	// Pre-build the two input regions used to gate pointer Enter/Leave.
	regionTongue := d.compositor.CreateRegion()
	regionTongue.Add(int32(render.ExpandedW-render.TongueW), 0, int32(render.TongueW), int32(render.TongueH))
	regionFull := d.compositor.CreateRegion()
	regionFull.Add(0, 0, int32(render.ExpandedW), int32(render.TongueH))

	ps := &layerSurface{
		surface:      surf,
		ls:           ls,
		state:        st,
		sessionID:    id,
		activity:     activity,
		attention:    attention,
		log:          d.log,
		d:            d,
		regionTongue: regionTongue,
		regionFull:   regionFull,
		slot:         slot,
		rightMargin:  initialRight,
		wobbleStart:  time.Now(),
		wobblePhase:  rand.Float64() * 2 * math.Pi,
	}

	// Start with the tongue-only input region — newly-created surfaces are
	// always collapsed.
	surf.SetInputRegion(regionTongue)

	// The configure handler: ack the serial and paint the first frame.
	// Subsequent configure events (e.g. output scale changes) also repaint.
	ls.SetListener(protocol.LayerSurfaceV1Listener{
		Configure: func(_ any, _ protocol.LayerSurfaceV1, serial uint32, w uint32, h uint32) error {
			ps.ls.AckConfigure(serial)
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

// repaint acquires a buffer, renders the current state via render.DrawTongue,
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
	img := render.DrawTongue(s.state, d.font)
	buf.CopyRGBA(img.RGBA)
	s.surface.Attach(buf.Wl, 0, 0)
	s.surface.Damage(0, 0, int32(render.ExpandedW), int32(render.TongueH))
	// Match input region to visible area: tongue strip only when collapsed,
	// full surface when expanded so the cursor can drift onto the panel.
	if s.state.Expanded {
		s.surface.SetInputRegion(s.regionFull)
	} else {
		s.surface.SetInputRegion(s.regionTongue)
	}
	s.surface.Commit()
	s.dirty = false
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

// computeRightMargin returns the right-margin (px from screen edge) for the
// surface at time `now`. Base = alertProtrusion if attention=needs else 0.
// Working sessions add a cosine-eased wobble on top of the base.
func (s *layerSurface) computeRightMargin(now time.Time) int32 {
	base := int32(0)
	if s.attention == "needs" {
		base = alertProtrusion
	}
	if s.activity == "working" {
		elapsed := now.Sub(s.wobbleStart).Seconds()
		// (1 - cos)/2 maps to [0, 1] with zero derivative at the endpoints.
		t01 := (1 - math.Cos(elapsed*2*math.Pi/wobblePeriod+s.wobblePhase)) / 2
		return base + int32(math.Round(wobbleAmp*t01))
	}
	return base
}

// restRightMargin is the static right-margin used at surface creation time
// before animation kicks in.
func restRightMargin(attention string) int32 {
	if attention == "needs" {
		return alertProtrusion
	}
	return 0
}

// slotTopMargin converts a slot index into a top-margin in px, including the
// global topOffset and the per-tongue gap.
func slotTopMargin(slot int) int {
	return topOffset + slot*(render.TongueH+tongueGap)
}

// destroy tears down the layer surface and releases the shm pool.
// Destroy order matters: destroy the layer_surface protocol object before the
// underlying wl_surface to avoid a protocol error.
func (s *layerSurface) destroy() {
	if s.pool != nil {
		s.pool.close()
		s.pool = nil
	}
	s.regionTongue.Destroy()
	s.regionFull.Destroy()
	// Destroy layer_surface before wl_surface (protocol requirement).
	s.ls.Destroy()
	s.surface.Destroy()
	s.log.Debug("layerSurface destroyed")
}
