package config

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestWatch_EmitsOnChange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Config{Theme: "silent", Shadow: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Config, 4)
	go func() {
		_ = Watch(ctx, ch, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	// Give the watcher time to register before mutating the file.
	time.Sleep(100 * time.Millisecond)
	if err := Save(Config{Theme: "traffic", Shadow: false}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.Theme != "traffic" || got.Shadow {
			t.Errorf("watched config = %+v, want {traffic false}", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no config event within 3s")
	}

	// The 80ms debounce exists precisely so a single Save collapses into a
	// single emission — Save's WriteFile typically produces more than one
	// fsnotify event (e.g. a truncate/write pair), and without debouncing the
	// dock would repaint once per fsnotify event instead of once per file
	// change. Wait comfortably longer than the debounce window for a second
	// emission that should never arrive.
	select {
	case extra := <-ch:
		t.Fatalf("one Save should collapse into exactly one emission via the debounce; got an extra event %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
