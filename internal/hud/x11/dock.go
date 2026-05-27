package x11

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BurntSushi/freetype-go/freetype/truetype"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xevent"

	"github.com/nitzanz/visor/internal/hud/render"
)

// dock owns the X connection and manages a map of tongue windows keyed
// by session ID. It selects between X events and incoming snapshot updates
// from the visor daemon subscription.
type dock struct {
	X       *xgbutil.XUtil
	mon     monitor
	log     *slog.Logger
	tongues map[string]*tongue // session id → window
	font    *truetype.Font     // shared across tongues; loaded once at startup

	// Synthetic "help" tongue pinned at slot 0; clicking it toggles helpW.
	helpT *tongue
	helpW *helpWindow
}

func newDock() (*dock, error) {
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
		X:       X,
		mon:     mon,
		log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		tongues: map[string]*tongue{},
	}
	if f, ferr := render.LoadFont(); ferr != nil {
		d.log.Warn("font load failed; expanded tongues will be blank", "err", ferr)
	} else {
		d.font = f
	}
	d.log.Info("X connected", "mon_x", mon.x, "mon_y", mon.y, "mon_w", mon.w, "mon_h", mon.h)
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
	for _, t := range d.tongues {
		t.destroy()
	}
	d.X.Conn().Close()
}

func (d *dock) run() error {
	// Create the help tongue at slot 0 before any session tongues land.
	if err := d.makeHelpTongue(); err != nil {
		d.log.Warn("help tongue create failed", "err", err)
	}

	snaps := make(chan []sessionView, 4)
	go subscribeLoop(snaps, d.log)
	d.log.Info("subscribed to visor daemon")

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
		case now := <-anim.C:
			d.animate(now)
		case <-sig:
			d.log.Info("signal received; shutting down")
			d.quit()
		}
	}
}

// animate ticks each tongue's animation state. Currently just the wobble
// on "working" tongues; other state-driven motion would go here too.
func (d *dock) animate(now time.Time) {
	for _, t := range d.tongues {
		t.tick(now)
	}
	// The help tongue doesn't wobble (it's not a session) but tick() is a
	// no-op for non-working tongues, so calling it is harmless.
	if d.helpT != nil {
		d.helpT.tick(now)
	}
}

// makeHelpTongue creates the synthetic help tab at slot 0 and wires its
// click handler to toggle the help window.
func (d *dock) makeHelpTongue() error {
	y := d.mon.y + dockTopMargin
	color := colorFor(helpTongueSession)
	t, err := newTongue(d.X, d.mon, tongueOpts{y: y, color: color})
	if err != nil {
		return err
	}
	t.sess = helpTongueSession
	t.font = d.font
	t.clickFn = func(button byte) {
		// Any button toggles the help window. Using a goroutine isn't
		// necessary here (no IPC), but X calls from the event handler are
		// fine since they go through xgb's serialized send queue.
		if d.helpW != nil {
			d.helpW.close()
			d.helpW = nil
			return
		}
		hw, herr := openHelp(d.X, d.mon, d.font, func() {
			d.helpW = nil
		})
		if herr != nil {
			d.log.Warn("help window create failed", "err", herr)
			return
		}
		d.helpW = hw
	}
	t.render()
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

// dock layout constants — shared by help tongue positioning and snapshot
// application so they stay in sync.
const (
	dockTopMargin = 140 // start lower on the screen for easier reach
	dockGap       = 8
)

// applySnapshot diffs the incoming session list against current tongues
// and opens/closes/updates windows to match. Positioning is index-based:
// session N is at y = mon.y + topMargin + (N+1)*(tongueH + gap) — slot 0
// is reserved for the help tongue.
//
// Dismissed sessions are hidden from the dock entirely — that's what
// dismissing means visually. They stay in the daemon's state (and in
// `ctl list` for debugging) and reappear when their next state change
// re-arms attention.
func (d *dock) applySnapshot(snap []sessionView) {
	const (
		topMargin = dockTopMargin
		gap       = dockGap
	)

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

	// Close tongues for sessions no longer present.
	for id, t := range d.tongues {
		if _, ok := want[id]; !ok {
			t.destroy()
			delete(d.tongues, id)
		}
	}

	// Open or update one tongue per snapshot entry. Slot 0 is the help
	// tongue, so session tongues start at slot 1.
	for i, s := range snap {
		y := d.mon.y + topMargin + (i+1)*(tongueH+gap)
		color := colorFor(s)
		t, ok := d.tongues[s.ID]
		if !ok {
			nt, err := newTongue(d.X, d.mon, tongueOpts{y: y, color: color})
			if err != nil {
				d.log.Warn("tongue create failed", "id", s.ID, "err", err)
				continue
			}
			nt.sess = s
			nt.font = d.font
			nt.render() // initial paint
			d.tongues[s.ID] = nt
			continue
		}
		t.update(s, y, color)
	}
	d.X.Sync()
}

// colorFor maps session state to a 0xRRGGBB tongue colour.
func colorFor(s sessionView) uint32 {
	switch {
	case s.Attention == "needs" && s.Waiting == "permission":
		return 0xff7a7a // red — blocked on approval
	case s.Attention == "needs":
		return 0xebcb8b // amber — waiting for user
	case s.Attention == "dismissed":
		return 0x3b414e // dim — silenced
	case s.Activity == "working":
		return 0x88c0d0 // cyan — busy
	default:
		return 0x6b7280 // grey — idle/ack
	}
}
