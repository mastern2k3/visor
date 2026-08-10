package state

import (
	"testing"
	"time"

	"github.com/nitzanz/visor/internal/hookpayload"
	"github.com/nitzanz/visor/internal/transcript"
)

func TestProjectName(t *testing.T) {
	cases := []struct{ title, cwd, id, want string }{
		{"my-feature", "/home/n/Projects/visor", "abcdef12", "my-feature"},
		{"", "/home/n/Projects/visor", "abcdef12", "visor"},
		{"", "", "abcdef1234", "abcdef12"},
		{"", "/", "abcdef12", "abcdef12"},
	}
	for _, c := range cases {
		if got := projectName(c.title, c.cwd, c.id); got != c.want {
			t.Errorf("projectName(%q,%q,%q) = %q, want %q",
				c.title, c.cwd, c.id, got, c.want)
		}
	}
}

// A single initial is enough until two live sessions collide, at which point
// BOTH must widen — otherwise the user sees two identical glyphs.
func TestAssignGlyphs_DisambiguatesCollisions(t *testing.T) {
	snaps := []Snapshot{
		{DisplayName: "visor"},
		{DisplayName: "deepdub"},
		{DisplayName: "dotfiles"},
	}
	assignGlyphs(snaps)
	if snaps[0].Glyph != "V" {
		t.Errorf("unique initial glyph = %q, want \"V\"", snaps[0].Glyph)
	}
	if snaps[1].Glyph != "DE" {
		t.Errorf("colliding glyph = %q, want \"DE\"", snaps[1].Glyph)
	}
	if snaps[2].Glyph != "DO" {
		t.Errorf("colliding glyph = %q, want \"DO\"", snaps[2].Glyph)
	}
}

func TestAssignGlyphs_EmptyNameYieldsEmptyGlyph(t *testing.T) {
	snaps := []Snapshot{{DisplayName: ""}}
	assignGlyphs(snaps)
	if snaps[0].Glyph != "" {
		t.Errorf("glyph for empty name = %q, want \"\"", snaps[0].Glyph)
	}
}

// Non-ASCII project names must produce a well-formed rune-based glyph, not a
// mangled partial UTF-8 byte sequence. See CLAUDE.md / task brief resolution (d).
func TestAssignGlyphs_NonASCIIName(t *testing.T) {
	snaps := []Snapshot{{DisplayName: "école"}}
	assignGlyphs(snaps)
	runes := []rune(snaps[0].Glyph)
	if len(runes) != 1 {
		t.Fatalf("glyph for non-ASCII name = %q (%d runes), want exactly 1 valid rune", snaps[0].Glyph, len(runes))
	}
	if runes[0] != 'É' {
		t.Errorf("glyph for non-ASCII name = %q, want %q", snaps[0].Glyph, "É")
	}

	// Collision case: two non-ASCII names sharing an initial must widen to two
	// full runes each, not two mangled bytes.
	snaps2 := []Snapshot{{DisplayName: "école"}, {DisplayName: "écran"}}
	assignGlyphs(snaps2)
	for _, s := range snaps2 {
		runes := []rune(s.Glyph)
		if len(runes) != 2 {
			t.Errorf("collision glyph for %q = %q (%d runes), want exactly 2 valid runes", "", s.Glyph, len(runes))
		}
	}
}

// StateSince must move on an activity transition and stay put otherwise —
// this is what makes the HUD's elapsed counter meaningful and what keeps it
// out of the broadcast digest.
func TestStateSince_MovesOnlyOnTransition(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := NewStore()
	sess := s.UpsertByPath("/tmp/x.jsonl")
	sess.Activity = transcript.ActivityWorking
	sess.StateSince = time.Now().Add(-time.Hour)
	before := sess.StateSince

	// Same activity again → StateSince unchanged.
	s.ApplyTranscript("/tmp/x.jsonl", []transcript.Line{
		{Type: "assistant", Message: &transcript.MessageBody{StopReason: "tool_use"}},
	}, 10, false)
	if got, _ := s.Get(sess.ID); !got.StateSince.Equal(before) {
		t.Errorf("StateSince moved without a transition: %v → %v", before, got.StateSince)
	}

	// Transition to waiting → StateSince updates.
	s.ApplyTranscript("/tmp/x.jsonl", []transcript.Line{
		{Type: "assistant", Message: &transcript.MessageBody{StopReason: "end_turn"}},
	}, 20, false)
	if got, _ := s.Get(sess.ID); !got.StateSince.After(before) {
		t.Errorf("StateSince did not move on transition: still %v", got.StateSince)
	}
}

// Regression guard for the digest rule in CLAUDE.md.
func TestHudDigest_IgnoresStateSince(t *testing.T) {
	a := []Snapshot{{ID: "x", Activity: "working", StateSince: time.Now()}}
	b := []Snapshot{{ID: "x", Activity: "working", StateSince: time.Now().Add(time.Hour)}}
	if hudDigest(a) != hudDigest(b) {
		t.Errorf("digest changed with StateSince; it must be excluded")
	}
}

func TestHudDigest_IncludesGlyph(t *testing.T) {
	a := []Snapshot{{ID: "x", Glyph: "V"}}
	b := []Snapshot{{ID: "x", Glyph: "VI"}}
	if hudDigest(a) == hudDigest(b) {
		t.Errorf("digest ignored Glyph; it must be included")
	}
}

// Resolution (c): every session-construction path must initialise StateSince
// to something sane. In particular, sessions hydrated from the persisted
// state file at daemon startup must never carry a zero StateSince — that
// renders in the HUD as an ~2562047h elapsed time (time.Since(zero)), because
// render.ElapsedString only clamps *negative* durations to 0s.
func TestNewStore_HydratedSessionsHaveNonZeroStateSince(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	firstSeen := time.Now().Add(-2 * time.Hour)
	err := savePersisted([]persistedSession{
		{
			ID:        "hydrated-1",
			CWD:       "/home/n/Projects/visor",
			FirstSeen: firstSeen,
		},
	})
	if err != nil {
		t.Fatalf("savePersisted: %v", err)
	}

	s := NewStore()
	sess, ok := s.Get("hydrated-1")
	if !ok {
		t.Fatalf("hydrated session not found in store")
	}
	if sess.StateSince.IsZero() {
		t.Fatalf("hydrated session has zero StateSince")
	}
	if !sess.StateSince.Equal(firstSeen) {
		t.Errorf("hydrated StateSince = %v, want FirstSeen %v", sess.StateSince, firstSeen)
	}
}

// ApplyHook's session-creation path (a session that arrives via a hook
// before the tailer has ever seen its transcript) is a fourth construction
// site not called out in the task brief. It must also seed StateSince.
func TestApplyHook_NewSessionHasNonZeroStateSince(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := NewStore()

	var p hookpayload.Enriched
	p.SessionID = "fresh-hook-session"
	sess := s.ApplyHook("SessionStart", p)
	if sess == nil {
		t.Fatalf("ApplyHook returned nil session")
	}
	if sess.StateSince.IsZero() {
		t.Fatalf("session created via ApplyHook has zero StateSince")
	}
}
