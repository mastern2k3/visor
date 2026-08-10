package wlr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/hud/config"
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

	// Render inputs used by layerSurface.repaint. faces is nil if font load
	// failed, in which case tabs render without any text. palette/cfg are
	// mutated in place (never rebuilt) when a config-file change arrives —
	// see applyConfig — so every surface picks up the fresh values on its
	// next repaint.
	faces   *render.Faces
	palette render.Palette
	cfg     config.Config

	// pinConfig is true when the user passed an explicit --theme or --shadow
	// flag. In that case run() does not start the config-file watcher, so a
	// later `visor hud theme`/`visor hud shadow` write cannot silently
	// override the flag.
	pinConfig bool

	// surfaces is keyed by session id. layerSurface values can be compared with
	// == in findSurface because wl.Surface embeds a pointer to per-object data,
	// so value equality reduces to pointer equality of that backing data.
	surfaces map[string]*layerSurface

	// pointer handles wl_pointer events (hover-expand, click-to-act).
	// Initialised in newDock after globals are bound.
	pointer *pointer
}

func newDock(cfg config.Config, pinConfig bool) (*dock, error) {
	d := &dock{
		log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		cfg:       cfg,
		palette:   render.Theme(cfg.Theme),
		pinConfig: pinConfig,
	}
	// internal/hud/config has no logger of its own (Load/Parse must never
	// fail, and the very first config.Resolve call in cmd/visor/hud.go runs
	// before any dock exists), so it warns about a bad "theme = " line in
	// hud.conf via slog's package-level default logger instead. That very
	// first Resolve call still goes through slog's plain (but non-silent —
	// stderr by default) handler; pointing the default at the dock's own
	// structured logger here just makes every *subsequent* config.Watch
	// reload warning come out formatted like the rest of this backend's logs.
	slog.SetDefault(d.log)

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

	// Load faces; failure is non-fatal — tabs render without text. This is the
	// most likely reason for missing text: install a font from the candidate
	// list (DejaVuSansMono, LiberationMono, or NotoSansMono) or point one of
	// the candidate paths at a valid TTF.
	if f, err := render.LoadFaces(); err != nil {
		d.log.Warn("font load failed; tabs will render without text — install DejaVuSansMono, LiberationMono, or NotoSansMono",
			"err", err,
			"tried", render.FontCandidates(),
		)
	} else {
		d.faces = f
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

	// cfgUpdates only receives events when pinConfig is false; when the config
	// was pinned via --theme/--shadow, no goroutine ever writes to it, so the select
	// cases below simply never fire and the flag-selected theme sticks for
	// the lifetime of the process.
	cfgUpdates := make(chan config.Config, 4)
	if !d.pinConfig {
		go func() {
			if err := config.Watch(ctx, cfgUpdates, d.log); err != nil && ctx.Err() == nil {
				d.log.Warn("config watch exited", "err", err)
			}
		}()
	}

	// Goroutine that closes the display when ctx is cancelled, which causes
	// an in-progress Dispatch() to return with an error that we treat as clean
	// shutdown.
	go func() {
		<-ctx.Done()
		_ = d.display.Close()
	}()

	for {
		// Drain pending snapshots and config updates without blocking.
		drained := false
		for {
			select {
			case snap := <-snaps:
				d.applySnapshot(snap)
				drained = true
				continue
			case newCfg := <-cfgUpdates:
				d.applyConfig(newCfg)
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
			case newCfg := <-cfgUpdates:
				d.applyConfig(newCfg)
			case <-time.After(idlePollInterval):
			case <-ctx.Done():
				return nil
			}
		}

		// Animation tick: working tabs wobble, needs tabs protrude, expanded
		// tabs' elapsed label ticks over once a second, and permission tabs
		// pulse their halo. Each of these is an independent cheap no-op when
		// its state doesn't apply, so calling all three for every surface on
		// every ~20Hz iteration costs nothing for the common case of a
		// collapsed, non-permission surface.
		now := time.Now()
		for _, s := range d.surfaces {
			s.animateTick(now)
			s.tickElapsed(now, d)
			s.tickHalo(now, d)
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

// applyConfig stores a newly-loaded config from the file watcher and
// repaints every surface in place with the new palette/shadow. Surfaces are
// not destroyed and recreated — that would flash the dock and lose hover
// state — so this only ever calls repaint() on existing surfaces, which
// itself falls back to the surface's own dirty-retry-on-release path (see
// layerSurface.repaint / pool.onRelease) if both shm buffers are currently
// in-flight with the compositor.
//
// Must be called from the run() goroutine, same as applySnapshot.
func (d *dock) applyConfig(cfg config.Config) {
	d.cfg = cfg
	d.palette = render.Theme(cfg.Theme)
	d.log.Info("config changed; repainting surfaces", "theme", cfg.Theme, "shadow", cfg.Shadow)
	for _, s := range d.surfaces {
		s.state.Shadow = cfg.Shadow
		s.repaint(d)
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
		name := s.DisplayName
		if name == "" {
			// Older daemon or incomplete snapshot: fall back to the
			// pre-Task-8 behaviour rather than showing an empty line 1.
			name = labelFor(s)
		}
		// labelFor falls back to DisplayCWD when there is no title, so drop the
		// path in that case rather than printing it twice.
		path := s.DisplayCWD
		if path == name {
			path = ""
		}
		now := time.Now()
		// Elapsed is derived fresh from "now" here, but truncated to whole
		// seconds (render.Elapsed) so that repeated calls within the same
		// second are equal — otherwise the `ls.state != st` skip-check below
		// would defeat itself and every broadcast would force a repaint
		// regardless of whether anything about this surface actually
		// changed. HaloPhase is filled in per-branch below: it is quantised
		// against the surface's own wobbleStart (the same epoch tickHalo
		// uses), which does not exist yet for a surface not yet created.
		st := render.TabState{
			Activity:          s.Activity,
			Attention:         s.Attention,
			Waiting:           s.Waiting,
			Glyph:             s.Glyph,
			Name:              name,
			Path:              path,
			Elapsed:           render.Elapsed(s.StateSince, now),
			TabRight:          true,
			Shadow:            d.cfg.Shadow,
			BackgroundRunning: s.BackgroundRunning,
			BackgroundOutcome: s.BackgroundOutcome,
		}
		if ls, ok := d.surfaces[s.ID]; ok {
			st.Expanded = ls.state.Expanded // preserve hover state across snapshot updates
			// Use the surface's own wobbleStart as the halo epoch — the same
			// one tickHalo will use on the next animation tick — so the
			// phase baked in here and the phase tickHalo advances from later
			// never disagree.
			_, haloPhase := render.HaloPhaseStep(ls.wobbleStart, now)
			st.HaloPhase = haloPhase
			if ls.state != st {
				ls.state = st
				ls.repaint(d)
			}
			// Update animation-relevant fields; the next tick picks up changes.
			ls.activity = s.Activity
			ls.attention = s.Attention
			ls.stateSince = s.StateSince
			ls.lastElapsed = render.ElapsedString(st.Elapsed)
			step, _ := render.HaloPhaseStep(ls.wobbleStart, now)
			ls.lastHaloStep = step
			// Re-stack: slot may have changed.
			ls.setSlot(slot)
		} else {
			// st.HaloPhase stays at its zero value: wobbleStart is only
			// assigned inside newLayerSurface below, so there is no epoch to
			// quantise against yet. The very next animation tick (tickHalo)
			// corrects it to the real phase, well within the ~50ms idle-poll
			// bound — imperceptible for a slow 1.6s pulse.
			ls, err := newLayerSurface(d, slot, s.ID, s.Activity, s.Attention, st)
			if err != nil {
				d.log.Warn("create surface", "id", s.ID, "err", err)
				slot++
				continue
			}
			ls.stateSince = s.StateSince
			ls.lastElapsed = render.ElapsedString(st.Elapsed)
			step, _ := render.HaloPhaseStep(ls.wobbleStart, now)
			ls.lastHaloStep = step
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
