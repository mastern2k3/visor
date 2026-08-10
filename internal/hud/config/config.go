// Package config reads and writes the HUD's on-disk preferences: which
// palette theme to use, and whether visor draws its own drop shadow.
//
// The format is deliberately flat `key = value` rather than TOML — there are
// two keys and no foreseeable nesting.
package config

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nitzanz/visor/internal/hud/render"
)

// Config is the resolved HUD preference set.
type Config struct {
	Theme  string
	Shadow bool
}

// Default is used when no file, env, or flag says otherwise.
func Default() Config {
	return Config{Theme: render.DefaultTheme, Shadow: true}
}

// Path returns the config file location: $XDG_CONFIG_HOME/visor/hud.conf,
// falling back to ~/.config/visor/hud.conf.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "visor-hud.conf"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "visor", "hud.conf")
}

// parseBool parses common boolean representations. It returns (value, ok) where
// ok indicates the string was a recognized boolean token.
func parseBool(v string) (bool, bool) {
	switch v {
	case "true", "on", "yes", "1":
		return true, true
	case "false", "off", "no", "0":
		return false, true
	default:
		return false, false
	}
}

// setTheme validates a theme name via render.ThemeByName and sets it if
// valid. An unrecognized candidate is dropped in favour of whatever *dst
// already held — the spec's documented behaviour ("fall back to the default
// with a logged warning") — but logs via slog's package-level default
// logger rather than taking a *slog.Logger parameter.
//
// This package deliberately holds no logger of its own: Load() has a
// no-error, always-succeeds contract, and it's called from spots (the very
// first config.Resolve in cmd/visor/hud.go, before any backend exists) that
// have no logger to inject yet. slog.Default() is ambient rather than
// dependency-injected, so both that early path and config.Watch's later
// reloads (which the dock configures via slog.SetDefault at startup) share
// one warning path without config importing anything backend-specific or
// changing Load's signature.
func setTheme(dst *string, candidate string) {
	if _, known := render.ThemeByName(candidate); known {
		*dst = candidate
		return
	}
	slog.Warn("hud config: unknown theme name, ignoring",
		"got", candidate, "have", strings.Join(render.Themes(), ", "), "kept", *dst)
}

// Parse reads flat key = value lines. Unknown keys, malformed lines and
// unknown theme names are ignored in favour of defaults — a bad config must
// degrade, never prevent the HUD from starting.
func Parse(r io.Reader) Config {
	c := Default()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "theme":
			setTheme(&c.Theme, v)
		case "shadow":
			if val, ok := parseBool(v); ok {
				c.Shadow = val
			}
		}
	}
	// Scanner errors are deliberately absorbed; this package's contract is
	// that unreadable input degrades to defaults, never prevents startup.
	return c
}

// Load reads the config file, returning defaults if it is missing or unreadable.
func Load() Config {
	f, err := os.Open(Path())
	if err != nil {
		return Default()
	}
	defer f.Close()
	return Parse(f)
}

// Resolve applies precedence: flag → env → file → default. A nil flagShadow
// or empty flagTheme means "the flag was not set".
func Resolve(flagTheme string, flagShadow *bool) Config {
	c := Load()
	if v := os.Getenv("VISOR_THEME"); v != "" {
		setTheme(&c.Theme, v)
	}
	if v := os.Getenv("VISOR_SHADOW"); v != "" {
		if val, ok := parseBool(v); ok {
			c.Shadow = val
		}
	}
	if flagTheme != "" {
		setTheme(&c.Theme, flagTheme)
	}
	if flagShadow != nil {
		c.Shadow = *flagShadow
	}
	return c
}

// Save writes both keys, creating the directory if needed.
func Save(c Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# visor HUD config. Themes: %s\ntheme = %s\nshadow = %t\n",
		strings.Join(render.Themes(), ", "), c.Theme, c.Shadow)
	return os.WriteFile(p, []byte(body), 0o644)
}
