package state

import (
	"testing"

	"github.com/nitzanz/visor/internal/transcript"
)

func ev(id string, kind transcript.BackgroundKind, failed bool) transcript.BackgroundEvent {
	return transcript.BackgroundEvent{TaskID: id, Kind: kind, Failed: failed}
}

func TestApplyBackground_RunningCount(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("b", transcript.BackgroundStart, false),
	}, false)
	if len(s.BackgroundRunning) != 2 {
		t.Fatalf("running=%d, want 2", len(s.BackgroundRunning))
	}
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want empty while running", s.BackgroundOutcome)
	}
}

func TestApplyBackground_OutcomeDone(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundStart, false)}, false)
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundFinish, false)}, false)
	if len(s.BackgroundRunning) != 0 {
		t.Fatalf("running=%d, want 0", len(s.BackgroundRunning))
	}
	if s.BackgroundOutcome != "done" {
		t.Errorf("outcome=%q, want done", s.BackgroundOutcome)
	}
}

func TestApplyBackground_AnyFailureFailsBatch(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("b", transcript.BackgroundStart, false),
	}, false)
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundFinish, false),
		ev("b", transcript.BackgroundFinish, true),
	}, false)
	if s.BackgroundOutcome != "failed" {
		t.Errorf("outcome=%q, want failed", s.BackgroundOutcome)
	}
}

func TestApplyBackground_NewBatchClearsOutcome(t *testing.T) {
	s := &Session{}
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundStart, false)}, false)
	s.applyBackground([]transcript.BackgroundEvent{ev("a", transcript.BackgroundFinish, true)}, false)
	if s.BackgroundOutcome != "failed" {
		t.Fatalf("setup: outcome=%q, want failed", s.BackgroundOutcome)
	}
	s.applyBackground([]transcript.BackgroundEvent{ev("b", transcript.BackgroundStart, false)}, false)
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want cleared when new batch starts", s.BackgroundOutcome)
	}
}

func TestApplyBackground_BackfillSuppressesOutcome(t *testing.T) {
	s := &Session{}
	// Whole history replayed at once on daemon start: net running=0, but the
	// completion is historical and must NOT light an outcome dot.
	s.applyBackground([]transcript.BackgroundEvent{
		ev("a", transcript.BackgroundStart, false),
		ev("a", transcript.BackgroundFinish, false),
	}, true)
	if s.BackgroundOutcome != "" {
		t.Errorf("outcome=%q, want empty on backfill", s.BackgroundOutcome)
	}
	if len(s.BackgroundRunning) != 0 {
		t.Errorf("running=%d, want 0", len(s.BackgroundRunning))
	}
}
