package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce absorbs bursts of filesystem events from a single logical
// write. Save (and most editors) produce more than one fsnotify event per
// write — e.g. a temp-file write followed by a rename — so a naive
// one-event-one-emit watcher would fire multiple times for one change.
const watchDebounce = 80 * time.Millisecond

// Watch watches the HUD config file for changes and sends the freshly
// reloaded Config on out each time it changes, until ctx is cancelled.
//
// It watches the file's *parent directory*, not the file itself: Save (and
// most editors) replace the file's inode on write rather than modifying it
// in place, and an inotify/fsnotify watch on a file descriptor goes stale
// the instant that happens — every write after the first would be silently
// missed. Watching the directory and filtering events to Path() survives
// inode replacement.
//
// Watch blocks until ctx is done, returning ctx.Err() at that point (or an
// fsnotify error, if the watcher itself fails outside of ctx cancellation).
func Watch(ctx context.Context, out chan<- Config, log *slog.Logger) error {
	target := Path()
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config watch: mkdir %s: %w", dir, err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("config watch: new watcher: %w", err)
	}
	defer w.Close()

	if err := w.Add(dir); err != nil {
		return fmt.Errorf("config watch: watch %s: %w", dir, err)
	}

	// timer is armed (Reset) on every relevant fsnotify event and disarmed by
	// default; it never fires on its own before the first relevant event.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) != target {
				continue
			}
			timer.Reset(watchDebounce)

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if log != nil {
				log.Warn("config watch", "err", err)
			}

		case <-timer.C:
			cfg := Load()
			select {
			case out <- cfg:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
