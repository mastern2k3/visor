// Package discovery finds Claude Code transcript JSONL files and emits
// events when they are created or appended to.
//
// Strategy:
//   - fsnotify watches the projects root + each project subdir for new .jsonl files
//     (subagent transcripts under <session-id>/subagents/ are excluded — they're
//     forks of a parent session, not their own attention units).
//   - per-file polling (mtime+size) replays appends, since several Claude
//     versions write transcripts via patterns that fsnotify misses in practice
//     (claude-pool and ccusage both poll for this reason).
package discovery

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event reports a transcript file that exists or has grown.
type Event struct {
	Path      string    // absolute path to .jsonl
	Size      int64
	ModTime   time.Time
	IsInitial bool // true for the backfill scan at startup
}

// Watcher emits Event values when transcript files appear or change.
type Watcher struct {
	root     string
	poll     time.Duration
	log      *slog.Logger
	events   chan Event
	mu       sync.Mutex
	seen     map[string]fileState // path → last observed
	fsn      *fsnotify.Watcher
	watchDir map[string]bool // dirs currently watched
}

type fileState struct {
	size    int64
	modTime time.Time
}

// backfillMaxAge: at startup, ignore transcripts whose mtime is older than this.
// Old sessions can still be picked up later if a hook fires or the file appends.
const backfillMaxAge = 24 * time.Hour

func New(root string, log *slog.Logger) *Watcher {
	return &Watcher{
		root:     root,
		poll:     400 * time.Millisecond,
		log:      log,
		events:   make(chan Event, 64),
		seen:     map[string]fileState{},
		watchDir: map[string]bool{},
	}
}

func (w *Watcher) Events() <-chan Event { return w.events }

func (w *Watcher) Run(ctx context.Context) error {
	fsn, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fsn = fsn
	defer fsn.Close()

	if err := w.addDir(w.root); err != nil {
		return err
	}
	w.backfill()

	tick := time.NewTicker(w.poll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			close(w.events)
			return nil
		case ev, ok := <-fsn.Events:
			if !ok {
				return nil
			}
			w.handleFSEvent(ev)
		case err, ok := <-fsn.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("fsnotify error", "err", err)
		case <-tick.C:
			w.poll1()
		}
	}
}

// isTranscript reports whether path is a top-level project transcript.
// Excludes subagent transcripts (which live in <session-id>/subagents/).
func isTranscript(root, path string) bool {
	if !strings.HasSuffix(path, ".jsonl") {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	// Expect: <project-dir>/<session-id>.jsonl  (exactly two segments)
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2
}

func (w *Watcher) addDir(d string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watchDir[d] {
		return nil
	}
	if err := w.fsn.Add(d); err != nil {
		return err
	}
	w.watchDir[d] = true
	return nil
}

// backfill scans the tree once at startup so pre-existing sessions are picked up.
func (w *Watcher) backfill() {
	_ = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Watch project dirs (depth 1) so new sessions are picked up.
			if path != w.root {
				rel, _ := filepath.Rel(w.root, path)
				if !strings.Contains(rel, string(filepath.Separator)) {
					_ = w.addDir(path)
				}
			}
			return nil
		}
		if isTranscript(w.root, path) {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if time.Since(info.ModTime()) > backfillMaxAge {
				return nil
			}
			w.maybeEmit(path, true)
		}
		return nil
	})
}

func (w *Watcher) handleFSEvent(ev fsnotify.Event) {
	if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
		return
	}
	fi, err := os.Stat(ev.Name)
	if err != nil {
		return
	}
	if fi.IsDir() {
		// New project directory — watch it.
		_ = w.addDir(ev.Name)
		return
	}
	if isTranscript(w.root, ev.Name) {
		w.maybeEmit(ev.Name, false)
	}
}

func (w *Watcher) poll1() {
	w.mu.Lock()
	paths := make([]string, 0, len(w.seen))
	for p := range w.seen {
		paths = append(paths, p)
	}
	w.mu.Unlock()
	for _, p := range paths {
		w.maybeEmit(p, false)
	}
}

func (w *Watcher) maybeEmit(path string, initial bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	w.mu.Lock()
	prev, ok := w.seen[path]
	if ok && prev.size == fi.Size() && prev.modTime.Equal(fi.ModTime()) {
		w.mu.Unlock()
		return
	}
	w.seen[path] = fileState{size: fi.Size(), modTime: fi.ModTime()}
	w.mu.Unlock()

	// Block on send: dropping events on a slow consumer is worse than briefly
	// pausing discovery. The poll tick will re-emit on the next interval anyway.
	w.events <- Event{Path: path, Size: fi.Size(), ModTime: fi.ModTime(), IsInitial: initial}
}
