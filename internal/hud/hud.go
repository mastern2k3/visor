// Package hud defines the common interface for HUD backends.
//
// A backend is anything that visualises the daemon's session state. Today
// there are two: `x11` and `wlr`, both push-based in-process renderers
// subscribed to state changes.
//
// The interface stays minimal — overspecifying it now risks fitting it to
// today's two examples rather than whatever a third backend actually needs.
package hud

// Backend is what `visor hud` subcommands dispatch to.
type Backend interface {
	// Name is the short identifier ("x11", "wlr").
	Name() string
	// Install writes whatever the backend needs on disk (configs, embedded
	// assets). Returns a human-readable summary suitable for printing.
	Install() (summary string, err error)
	// Open starts the visualisation in-process (spawns the renderer that
	// subscribes to daemon state and draws the dock).
	Open() error
	// Close stops the visualisation.
	Close() error
}
