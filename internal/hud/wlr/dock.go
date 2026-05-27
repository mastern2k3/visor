package wlr

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"codeberg.org/tesselslate/wl"
	"github.com/BurntSushi/freetype-go/freetype/truetype"

	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/hud/wlr/protocol"
)

// maxVersion caps the version we request when binding globals. We only need
// v1 features for all globals we bind here; requesting more than the
// compositor offers is a protocol error.
const (
	maxCompositorVersion = 4
	maxShmVersion        = 1
	maxSeatVersion       = 7
	maxOutputVersion     = 3
	maxLayerShellVersion = 4
)

type dock struct {
	log *slog.Logger

	// Wayland connection + registry.
	display  *wl.Display
	registry wl.Registry

	// Bound globals.
	compositor wl.Compositor
	shm        wl.Shm
	seat       wl.Seat
	output     wl.Output
	layerShell protocol.LayerShellV1

	// Which globals were observed during initial roundtrip.
	hasCompositor, hasShm, hasSeat, hasOutput, hasLayerShell bool

	// Font used by layerSurface.repaint. Nil if font load failed (tongues
	// will show background colour only, without text labels).
	font *truetype.Font

	// surfaces maps session id → layer surface. Driven by daemon snapshots.
	surfaces map[string]*layerSurface
}

func newDock() (*dock, error) {
	d := &dock{
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	// Connect to the Wayland display. NewDisplay("") falls back to
	// WAYLAND_DISPLAY then "wayland-0".
	disp, err := wl.NewDisplay("")
	if err != nil {
		return nil, fmt.Errorf("wl.NewDisplay: %w", err)
	}
	d.display = disp

	// Get the registry; register our global handler before the roundtrip so
	// we receive all currently-present globals.
	d.registry = d.display.GetRegistry()
	d.registry.SetListener(wl.RegistryListener{
		Global:       d.onGlobal,
		GlobalRemove: d.onGlobalRemove,
	}, nil)

	// Roundtrip pumps the initial burst of wl_registry.global events so all
	// currently-advertised globals are bound before we return.
	if err := d.display.Roundtrip(); err != nil {
		_ = d.display.Close()
		return nil, fmt.Errorf("registry roundtrip: %w", err)
	}

	// Validate that we received all required globals.
	if !d.hasCompositor {
		_ = d.display.Close()
		return nil, fmt.Errorf("compositor missing wl_compositor global")
	}
	if !d.hasShm {
		_ = d.display.Close()
		return nil, fmt.Errorf("compositor missing wl_shm global")
	}
	if !d.hasSeat {
		_ = d.display.Close()
		return nil, fmt.Errorf("compositor missing wl_seat global")
	}
	if !d.hasLayerShell {
		_ = d.display.Close()
		return nil, fmt.Errorf("compositor missing zwlr_layer_shell_v1 (GNOME? try --backend=x11)")
	}
	if !d.hasOutput {
		_ = d.display.Close()
		return nil, fmt.Errorf("no wl_output advertised by compositor")
	}

	// Load font; failure is non-fatal — tongues show background colour only.
	if f, err := render.LoadFont(); err != nil {
		d.log.Warn("font load failed; tongues will be blank", "err", err)
	} else {
		d.font = f
	}

	d.surfaces = map[string]*layerSurface{}

	d.log.Info("wayland connected")
	return d, nil
}

// onGlobal is invoked for every wl_registry.global event during the initial
// roundtrip and any time the compositor announces a new global afterwards.
// We bind the first instance of each global we care about; later
// announcements (e.g. a second output) are logged and ignored —
// multi-output support is a follow-up task.
func (d *dock) onGlobal(_ any, _ wl.Registry, name uint32, iface string, version uint32) error {
	switch iface {
	case "wl_compositor":
		if !d.hasCompositor {
			v := version
			if v > maxCompositorVersion {
				v = maxCompositorVersion
			}
			d.compositor = wl.Compositor(d.registry.Bind(name, &wl.CompositorInterface, v))
			d.hasCompositor = true
			d.log.Debug("bound wl_compositor", "name", name, "version", v)
		}
	case "wl_shm":
		if !d.hasShm {
			v := version
			if v > maxShmVersion {
				v = maxShmVersion
			}
			d.shm = wl.Shm(d.registry.Bind(name, &wl.ShmInterface, v))
			d.hasShm = true
			d.log.Debug("bound wl_shm", "name", name, "version", v)
		}
	case "wl_seat":
		if !d.hasSeat {
			v := version
			if v > maxSeatVersion {
				v = maxSeatVersion
			}
			d.seat = wl.Seat(d.registry.Bind(name, &wl.SeatInterface, v))
			d.hasSeat = true
			d.log.Debug("bound wl_seat", "name", name, "version", v)
		}
	case "wl_output":
		if !d.hasOutput {
			v := version
			if v > maxOutputVersion {
				v = maxOutputVersion
			}
			d.output = wl.Output(d.registry.Bind(name, &wl.OutputInterface, v))
			d.hasOutput = true
			d.log.Debug("bound wl_output", "name", name, "version", v)
		} else {
			d.log.Debug("ignoring additional wl_output (multi-output not yet supported)", "name", name)
		}
	case "zwlr_layer_shell_v1":
		if !d.hasLayerShell {
			v := version
			if v > maxLayerShellVersion {
				v = maxLayerShellVersion
			}
			d.layerShell = protocol.LayerShellV1(d.registry.Bind(name, &protocol.LayerShellV1Interface, v))
			d.hasLayerShell = true
			d.log.Debug("bound zwlr_layer_shell_v1", "name", name, "version", v)
		}
	}
	return nil
}

// onGlobalRemove is invoked when a global disappears (e.g. monitor hotplug).
// We log and ignore for now; Task 5+ will handle output removal.
func (d *dock) onGlobalRemove(_ any, _ wl.Registry, name uint32) error {
	d.log.Debug("wl_registry global_remove", "name", name)
	return nil
}

// close tears down all layer surfaces and the Wayland connection.
// It is safe to call more than once; Display.Close returns ErrAlreadyClosed
// on subsequent calls which we swallow.
func (d *dock) close() {
	for _, s := range d.surfaces {
		s.destroy()
	}
	d.surfaces = map[string]*layerSurface{}
	if err := d.display.Close(); err != nil {
		d.log.Debug("display close", "err", err)
	}
}

// run pumps the Wayland event loop until ctx is cancelled or a dispatch/flush
// error occurs.
//
// Event-loop pattern: tesselslate/wl only exposes a blocking Dispatch() with
// no non-blocking variant. To interleave snapshot updates with Wayland events
// without racing on Wayland objects, we keep ALL Wayland mutations on this
// single goroutine. We ensure Dispatch() returns promptly by issuing a
// wl_display.sync before each blocking call; the compositor replies with a
// wl_callback.done event, which wakes Dispatch(). Snapshot updates are drained
// after each wakeup, so latency is at most one compositor round-trip (~1 ms on
// a local socket) rather than being unbounded.
func (d *dock) run(ctx context.Context) error {
	snaps := make(chan []sessionView, 4)
	go subscribeLoop(snaps, d.log)

	// Goroutine that closes the display when ctx is cancelled, which causes
	// an in-progress Dispatch() to return with an error that we treat as clean
	// shutdown.
	go func() {
		<-ctx.Done()
		_ = d.display.Close()
	}()

	for {
		// Drain any pending snapshot updates before blocking on Dispatch.
		for {
			select {
			case snap := <-snaps:
				d.applySnapshot(snap)
			default:
				goto doneSnaps
			}
		}
	doneSnaps:

		// Issue a sync so the compositor will send back a wl_callback.done
		// event, guaranteeing that Dispatch() returns in finite time even when
		// there is no other compositor activity. This is safe because sync
		// writes to the write queue which is only touched on this goroutine.
		cb := d.display.Sync()
		cb.SetListener(wl.CallbackListener{
			Done: func(_ any, _ wl.Callback, _ uint32) error { return nil },
		}, nil)

		if err := d.display.Flush(); err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("flush: %w", err)
		}
		if err := d.display.Dispatch(); err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown triggered by ctx cancel
			}
			return fmt.Errorf("dispatch: %w", err)
		}
	}
}

// applySnapshot diffs the incoming session list against the current surface
// map and creates/destroys/updates surfaces to match.
// applySnapshot must be called from the same goroutine that owns all Wayland
// objects (the run() goroutine).
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

	// Destroy surfaces for sessions no longer present (or nil snapshot = clear all).
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

// colorFor maps session state to the canonical 0x00RRGGBB tongue colour,
// delegating to render.ColorFor so all backends share the same scheme.
func colorFor(s sessionView) uint32 {
	return render.ColorFor(s.Activity, s.Attention, s.Waiting)
}
