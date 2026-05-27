package wlr

import (
	"fmt"
	"log/slog"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/hud/wlr/protocol"
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
}

// newLayerSurface creates a wl_surface + zwlr_layer_surface_v1, configures
// layer-shell properties (anchor, size, etc.), sets the configure listener,
// and commits with no buffer attached to trigger the first configure event.
// The compositor calls our configure handler before mapping the surface; we
// ack there and attach the first frame.
func newLayerSurface(d *dock, slot int, id string, st render.TongueState) (*layerSurface, error) {
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
	ls.SetMargin(int32(slot*render.TongueH), 0, 0, 0) // top, right, bottom, left
	ls.SetKeyboardInteractivity(protocol.LayerSurfaceV1KeyboardInteractivityNone)

	ps := &layerSurface{
		surface:   surf,
		ls:        ls,
		state:     st,
		sessionID: id,
		log:       d.log,
	}

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

	return ps, nil
}

// repaint acquires a buffer, renders the current state via render.DrawTongue,
// attaches it, damages the full surface, and commits.  A nil Acquire means
// both buffers are still in-flight; we silently drop the frame.
func (s *layerSurface) repaint(d *dock) {
	buf := s.pool.Acquire()
	if buf == nil {
		d.log.Debug("both shm buffers in-flight; dropping frame")
		return
	}
	img := render.DrawTongue(s.state, d.font)
	buf.CopyRGBA(img.RGBA)
	s.surface.Attach(buf.Wl, 0, 0)
	s.surface.Damage(0, 0, int32(render.ExpandedW), int32(render.TongueH))
	s.surface.Commit()
}

// setSlot updates the surface's vertical position. Each surface commits
// independently — wl_surface.commit is per-surface by protocol; there is no
// batch primitive at this layer.
// Must be called from the Wayland dispatch goroutine.
func (s *layerSurface) setSlot(slot int) {
	s.ls.SetMargin(int32(slot*render.TongueH), 0, 0, 0)
	s.surface.Commit()
}

// destroy tears down the layer surface and releases the shm pool.
// Destroy order matters: destroy the layer_surface protocol object before the
// underlying wl_surface to avoid a protocol error.
func (s *layerSurface) destroy() {
	if s.pool != nil {
		s.pool.close()
		s.pool = nil
	}
	// Destroy layer_surface before wl_surface (protocol requirement).
	s.ls.Destroy()
	s.surface.Destroy()
	s.log.Debug("layerSurface destroyed")
}
