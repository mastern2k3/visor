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
			if _, known := render.ThemeByName(v); known {
				c.Theme = v
			}
		case "shadow":
			switch v {
			case "true", "on", "yes", "1":
				c.Shadow = true
			case "false", "off", "no", "0":
				c.Shadow = false
			}
		}
	}
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
		if _, known := render.ThemeByName(v); known {
			c.Theme = v
		}
	}
	if v := os.Getenv("VISOR_SHADOW"); v != "" {
		switch v {
		case "true", "on", "yes", "1":
			c.Shadow = true
		case "false", "off", "no", "0":
			c.Shadow = false
		}
	}
	if flagTheme != "" {
		if _, known := render.ThemeByName(flagTheme); known {
			c.Theme = flagTheme
		}
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
