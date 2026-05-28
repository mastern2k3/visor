package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nitzanz/visor/internal/hud"
	"github.com/nitzanz/visor/internal/hud/eww"
	"github.com/nitzanz/visor/internal/hud/wlr"
	"github.com/nitzanz/visor/internal/hud/x11"
)

// pickBackend resolves a backend name to an implementation.
func pickBackend(name string) (hud.Backend, error) {
	switch name {
	case "", "eww":
		return eww.New(), nil
	case "x11":
		return x11.New(), nil
	case "wlr":
		return wlr.New(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}

func runHUD(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "hud: subcommand required (install|open|close)")
		os.Exit(2)
	}
	sub := args[0]
	fs := flag.NewFlagSet("hud "+sub, flag.ExitOnError)
	backendName := fs.String("backend", "eww", "HUD backend (eww|x11|wlr)")
	_ = fs.Parse(args[1:])

	b, err := pickBackend(*backendName)
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
	default:
		fmt.Fprintf(os.Stderr, "hud: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
}
