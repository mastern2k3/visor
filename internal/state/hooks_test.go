package state

import (
	"testing"

	"github.com/nitzanz/visor/internal/hookpayload"
	"github.com/nitzanz/visor/internal/transcript"
)

// hook is a tiny constructor for an enriched payload in tests.
func hook(sessionID, path string) hookpayload.Enriched {
	var p hookpayload.Enriched
	p.SessionID = sessionID
	p.TranscriptPath = path
	return p
}

func inSnapshot(snap []Snapshot, id string) bool {
	for _, s := range snap {
		if s.ID == id {
			return true
		}
	}
	return false
}

// A session that ended and is later revived (e.g. via `/branch`, `/resume`, or
// a fork that reuses the transcript) must reappear in the HUD. SessionEnd
// tombstones the session; a subsequent SessionStart means the user is back in
// it, so the tombstone must be cleared.
func TestApplyHook_SessionStartClearsEndedTombstone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := NewStore()

	id := "branch-sess-1"
	path := "/tmp/projects/proj/branch-sess-1.jsonl"

	s.ApplyHook("SessionStart", hook(id, path))
	if !inSnapshot(s.Snapshot(), id) {
		t.Fatalf("setup: session not in snapshot after SessionStart")
	}

	s.ApplyHook("SessionEnd", hook(id, path))
	if inSnapshot(s.Snapshot(), id) {
		t.Fatalf("setup: ended session should be excluded from snapshot")
	}

	// Revival: SessionStart fires again for the same session (this is exactly
	// what /branch and /resume do — metadata like PID/WM is re-captured).
	s.ApplyHook("SessionStart", hook(id, path))
	if !inSnapshot(s.Snapshot(), id) {
		t.Fatalf("revived session never reappeared in snapshot — Ended tombstone not cleared")
	}
}

// An already-running session that ended and is then revived only via continued
// transcript writes (no fresh SessionStart) must also come back. This is the
// recovery path for a live session after a daemon restart reloads its tombstone.
func TestApplyTranscript_LiveAppendClearsEndedTombstone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := NewStore()

	path := "/tmp/projects/proj/revive.jsonl"
	s.UpsertByPath(path)
	s.ApplyHook("SessionEnd", hook("", path))
	if inSnapshot(s.Snapshot(), path) {
		t.Fatalf("setup: ended session should be excluded from snapshot")
	}

	// A live append (isInitial=false) means the session is alive again.
	lines := []transcript.Line{{Type: "user", SessionID: "", CWD: "/tmp"}}
	s.ApplyTranscript(path, lines, 1, false)
	if !inSnapshot(s.Snapshot(), path) {
		t.Fatalf("live append did not clear Ended tombstone")
	}
}

// Backfill at startup must NOT resurrect an ended session — that's the whole
// reason the tombstone is persisted.
func TestApplyTranscript_BackfillKeepsEndedTombstone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := NewStore()

	path := "/tmp/projects/proj/dead.jsonl"
	s.UpsertByPath(path)
	s.ApplyHook("SessionEnd", hook("", path))

	// Startup replay of the on-disk transcript (isInitial=true) must stay hidden.
	lines := []transcript.Line{{Type: "user", SessionID: "", CWD: "/tmp"}}
	s.ApplyTranscript(path, lines, 1, true)
	if inSnapshot(s.Snapshot(), path) {
		t.Fatalf("backfill resurrected an ended session — tombstone not respected")
	}
}
