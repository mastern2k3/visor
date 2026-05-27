package wlr

import (
	"log/slog"

	"codeberg.org/tesselslate/wl"

	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
)

const (
	btnLeft   = 0x110 // BTN_LEFT  (linux/input-event-codes.h)
	btnRight  = 0x111 // BTN_RIGHT
	btnMiddle = 0x112 // BTN_MIDDLE
)

// pointer wires up wl_pointer event handlers. It looks up surfaces via the
// dock's surfaces map. All callbacks are invoked from the Wayland dispatch
// goroutine (dock.run), so no additional locking is needed.
type pointer struct {
	d       *dock
	wp      wl.Pointer
	focused *layerSurface // surface currently under the cursor, nil if none
}

// newPointer obtains a wl_pointer from the seat and registers Enter / Leave /
// Button handlers. Must be called after the wl_seat global is bound.
func newPointer(d *dock) *pointer {
	wp := d.seat.GetPointer()
	p := &pointer{d: d, wp: wp}
	wp.SetListener(wl.PointerListener{
		Enter:  p.onEnter,
		Leave:  p.onLeave,
		Button: p.onButton,
	}, nil)
	return p
}

// onEnter is called by the compositor when the pointer moves onto a surface.
// The surface argument identifies which wl_surface was entered.
func (p *pointer) onEnter(_ any, _ wl.Pointer, _ uint32, surf wl.Surface, _, _ float64) error {
	ls := p.d.findSurface(surf)
	if ls == nil {
		return nil
	}
	p.focused = ls
	if !ls.state.Expanded {
		ls.state.Expanded = true
		ls.repaint(p.d)
	}
	return nil
}

// onLeave is called by the compositor when the pointer leaves a surface.
func (p *pointer) onLeave(_ any, _ wl.Pointer, _ uint32, surf wl.Surface) error {
	ls := p.d.findSurface(surf)
	if ls == nil {
		return nil
	}
	if p.focused == ls {
		p.focused = nil
	}
	if ls.state.Expanded {
		ls.state.Expanded = false
		ls.repaint(p.d)
	}
	return nil
}

// onButton is called for each mouse button press or release while the pointer
// is over a surface. We only act on presses (state == PointerButtonStatePressed).
func (p *pointer) onButton(_ any, _ wl.Pointer, _ uint32, _ uint32, button uint32, state wl.PointerButtonState) error {
	if state != wl.PointerButtonStatePressed || p.focused == nil {
		return nil
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
		return nil
	}
	id := p.focused.sessionID
	go func() {
		_, err := ipc.Call(paths.Socket(), ipc.Request{Cmd: cmd, ID: id})
		if err != nil {
			slog.Warn("wlr ipc", "cmd", cmd, "err", err)
		}
	}()
	return nil
}
