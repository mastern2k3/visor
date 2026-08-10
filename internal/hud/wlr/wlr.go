// Package wlr is the native Wayland HUD backend.
//
// One zwlr_layer_surface_v1 per Claude session, anchored to the right edge of
// the primary output. Surfaces are drawn into wl_shm buffers; double-buffered
// so a frame is never modified while the compositor holds it.
//
// Compositor requirements: zwlr_layer_shell_v1 must be in the registry.
// Targets Niri (primary), sway, hyprland. Will NOT work on GNOME —
// use --backend=x11 (Xwayland) there.
package wlr

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/nitzanz/visor/internal/hud"
	"github.com/nitzanz/visor/internal/hud/config"
)

var _ hud.Backend = (*Backend)(nil)

// Backend implements hud.Backend by running an in-process Wayland client
// that subscribes to the visor daemon and manages one layer surface per
// session.
type Backend struct {
	cfg       config.Config
	pinConfig bool
}

// New constructs the wlr backend with an already-resolved config. pinConfig
// is true when the caller passed an explicit --theme or --shadow flag: in
// that case the dock does not start the config-file watcher, so the
// flag-selected values cannot be silently overridden by a later `visor hud
// theme`/`visor hud shadow` write.
func New(cfg config.Config, pinConfig bool) *Backend {
	return &Backend{cfg: cfg, pinConfig: pinConfig}
}

func (b *Backend) Name() string { return "wlr" }

func (b *Backend) Install() (string, error) {
	return "wlr backend is built into visor; nothing to install. Run `visor hud open --backend=wlr`.\n", nil
}

func (b *Backend) Open() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d, err := newDock(b.cfg, b.pinConfig)
	if err != nil {
		return fmt.Errorf("connect wayland: %w", err)
	}
	defer d.close()
	return d.run(ctx)
}

func (b *Backend) Close() error {
	return fmt.Errorf("wlr backend runs in-foreground; send SIGTERM (Ctrl-C / kill) to stop it")
}
