package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nitzanz/visor/internal/hud"
	"github.com/nitzanz/visor/internal/hud/config"
	"github.com/nitzanz/visor/internal/hud/eww"
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

// pickBackend resolves a backend name to an implementation. cfg and
// pinTheme are only meaningful to the in-process backends (x11, wlr); eww
// renders from its own `visor ctl watch` subscription and has no concept of
// a resolved palette.
func pickBackend(name string, cfg config.Config, pinTheme bool) (hud.Backend, error) {
	switch name {
	case "", "eww":
		return eww.New(), nil
	case "x11":
		return x11.New(cfg, pinTheme), nil
	case "wlr":
		return wlr.New(cfg, pinTheme), nil
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
	backendName := fs.String("backend", "eww", "HUD backend (eww|x11|wlr)")
	themeFlag := fs.String("theme", "", "palette theme (tuned|silent|traffic); pins the theme, disabling live reload")
	var shadowFlag optionalBool
	fs.Var(&shadowFlag, "shadow", "draw the HUD's own drop shadow (true|false)")
	_ = fs.Parse(args[1:])

	switch sub {
	case "install", "open", "close":
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
		if _, ok := render.ThemeByName(name); !ok {
			fmt.Fprintf(os.Stderr, "hud theme: unknown theme %q (have: %s)\n",
				name, strings.Join(render.Themes(), ", "))
			os.Exit(2)
		}
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
