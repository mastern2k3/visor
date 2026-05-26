package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/nitzanz/visor/internal/paths"
)

// persistedSession is the on-disk shape of one session — only the fields
// that can't be reconstructed by re-tailing the transcript:
//
//   - Hook-captured metadata (PID, WM, WindowID, TmuxPane) — without this,
//     `visor ctl jump` can't focus the right window after a daemon restart
//     until the next SessionStart hook fires.
//   - CWD — also present in every JSONL line, but cheap to carry and lets
//     the HUD show a meaningful label the moment the daemon comes up,
//     before the tailer has read any line.
//   - Dismissed — explicit user intent to silence.
//
// Deliberately NOT persisted (recomputed from JSONL on startup):
//   - Activity / Waiting / Attention=Needs — derived state
//   - Title — from `ai-title` records in the transcript; re-extracted on tail
type persistedSession struct {
	ID             string    `json:"id"`
	TranscriptPath string    `json:"transcript_path"`
	CWD            string    `json:"cwd,omitempty"`
	PID            int       `json:"pid,omitempty"`
	WM             string    `json:"wm,omitempty"`
	WindowID       string    `json:"window_id,omitempty"`
	TmuxPane       string    `json:"tmux_pane,omitempty"`
	FirstSeen      time.Time `json:"first_seen,omitempty"`
	Dismissed      bool      `json:"dismissed,omitempty"`
}

// persistedState is what we serialize to disk.
type persistedState struct {
	Sessions []persistedSession `json:"sessions"`

	// Legacy: older builds wrote a flat list of dismissed IDs separately
	// from session metadata. Still read on load for migration; never written.
	LegacyDismissed []string `json:"dismissed,omitempty"`
}

func stateFile() string {
	return filepath.Join(paths.StateDir(), "state.json")
}

// LoadPersisted reads the saved state file and returns the sessions
// and dismissed set ready for hydration into a new Store.
//
// Missing file → empty result, not an error. Sessions whose transcript
// path no longer exists are dropped (avoid zombie entries in the HUD).
func LoadPersisted() (sessions []persistedSession, dismissed map[string]bool, err error) {
	dismissed = map[string]bool{}
	b, err := os.ReadFile(stateFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, dismissed, nil
		}
		return nil, dismissed, err
	}
	var ps persistedState
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, dismissed, err
	}
	// Legacy migration: dismissed ids without session metadata.
	for _, id := range ps.LegacyDismissed {
		dismissed[id] = true
	}
	// Filter sessions whose transcript is gone; merge dismissed-flag into the set.
	for _, ps := range ps.Sessions {
		// Drop entries with no identity at all — these are leftovers from
		// older builds that accepted hook payloads missing both fields, and
		// would render as blank, undismissable tongues in the HUD.
		if ps.ID == "" && ps.TranscriptPath == "" {
			continue
		}
		if ps.TranscriptPath != "" {
			if _, err := os.Stat(ps.TranscriptPath); err != nil {
				continue // transcript deleted; drop the entry
			}
		}
		sessions = append(sessions, ps)
		if ps.Dismissed {
			dismissed[ps.ID] = true
		}
	}
	return sessions, dismissed, nil
}

// savePersisted writes the current durable state atomically. Tiny payload,
// fine to call synchronously after each mutation.
func savePersisted(sessions []persistedSession) error {
	dir := paths.StateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(persistedState{Sessions: sessions}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, stateFile())
}
