package wlr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

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

	// surfaces is keyed by session id. layerSurface values can be compared with
	// == in findSurface because wl.Surface embeds a pointer to per-object data,
	// so value equality reduces to pointer equality of that backing data.
	surfaces map[string]*layerSurface

	// pointer handles wl_pointer events (hover-expand, click-to-act).
	// Initialised in newDock after globals are bound.
	pointer *pointer
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
	// This is the most likely reason for missing text: install a font from the
	// candidate list (DejaVuSansMono, LiberationMono, or NotoSansMono) or
	// point one of the candidate paths at a valid TTF.
	if f, err := render.LoadFont(); err != nil {
		d.log.Warn("font load failed; tongues will show background colour only — install DejaVuSansMono, LiberationMono, or NotoSansMono",
			"err", err,
			"tried", render.FontCandidates(),
		)
	} else {
		d.font = f
	}

	d.surfaces = map[string]*layerSurface{}

	// Wire pointer input. newPointer calls seat.GetPointer(), which requires
	// the seat global to already be bound — safe here because the roundtrip
	// above has completed.
	d.pointer = newPointer(d)

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
	if d.pointer != nil {
		d.pointer.wp.Release()
	}
	if err := d.display.Close(); err != nil {
		d.log.Debug("display close", "err", err)
	}
}

// idlePollInterval caps how long the event loop waits for compositor activity
// when no snapshot has arrived. At 50 ms the HUD lag is imperceptible to a
// human; in the idle case the loop ticks at ~20 Hz instead of hot-looping at
// compositor round-trip rate (~1 ms), saving significant CPU.
const idlePollInterval = 50 * time.Millisecond

// run pumps the Wayland event loop until ctx is cancelled or a dispatch/flush
// error occurs.
//
// Event-loop pattern: tesselslate/wl only exposes a blocking Dispatch() with
// no non-blocking variant and does not expose the display fd for edge-triggered
// I/O. To interleave snapshot updates with Wayland events without racing on
// Wayland objects, we keep ALL Wayland mutations on this single goroutine.
//
// Rate-limiting strategy: when no snapshot arrived in the last iteration we
// wait up to idlePollInterval before forcing a compositor wakeup via
// wl_display.sync. A new snapshot cancels the wait early, keeping interactive
// latency low (~50 ms worst-case) while cutting idle CPU from ~1000 Hz to
// ~20 Hz. A follow-up could vendor-patch the library to add Display.Fd() and
// replace this with unix.Poll for true edge-triggered wakeups.
func (d *dock) run(ctx context.Context) error {
	snaps := make(chan []sessionView, 4)
	go subscribeLoop(ctx, snaps, d.log)

	// Goroutine that closes the display when ctx is cancelled, which causes
	// an in-progress Dispatch() to return with an error that we treat as clean
	// shutdown.
	go func() {
		<-ctx.Done()
		_ = d.display.Close()
	}()

	for {
		// Drain pending snapshots without blocking.
		drained := false
		for {
			select {
			case snap := <-snaps:
				d.applySnapshot(snap)
				drained = true
				continue
			default:
			}
			break
		}

		if !drained {
			// Idle path: wait up to idlePollInterval for a snapshot before
			// forcing a Wayland wakeup. Keeps idle CPU at ~20 Hz instead of
			// hot-looping at compositor round-trip rate (~1 ms).
			select {
			case snap := <-snaps:
				d.applySnapshot(snap)
			case <-time.After(idlePollInterval):
			case <-ctx.Done():
				return nil
			}
		}

		// Force Dispatch() to return in bounded time. wl_callback is
		// auto-destroyed by the dispatch path after Done fires, so no leak.
		cb := d.display.Sync()
		cb.SetListener(wl.CallbackListener{
			// Listener body is intentionally empty — we only need Dispatch()
			// to return when this callback fires.
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

// applySnapshot reconciles the surface map with a new snapshot.
//
// snap is iterated in daemon-sort order (needs > ack, then by FirstSeen — see
// internal/state/notify.go), so slot assignments are stable across calls.
// Map iteration in the destroy loop is unordered, but destroy is commutative
// so order doesn't matter.
//
// A nil snap (daemon down) destroys all surfaces, clearing the HUD.
//
// Dismissed sessions are filtered out and treated as absent, matching x11
// backend semantics — they stay in the daemon's state but are not shown until
// their next state transition re-arms attention.
//
// applySnapshot must be called from the same goroutine that owns all Wayland
// objects (the run() goroutine).
func (d *dock) applySnapshot(snap []sessionView) {
	seen := map[string]bool{}
	slot := 0
	for _, s := range snap {
		if s.Attention == "dismissed" {
			continue
		}
		seen[s.ID] = true
		st := render.TongueState{
			Color:       colorFor(s),
			Label:       labelFor(s),
			TongueRight: true,
		}
		if ls, ok := d.surfaces[s.ID]; ok {
			st.Expanded = ls.state.Expanded // preserve hover state across snapshot updates
			if ls.state != st {
				ls.state = st
				ls.repaint(d)
			}
			// Re-stack: slot may have changed.
			ls.setSlot(slot)
		} else {
			ls, err := newLayerSurface(d, slot, s.ID, st)
			if err != nil {
				d.log.Warn("create surface", "id", s.ID, "err", err)
				slot++
				continue
			}
			d.surfaces[s.ID] = ls
		}
		slot++
	}

	// Destroy surfaces for sessions no longer present (or nil snapshot = clear all).
	for id, ls := range d.surfaces {
		if !seen[id] {
			if d.pointer != nil && d.pointer.focused == ls {
				d.pointer.focused = nil
			}
			ls.destroy()
			delete(d.surfaces, id)
		}
	}
}

// findSurface returns the layerSurface whose underlying wl_surface matches s,
// or nil if none is found. Used by the pointer input handler to map compositor
// Enter/Leave events back to the owning session surface.
//
// wl.Surface is defined as `type Surface Object`, and Object embeds *objdata.
// Two Surface values are identical when their *objdata pointers are equal, so
// the == comparison is correct.
func (d *dock) findSurface(s wl.Surface) *layerSurface {
	for _, ls := range d.surfaces {
		if ls.surface == s {
			return ls
		}
	}
	return nil
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
