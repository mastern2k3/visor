package x11

import (
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/xevent"
)

// eventPump replaces xevent.Main/MainPing so that xgb's reader goroutine can
// never block on a full event channel.
//
// Why: xgb reads everything the server sends — events and replies — on one
// goroutine, in order, and hands events off through a bounded channel
// (eventBuffer, 5000). Separately, once ~1000 requests are outstanding, xgb's
// sender forces a synchronous round trip and waits for its reply. With
// xgbutil's loop, that channel is drained only by the goroutine that runs our
// callbacks, so a callback making *any* X request — checked or not — while the
// channel is full deadlocks: callback waits on the sender, sender waits on the
// reply, reader waits on the channel, channel waits on the callback. Observed
// in the wild (onEnter -> setExpanded -> win.Move) after ~3 days of uptime.
//
// Here a dedicated goroutine does nothing but move events from xgb into an
// unbounded slice, so the reader always makes progress and the cycle cannot
// close. Dispatch still goes through xgbutil's own callback and hook tables,
// so every existing xevent.*Fun.Connect registration keeps working.
type eventPump struct {
	mu    sync.Mutex
	q     []xgbutil.EventOrError
	ready chan struct{} // 1-buffered: "queue is non-empty"
	done  chan struct{} // closed when the X connection is gone
}

func startEventPump(c *xgb.Conn) *eventPump {
	p := &eventPump{ready: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for {
			ev, err := c.WaitForEvent()
			if ev == nil && err == nil {
				return // connection closed
			}
			p.mu.Lock()
			p.q = append(p.q, xgbutil.EventOrError{Event: ev, Err: err})
			p.mu.Unlock()
			select {
			case p.ready <- struct{}{}:
			default:
			}
		}
	}()
	return p
}

func (p *eventPump) take() []xgbutil.EventOrError {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.q
	p.q = nil
	return q
}

// dispatchEvent runs xgbutil hooks and then the callbacks registered for the
// event types this dock connects (ButtonPress, EnterNotify, LeaveNotify).
// Anything else is dropped, as xgbutil's loop would for a type with no
// callbacks. Add a case here if you Connect a new event type.
func dispatchEvent(X *xgbutil.XUtil, ev xgb.Event) {
	X.HooksLck.RLock()
	hooks := X.Hooks
	X.HooksLck.RUnlock()
	for _, h := range hooks {
		if !h.Run(X, ev) {
			return
		}
	}

	var (
		wrapped interface{}
		evtype  int
		win     xproto.Window
	)
	switch e := ev.(type) {
	case xproto.ButtonPressEvent:
		X.TimeSet(e.Time)
		wrapped, evtype, win = xevent.ButtonPressEvent{ButtonPressEvent: &e}, xevent.ButtonPress, e.Event
	case xproto.EnterNotifyEvent:
		X.TimeSet(e.Time)
		wrapped, evtype, win = xevent.EnterNotifyEvent{EnterNotifyEvent: &e}, xevent.EnterNotify, e.Event
	case xproto.LeaveNotifyEvent:
		X.TimeSet(e.Time)
		wrapped, evtype, win = xevent.LeaveNotifyEvent{LeaveNotifyEvent: &e}, xevent.LeaveNotify, e.Event
	default:
		return
	}

	// Copy-on-write slice, same as xgbutil's runCallbacks.
	X.CallbacksLck.RLock()
	cbs := X.Callbacks[evtype][win]
	X.CallbacksLck.RUnlock()
	for _, cb := range cbs {
		cb.Run(X, wrapped)
	}
}
