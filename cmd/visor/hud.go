package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nitzanz/visor/internal/hud"
	"github.com/nitzanz/visor/internal/hud/config"
	"github.com/nitzanz/visor/internal/hud/render"
	"github.com/nitzanz/visor/internal/hud/wlr"
	"github.com/nitzanz/visor/internal/hud/x11"
)

// optionalBool is a flag.Value that distinguishes "flag not passed" from
// both "--shadow=true" and "--shadow=false". flag.Bool can't express that
// third state, but config.Resolve's precedence (flag > env > file > default)
// needs it: a nil flag must fall through to env/file rather than forcing
// Shadow to false.
type optionalBool struct {
	val *bool
}

func (o *optionalBool) String() string {
	if o.val == nil {
		return ""
	}
	return strconv.FormatBool(*o.val)
}

func (o *optionalBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("invalid boolean %q", s)
	}
	o.val = &v
	return nil
}

// IsBoolFlag lets the flag package accept a bare "--shadow" (no "=value") as
// shorthand for "--shadow=true", matching flag.Bool's usual ergonomics.
func (o *optionalBool) IsBoolFlag() bool { return true }

// rejectUnknownTheme prints the same "unknown theme" error the `theme`
// subcommand uses and exits 2 when name is non-empty but not a recognized
// theme name. An empty name (flag not passed) is not an error here — that
// case is handled by config.Resolve falling through to env/file/default.
//
// This must run *before* config.Resolve/pinTheme are computed for the
// --theme flag: config.Resolve's underlying setTheme silently discards an
// unknown theme and keeps whatever the file/env/default already had, so
// without this check `visor hud open --theme=bogus` would print nothing,
// render with the fallback theme, and — worse — permanently pin the config
// (disabling live reload) for a theme the user never actually got. Exiting
// here keeps the two entry points (this flag, and `hud theme <name>`) from
// drifting: same message shape, same exit code.
func rejectUnknownTheme(cmdName, name string) {
	if name == "" {
		return
	}
	if _, ok := render.ThemeByName(name); !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown theme %q (have: %s)\n",
			cmdName, name, strings.Join(render.Themes(), ", "))
		os.Exit(2)
	}
}

// pickBackend resolves a backend name to an implementation. An empty name
// auto-detects: a Wayland session (WAYLAND_DISPLAY set) gets the wlr
// backend, everything else gets x11 (which also covers GNOME via XWayland,
// since GNOME has no layer-shell). cfg and pinTheme are threaded through to
// whichever in-process backend gets picked.
func pickBackend(name string, cfg config.Config, pinTheme bool) (hud.Backend, error) {
	switch name {
	case "":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return wlr.New(cfg, pinTheme), nil
		}
		return x11.New(cfg, pinTheme), nil
	case "x11":
		return x11.New(cfg, pinTheme), nil
	case "wlr":
		return wlr.New(cfg, pinTheme), nil
	case "eww":
		return nil, errors.New("the eww backend was removed; use --backend=x11 or --backend=wlr instead")
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}

func runHUD(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "hud: subcommand required (install|open|close|theme|shadow)")
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("hud "+sub, flag.ExitOnError)
	backendName := fs.String("backend", "", "HUD backend (x11|wlr; default: auto-detect)")
	themeFlag := fs.String("theme", "", "palette theme (tuned|silent|traffic); pins the whole config (theme AND shadow), disabling live reload for both")
	var shadowFlag optionalBool
	fs.Var(&shadowFlag, "shadow", "draw the HUD's own drop shadow (true|false)")
	_ = fs.Parse(args[1:])

	switch sub {
	case "install", "open", "close":
		rejectUnknownTheme("hud "+sub, *themeFlag)
		cfg := config.Resolve(*themeFlag, shadowFlag.val)
		b, err := pickBackend(*backendName, cfg, *themeFlag != "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "hud:", err)
			os.Exit(2)
		}
		switch sub {
		case "install":
			summary, err := b.Install()
			if err != nil {
				fmt.Fprintln(os.Stderr, "hud install:", err)
				os.Exit(1)
			}
			fmt.Print(summary)
		case "open":
			if err := b.Open(); err != nil {
				fmt.Fprintln(os.Stderr, "hud open:", err)
				os.Exit(1)
			}
		case "close":
			if err := b.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "hud close:", err)
				os.Exit(1)
			}
		}
	case "theme":
		if fs.NArg() != 1 {
			fmt.Fprintf(os.Stderr, "hud theme: want one of: %s\n",
				strings.Join(render.Themes(), ", "))
			os.Exit(2)
		}
		name := fs.Arg(0)
		rejectUnknownTheme("hud theme", name)
		c := config.Load()
		c.Theme = name
		if err := config.Save(c); err != nil {
			fmt.Fprintln(os.Stderr, "hud theme:", err)
			os.Exit(1)
		}
		fmt.Printf("theme = %s (written to %s)\n", name, config.Path())
	case "shadow":
		if fs.NArg() != 1 || (fs.Arg(0) != "on" && fs.Arg(0) != "off") {
			fmt.Fprintln(os.Stderr, "hud shadow: want one of: on, off")
			os.Exit(2)
		}
		c := config.Load()
		c.Shadow = fs.Arg(0) == "on"
		if err := config.Save(c); err != nil {
			fmt.Fprintln(os.Stderr, "hud shadow:", err)
			os.Exit(1)
		}
		fmt.Printf("shadow = %v (written to %s)\n", c.Shadow, config.Path())
	default:
		fmt.Fprintf(os.Stderr, "hud: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
}
