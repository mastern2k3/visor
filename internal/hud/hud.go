// Package hud defines the common interface for HUD backends.
//
// A backend is anything that visualises the daemon's session state. Today
// there's just `eww` (pull-based: the daemon serves JSON, eww renders). Future
// backends (x11, gtk) will be push-based: in-process renderers subscribed to
// state changes.
//
// The interface stays minimal until we have a second backend — overspecifying
// it now risks fitting it to today's only example.
package hud

// Backend is what `visor hud` subcommands dispatch to.
type Backend interface {
	// Name is the short identifier ("eww", "x11", "gtk").
	Name() string
	// Install writes whatever the backend needs on disk (configs, embedded
	// assets). Returns a human-readable summary suitable for printing.
	Install() (summary string, err error)
	// Open starts the visualisation. For external-process backends (eww) this
	// spawns the process; for in-process backends it would start the renderer.
	Open() error
	// Close stops the visualisation.
	Close() error
}
