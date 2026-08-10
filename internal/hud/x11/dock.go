package x11

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xevent"

	"github.com/nitzanz/visor/internal/hud/config"
	"github.com/nitzanz/visor/internal/hud/render"
)

// dock owns the X connection and manages a map of tab windows keyed
// by session ID. It selects between X events and incoming snapshot updates
// from the visor daemon subscription.
type dock struct {
	X    *xgbutil.XUtil
	mon  monitor
	log  *slog.Logger
	tabs map[string]*tab // session id → window

	// Render inputs, loaded/resolved once at startup and shared across tabs.
	// palette/cfg are mutated in place when a config-file change arrives (see
	// applyConfig) — never rebuilt into a new struct — so every tab that holds
	// a copy of d.palette gets the fresh one on its next render() call.
	faces   *render.Faces
	palette render.Palette
	cfg     config.Config

	// pinTheme is true when the user passed an explicit --theme flag. In that
	// case run() does not start the config-file watcher, so a later `visor
	// hud theme` write cannot silently override the flag.
	pinTheme bool

	// tipFont backs the two remaining xgraphics text paths (overflow tooltip,
	// help window), which draw with freetype rather than gg.
	tipFont *truetype.Font

	// visual/argb are resolved once at startup. argb is true only when both
	// halves hold: a depth-32 TrueColor visual exists AND a compositing manager
	// is running to blend it. Either one alone is useless — an unblended alpha
	// window renders its transparent padding as black. When argb is false the
	// tabs stay at root depth and render squared and shadowless.
	visual xproto.Visualid
	argb   bool

	// Synthetic "help" tab pinned at slot 0; clicking it toggles helpW.
	helpT *tab
	helpW *helpWindow
}

func newDock(cfg config.Config, pinTheme bool) (*dock, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, err
	}
	mon, err := primaryMonitor(X)
	if err != nil {
		X.Conn().Close()
		return nil, err
	}
	d := &dock{
		X:        X,
		mon:      mon,
		log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		tabs:     map[string]*tab{},
		cfg:      cfg,
		palette:  render.Theme(cfg.Theme),
		pinTheme: pinTheme,
	}
	if f, ferr := render.LoadFaces(); ferr != nil {
		d.log.Warn("font load failed; tabs will render without text",
			"err", ferr, "tried", render.FontCandidates())
	} else {
		d.faces = f
	}
	if f, ferr := loadTipFont(); ferr != nil {
		d.log.Warn("tooltip/help font load failed; those windows will be blank", "err", ferr)
	} else {
		d.tipFont = f
	}
	// Detect alpha capability exactly once, and log the fallback exactly once —
	// per-tab logging here would spam a line per session.
	visual, visualOK := argbVisual(X)
	d.visual = visual
	d.argb = visualOK && hasCompositor(X)
	if !d.argb {
		d.log.Info("no compositing manager or ARGB visual; " +
			"falling back to squared corners without shadow")
	}

	d.log.Info("X connected", "mon_x", mon.x, "mon_y", mon.y, "mon_w", mon.w,
		"mon_h", mon.h, "argb", d.argb)
	return d, nil
}

func (d *dock) close() {
	if d.helpW != nil {
		d.helpW.close()
		d.helpW = nil
	}
	if d.helpT != nil {
		d.helpT.destroy()
		d.helpT = nil
	}
	for _, t := range d.tabs {
		t.destroy()
	}
	d.X.Conn().Close()
}

func (d *dock) run() error {
	// Create the help tab at slot 0 before any session tabs land.
	if err := d.makeHelpTab(); err != nil {
		d.log.Warn("help tab create failed", "err", err)
	}

	// Derive a context that is cancelled when the X event loop shuts down or a
	// signal arrives. subscribeLoop uses this to exit cleanly without leaking
	// goroutines or file descriptors.
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	snaps := make(chan []sessionView, 4)
	go subscribeLoop(ctx, snaps, d.log)
	d.log.Info("subscribed to visor daemon")

	// cfgUpdates only receives events when pinTheme is false; when the theme
	// was pinned via --theme, no goroutine ever writes to it, so this case in
	// the select below simply never fires and the flag-selected theme sticks
	// for the lifetime of the process.
	cfgUpdates := make(chan config.Config, 4)
	if !d.pinTheme {
		go func() {
			if err := config.Watch(ctx, cfgUpdates, d.log); err != nil && ctx.Err() == nil {
				d.log.Warn("config watch exited", "err", err)
			}
		}()
	}

	pingBefore, pingAfter, pingQuit := xevent.MainPing(d.X)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	// ~30 Hz animation tick — smooth enough for wobble easing, low overhead
	// (xgb's Move requests are tiny; the X server applies them in batch).
	anim := time.NewTicker(33 * time.Millisecond)
	defer anim.Stop()

	for {
		select {
		case <-pingBefore:
			<-pingAfter
		case <-pingQuit:
			d.log.Info("event loop quit")
			return nil
		case snap := <-snaps:
			d.applySnapshot(snap)
		case newCfg := <-cfgUpdates:
			d.applyConfig(newCfg)
		case now := <-anim.C:
			d.animate(now)
		case <-sig:
			d.log.Info("signal received; shutting down")
			d.quit()
		}
	}
}

// animate ticks each tab's animation state: position wobble, the once-a-
// second elapsed-label refresh on expanded tabs, and the permission halo
// pulse. tick/tickElapsed/tickHalo are each individually cheap no-ops when
// their state doesn't apply (not working / not hovered / not glowing), so
// calling all three for every tab on every ~30Hz frame costs nothing for the
// common case of a collapsed, non-permission tab.
func (d *dock) animate(now time.Time) {
	for _, t := range d.tabs {
		t.tick(now)
		t.tickElapsed(now)
		t.tickHalo(now)
	}
	// The help tab doesn't wobble (it's not a session) but tick()/tickElapsed/
	// tickHalo are no-ops for it, so calling them is harmless.
	if d.helpT != nil {
		d.helpT.tick(now)
		d.helpT.tickElapsed(now)
		d.helpT.tickHalo(now)
	}
}

// applyConfig stores a newly-loaded config from the file watcher and
// re-renders every tab (including the synthetic help tab) in place with the
// new palette/shadow. Windows are reused, not recreated — recreating them
// would flash the dock and drop hover/expanded state — so this only ever
// calls render() on existing tabs.
//
// render() normally skips repainting when the computed render.TabState is
// unchanged from last time, and TabState carries no palette field (palette
// is a render *input*, not part of the observable state), so t.rendered is
// forced back to false here to bypass that memoization for exactly this one
// call.
func (d *dock) applyConfig(cfg config.Config) {
	d.cfg = cfg
	d.palette = render.Theme(cfg.Theme)
	d.log.Info("config changed; re-rendering tabs", "theme", cfg.Theme, "shadow", cfg.Shadow)

	now := time.Now()
	for _, t := range d.tabs {
		t.palette = d.palette
		t.shadow = cfg.Shadow
		t.rendered = false
		t.render(now)
	}
	if d.helpT != nil {
		d.helpT.palette = d.palette
		d.helpT.shadow = cfg.Shadow
		d.helpT.rendered = false
		d.helpT.render(now)
	}
	d.X.Sync()
}

// makeHelpTab creates the synthetic help tab at slot 0 and wires its
// click handler to toggle the help window.
func (d *dock) makeHelpTab() error {
	y := d.mon.y + dockTopMargin
	t, err := newTab(d.X, d.mon, tabOpts{
		y: y, color: d.bgPixel(helpTabSession), argb: d.argb, visual: d.visual,
	})
	if err != nil {
		return err
	}
	t.sess = helpTabSession
	t.faces = d.faces
	t.palette = d.palette
	t.shadow = d.cfg.Shadow
	t.tipFont = d.tipFont
	t.clickFn = func(button byte) {
		// Any button toggles the help window. Using a goroutine isn't
		// necessary here (no IPC), but X calls from the event handler are
		// fine since they go through xgb's serialized send queue.
		if d.helpW != nil {
			d.helpW.close()
			d.helpW = nil
			return
		}
		hw, herr := openHelp(d.X, d.mon, d.tipFont, d.palette, func() {
			d.helpW = nil
		})
		if herr != nil {
			d.log.Warn("help window create failed", "err", herr)
			return
		}
		d.helpW = hw
	}
	t.render(time.Now())
	d.helpT = t
	return nil
}

// quit stops the X event loop. xevent.Quit only sets a flag — if the loop
// is currently blocked inside Read waiting for the next X event, it won't
// notice. Sending a synthetic ClientMessage to the root window wakes the
// read so the flag is checked on the next iteration.
func (d *dock) quit() {
	xevent.Quit(d.X)
	wake := xproto.ClientMessageEvent{
		Format: 32,
		Window: d.X.RootWin(),
		Type:   0,
		Data:   xproto.ClientMessageDataUnionData32New([]uint32{0, 0, 0, 0, 0}),
	}
	xproto.SendEvent(d.X.Conn(), false, d.X.RootWin(),
		uint32(xproto.EventMaskStructureNotify),
		string(wake.Bytes()))
	d.X.Sync()
}

// loadTipFont parses the system mono font with freetype for the two windows
// that still draw text through xgraphics (the overflow tooltip and the help
// screen). render.LoadFont is gone — render now exposes opentype faces for gg
// — but xgraphics.Image.Text only accepts a freetype *truetype.Font, so the
// parse lives here until those windows are ported too.
func loadTipFont() (*truetype.Font, error) {
	p, err := render.FontPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read font %s: %w", p, err)
	}
	ft, err := truetype.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("parse font %s: %w", p, err)
	}
	return ft, nil
}

// dock layout constants — shared by help tab positioning and snapshot
// application so they stay in sync. Vertical spacing itself comes from
// render.RowPitch, which already includes each tab's shadow padding.
const (
	dockTopMargin = 140 // start lower on the screen for easier reach
)

// applySnapshot diffs the incoming session list against current tabs
// and opens/closes/updates windows to match. Positioning is index-based:
// session N is at y = mon.y + topMargin + (N+1)*(tabH + gap) — slot 0
// is reserved for the help tab.
//
// Dismissed sessions are hidden from the dock entirely — that's what
// dismissing means visually. They stay in the daemon's state (and in
// `ctl list` for debugging) and reappear when their next state change
// re-arms attention.
func (d *dock) applySnapshot(snap []sessionView) {
	const topMargin = dockTopMargin

	visible := snap[:0:0]
	for _, s := range snap {
		if s.Attention == "dismissed" {
			continue
		}
		visible = append(visible, s)
	}
	snap = visible

	// Build set of incoming IDs for diffing.
	want := make(map[string]int, len(snap))
	for i, s := range snap {
		want[s.ID] = i
	}

	// Close tabs for sessions no longer present.
	for id, t := range d.tabs {
		if _, ok := want[id]; !ok {
			t.destroy()
			delete(d.tabs, id)
		}
	}

	// Open or update one tab per snapshot entry. Slot 0 is the help
	// tab, so session tabs start at slot 1.
	for i, s := range snap {
		y := d.mon.y + topMargin + (i+1)*render.RowPitch
		t, ok := d.tabs[s.ID]
		if !ok {
			nt, err := newTab(d.X, d.mon, tabOpts{
				y: y, color: d.bgPixel(s), argb: d.argb, visual: d.visual,
			})
			if err != nil {
				d.log.Warn("tab create failed", "id", s.ID, "err", err)
				continue
			}
			nt.sess = s
			nt.faces = d.faces
			nt.palette = d.palette
			nt.shadow = d.cfg.Shadow
			nt.tipFont = d.tipFont
			nt.render(time.Now()) // initial paint
			d.tabs[s.ID] = nt
			continue
		}
		t.update(s, y)
	}
	d.X.Sync()
}

// bgPixel is the window background colour used until the first pixmap paint
// lands. The rendered buffer covers the whole window, so this only matters for
// the instant between Map and the first XDraw; the capsule base colour is the
// least jarring thing to show there.
func (d *dock) bgPixel(s sessionView) uint32 {
	return d.palette.For(s.Activity, s.Attention, s.Waiting).Base & 0x00ffffff
}
