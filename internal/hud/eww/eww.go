// Package eww is the eww-based HUD backend.
//
// Eww is a yuck-configured GTK widget host that already solves the
// cross-WM "always-on-top dock" problem we'd otherwise reimplement against
// gtk-layer-shell + _NET_WM_WINDOW_TYPE_DOCK. The daemon serves session
// state via `visor ctl json`; eww polls it once a second and renders.
package eww

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed eww.yuck eww.scss
var assets embed.FS

const windowName = "visor"

type Backend struct{}

func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return "eww" }

// configDir returns ~/.config/eww/visor — the canonical eww config dir for
// this backend (-c flag).
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "eww", "visor"), nil
}

func (b *Backend) Install() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files := []string{"eww.yuck", "eww.scss"}
	written := make([]string, 0, len(files))
	for _, name := range files {
		b, err := assets.ReadFile(name)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return "", err
		}
		written = append(written, dst)
	}
	summary := ""
	for _, p := range written {
		summary += "wrote " + p + "\n"
	}
	if _, err := exec.LookPath("eww"); err != nil {
		summary += "\neww binary not found in $PATH.\n" +
			"Install with one of:\n" +
			"  • cargo install eww --locked   (needs sudo apt install libgtk-3-dev libdbusmenu-gtk3-dev libgtk-layer-shell-dev)\n" +
			"  • Build from https://github.com/elkowar/eww (no prebuilt binaries since v0.4.0)\n"
	} else {
		summary += "\nNext: start the daemon, then `visor hud open`.\n"
	}
	return summary, nil
}

func (b *Backend) Open() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "eww.yuck")); err != nil {
		return fmt.Errorf("eww config not installed (run `visor hud install`): %w", err)
	}
	if _, err := exec.LookPath("eww"); err != nil {
		return errors.New("eww binary not in $PATH")
	}
	cmd := exec.Command("eww", "-c", dir, "open", windowName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("eww open: %w (output: %s)", err, out)
	}
	return nil
}

func (b *Backend) Close() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("eww"); err != nil {
		return errors.New("eww binary not in $PATH")
	}
	cmd := exec.Command("eww", "-c", dir, "close", windowName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("eww close: %w (output: %s)", err, out)
	}
	return nil
}
