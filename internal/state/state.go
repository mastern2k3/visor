// Package state holds the daemon's view of live Claude sessions.
//
// Two orthogonal axes:
//   - Activity (from JSONL/hooks): working | waiting | unknown
//   - Attention (subjective): needs | acknowledged | dismissed
//
// Dismiss silences a session until the next live event (any hook or transcript
// append clears the dismissal). Permission prompts are a separate sub-state of "waiting"
// because they need different visual treatment in the HUD.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nitzanz/visor/internal/transcript"
)

var homeDir = func() string {
	h, _ := os.UserHomeDir()
	return h
}()

func toDisplayCWD(p string) string {
	if homeDir != "" && strings.HasPrefix(p, homeDir) {
		return "~" + p[len(homeDir):]
	}
	return p
}

type Attention int

const (
	AttentionAck     Attention = iota // user has acknowledged the current state
	AttentionNeeds                    // session wants attention
	AttentionDismiss                  // silenced until activity changes
)

func (a Attention) String() string {
	switch a {
	case AttentionNeeds:
		return "needs"
	case AttentionDismiss:
		return "dismissed"
	}
	return "ack"
}

type Waiting int

const (
	WaitingNone       Waiting = iota
	WaitingUser               // Claude finished a turn, waiting for input
	WaitingPermission         // Claude is blocked on a tool-approval prompt
)

// Session is one Claude Code session as the daemon understands it.
type Session struct {
	ID             string `json:"id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd,omitempty"`
	PID            int    `json:"pid,omitempty"`
	WindowID       string `json:"window_id,omitempty"` // WM-specific locator
	WM             string `json:"wm,omitempty"`        // "niri" | "sway" | "hypr" | "x11" | "tmux"
	TmuxPane       string `json:"tmux_pane,omitempty"`
	// JumpCmd is the launcher-declared custom jump command captured at
	// SessionStart from $VISOR_JUMP_CMD. Empty for ordinary sessions.
	JumpCmd string `json:"jump_cmd,omitempty"`

	// Title sources (both come from the JSONL; customTitle wins when set).
	AiTitle     string `json:"ai_title,omitempty"`
	CustomTitle string `json:"custom_title,omitempty"`

	Activity  transcript.SessionActivity `json:"-"`
	Waiting   Waiting                    `json:"-"`
	Attention Attention                  `json:"-"`

	// Background work axis (objective, derived from JSONL; never persisted).
	// BackgroundRunning is the set of in-flight background task IDs.
	// BackgroundOutcome is the result of the last finished batch:
	// "" | "done" | "failed". batchFailed accumulates failures within the
	// current batch and resets when the running set goes empty→non-empty.
	BackgroundRunning map[string]bool `json:"-"`
	BackgroundOutcome string          `json:"-"`
	batchFailed       bool

	// Tailer cursor.
	Offset int64 `json:"-"`

	FirstSeen   time.Time `json:"first_seen"`
	LastUpdate  time.Time `json:"last_update"`
	LastWaiting time.Time `json:"last_waiting,omitempty"`

	// StateSince is when (Activity, Waiting) last changed. Attention changes
	// do NOT move it: the HUD renders "time in current state", and dismissing
	// a session is not a state change in that sense.
	//
	// Deliberately NOT in the broadcast digest, and it does not need to be —
	// it only moves when a digested field moves. Rendering elapsed from
	// LastUpdate instead would freeze the counter, because a transcript
	// append to an already-working session changes no digested field and so
	// fires no broadcast.
	StateSince time.Time `json:"state_since"`

	// Ended is true once SessionEnd has fired. Ended sessions stay in the
	// store as tombstones — excluded from Snapshot (so they vanish from the
	// HUD) but kept so discovery's UpsertByPath returns the existing entry
	// instead of resurrecting a fresh tab from the on-disk transcript.
	Ended bool `json:"-"`
}

// Snapshot is the public view (used by ctl + HUD).
//
// Fields are non-omitempty by design — the HUD's yuck expressions need
// every key present (null access on missing keys is fragile in simplexpr).
type Snapshot struct {
	ID                string    `json:"id"`
	TranscriptPath    string    `json:"transcript_path"`
	CWD               string    `json:"cwd"`
	DisplayCWD        string    `json:"display_cwd"` // CWD with $HOME → "~"
	PID               int       `json:"pid"`
	WM                string    `json:"wm"`
	WindowID          string    `json:"window_id"`
	TmuxPane          string    `json:"tmux_pane"`
	Title             string    `json:"title"`
	Activity          string    `json:"activity"`
	Waiting           string    `json:"waiting"`
	Attention         string    `json:"attention"`
	BackgroundRunning int       `json:"background_running"`
	BackgroundOutcome string    `json:"background_outcome"`
	FirstSeen         time.Time `json:"first_seen"`
	LastUpdate        time.Time `json:"last_update"`
	DisplayName       string    `json:"display_name"`
	Glyph             string    `json:"glyph"`
	StateSince        time.Time `json:"state_since"`
}

// resolvedTitle is what the HUD should display. Custom (user-set) beats
// AI-generated; either beats falling through to cwd in the dock's display
// logic.
func (s *Session) resolvedTitle() string {
	if s.CustomTitle != "" {
		return s.CustomTitle
	}
	return s.AiTitle
}

func waitingString(w Waiting) string {
	switch w {
	case WaitingUser:
		return "user"
	case WaitingPermission:
		return "permission"
	}
	return ""
}

// Store is the concurrent session registry.
type Store struct {
	mu        sync.RWMutex
	sessions  map[string]*Session // keyed by session ID (UUID)
	byPath    map[string]string   // transcript path → session ID
	dismissed map[string]bool     // persisted across restarts; keyed by real UUID
	subs      *Subscribers
}

func NewStore() *Store {
	persistedSess, dismissed, err := LoadPersisted()
	if err != nil {
		// Non-fatal — bad persisted state shouldn't block startup.
		dismissed = map[string]bool{}
		persistedSess = nil
	}
	s := &Store{
		sessions:  map[string]*Session{},
		byPath:    map[string]string{},
		dismissed: dismissed,
		subs:      NewSubscribers(),
	}
	// Hydrate sessions from disk. Activity/Waiting/Attention are intentionally
	// not restored — they're derived state that gets recomputed by the tailer
	// (and re-armed by future hook events). Dismiss is restored via the
	// dismissed set.
	for _, p := range persistedSess {
		sess := &Session{
			ID:             p.ID,
			TranscriptPath: p.TranscriptPath,
			CWD:            p.CWD,
			PID:            p.PID,
			WM:             p.WM,
			WindowID:       p.WindowID,
			TmuxPane:       p.TmuxPane,
			JumpCmd:        p.JumpCmd,
			FirstSeen:      p.FirstSeen,
			Ended:          p.Ended,
		}
		if sess.FirstSeen.IsZero() {
			sess.FirstSeen = time.Now()
		}
		// Activity/Waiting are recomputed by the tailer, but StateSince needs
		// a non-zero seed now — otherwise a hydrated session that never gets
		// re-tailed before the HUD reads it renders as ~2562047h elapsed
		// (time.Since(zero)), since render.ElapsedString only clamps negative
		// durations. FirstSeen is the best available proxy for "since when".
		sess.StateSince = sess.FirstSeen
		if s.dismissed[sess.ID] {
			sess.Attention = AttentionDismiss
		}
		s.sessions[sess.ID] = sess
		if sess.TranscriptPath != "" {
			s.byPath[sess.TranscriptPath] = sess.ID
		}
	}
	return s
}

// snapshotPersist builds the on-disk representation under the lock,
// returning the slice and dismissed map so I/O happens without holding it.
func (s *Store) snapshotPersist() []persistedSession {
	out := make([]persistedSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		// Skip sessions still keyed by transcript path (no real UUID yet).
		// We can't reliably restore them on next boot — they'd need to be
		// re-discovered from the JSONL anyway. The path-keyed entry will
		// adopt its real UUID once the tailer reads a few lines.
		if sess.ID == sess.TranscriptPath {
			continue
		}
		out = append(out, persistedSession{
			ID:             sess.ID,
			TranscriptPath: sess.TranscriptPath,
			CWD:            sess.CWD,
			PID:            sess.PID,
			WM:             sess.WM,
			WindowID:       sess.WindowID,
			TmuxPane:       sess.TmuxPane,
			JumpCmd:        sess.JumpCmd,
			FirstSeen:      sess.FirstSeen,
			Dismissed:      sess.Attention == AttentionDismiss,
			Ended:          sess.Ended,
		})
	}
	return out
}

// Subscribers exposes the pub/sub registry so the IPC layer can attach.
func (s *Store) Subscribers() *Subscribers { return s.subs }

// notify computes a fresh snapshot and broadcasts to subscribers, then
// persists the durable parts of state. The Subscribers layer dedupes by
// HUD-relevant digest, so HUD broadcasts are cheap; disk writes are tiny
// (a few KB JSON) and happen on every mutation. If this becomes hot we
// can add a debounce later.
func (s *Store) notify() {
	s.subs.Broadcast(s.Snapshot())
	s.mu.RLock()
	ps := s.snapshotPersist()
	s.mu.RUnlock()
	_ = savePersisted(ps)
}

// UpsertByPath finds-or-creates a session keyed by transcript path. Session ID
// is filled in once we observe it inside the JSONL (or via a hook).
func (s *Store) UpsertByPath(path string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byPath[path]; ok {
		return s.sessions[id]
	}
	// Tentative: use path as the synthetic key until JSONL reveals real ID.
	now := time.Now()
	sess := &Session{
		TranscriptPath: path,
		ID:             path,
		FirstSeen:      now,
		StateSince:     now,
	}
	s.sessions[sess.ID] = sess
	s.byPath[path] = sess.ID
	return sess
}

// adoptID re-keys a session from a synthetic path-key to its real UUID once seen.
// Must be called with s.mu held.
func (s *Store) adoptID(sess *Session, realID string) {
	if sess.ID == realID || realID == "" {
		return
	}
	delete(s.sessions, sess.ID)
	sess.ID = realID
	s.sessions[realID] = sess
	s.byPath[sess.TranscriptPath] = realID
	// Re-apply persisted dismiss now that the real ID is known.
	if s.dismissed[realID] {
		sess.Attention = AttentionDismiss
	}
}

// applyBackground folds background lifecycle events into the session's
// Background axis. On backfill (isInitial) the net running count is computed
// but no outcome dot is surfaced — historical completions aren't news.
func (sess *Session) applyBackground(events []transcript.BackgroundEvent, isInitial bool) {
	for _, e := range events {
		switch e.Kind {
		case transcript.BackgroundStart:
			if len(sess.BackgroundRunning) == 0 {
				// New batch begins: reset accumulators and clear any
				// lingering outcome from the previous batch.
				sess.batchFailed = false
				sess.BackgroundOutcome = ""
			}
			if sess.BackgroundRunning == nil {
				sess.BackgroundRunning = map[string]bool{}
			}
			sess.BackgroundRunning[e.TaskID] = true
		case transcript.BackgroundFinish:
			if e.Failed {
				sess.batchFailed = true
			}
			delete(sess.BackgroundRunning, e.TaskID)
			if len(sess.BackgroundRunning) == 0 && !isInitial {
				if sess.batchFailed {
					sess.BackgroundOutcome = "failed"
				} else {
					sess.BackgroundOutcome = "done"
				}
			}
		}
	}
}

// ApplyTranscript folds parsed transcript lines into the session, returning
// whether the activity changed (caller decides whether to re-arm attention).
// When isInitial is true, transitions don't arm attention (backfill shouldn't nag).
func (s *Store) ApplyTranscript(path string, lines []transcript.Line, newOffset int64, isInitial bool) (changed bool) {
	defer s.notify()
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byPath[path]
	if !ok {
		// shouldn't happen — discovery calls UpsertByPath first
		return false
	}
	sess := s.sessions[id]
	sess.Offset = newOffset
	sess.LastUpdate = time.Now()
	for _, ln := range lines {
		if ln.SessionID != "" && sess.ID != ln.SessionID {
			s.adoptID(sess, ln.SessionID)
		}
		if ln.CWD != "" && sess.CWD == "" {
			sess.CWD = ln.CWD
		}
		// Title records arrive repeatedly through the session; latest non-empty
		// wins. customTitle (user-set, e.g. `/branch <name>`) takes precedence
		// over aiTitle (Claude-generated) at display time.
		switch ln.Type {
		case "ai-title":
			if ln.AiTitle != "" {
				sess.AiTitle = ln.AiTitle
			}
		case "custom-title":
			if ln.CustomTitle != "" {
				sess.CustomTitle = ln.CustomTitle
			}
		}
	}
	prev := sess.Activity
	// Activity is derived from the *whole* recent tail, but for new appends
	// the last line decides. We hand the parser-provided slice through; if
	// it's empty, retain prior state.
	if len(lines) > 0 {
		newAct := transcript.Classify(lines)
		if newAct != transcript.ActivityUnknown {
			sess.Activity = newAct
		}
	}
	if len(lines) > 0 {
		sess.applyBackground(transcript.ScanBackground(lines), isInitial)
	}
	// Live transcript appends on a dismissed session clear the dismissal —
	// new activity means the user is engaging again. Backfill (isInitial)
	// must not un-silence; that's the whole point of persisting dismiss.
	if !isInitial && len(lines) > 0 && sess.Attention == AttentionDismiss {
		sess.Attention = AttentionAck
		delete(s.dismissed, sess.ID)
	}
	// Live appends also un-tombstone an ended session. A `/branch`, `/resume`,
	// or fork keeps writing to the transcript; an already-running revived
	// session won't fire a fresh SessionStart, so the tailer is the only signal
	// that brings it back. Backfill (isInitial) must NOT clear Ended — that's
	// exactly the startup resurrection the tombstone exists to prevent.
	if !isInitial && len(lines) > 0 && sess.Ended {
		sess.Ended = false
	}
	if sess.Activity != prev {
		changed = true
		sess.StateSince = time.Now()
		if sess.Activity == transcript.ActivityWaitingUser {
			sess.LastWaiting = time.Now()
			sess.Waiting = WaitingUser
			if !isInitial {
				sess.Attention = AttentionNeeds
			}
		} else if sess.Activity == transcript.ActivityWorking {
			sess.Waiting = WaitingNone
			if sess.Attention == AttentionNeeds {
				sess.Attention = AttentionAck
			}
		}
	}
	return changed
}

// Dismiss silences a session until the next live event arrives for it
// (any hook or transcript append clears the dismissal in ApplyHook /
// ApplyTranscript). The dismiss is persisted across daemon restarts so
// silencing survives a reboot when the session is idle.
func (s *Store) Dismiss(id string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	sess.Attention = AttentionDismiss
	s.dismissed[sess.ID] = true
	s.mu.Unlock()
	s.notify()
	return true
}

// Acknowledge marks attention as seen but not dismissed.
func (s *Store) Acknowledge(id string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	sess.Attention = AttentionAck
	delete(s.dismissed, sess.ID)
	s.mu.Unlock()
	s.notify()
	return true
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *Store) Snapshot() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snapshot, 0, len(s.sessions))
	// Stable order matters: the HUD polls this every second; map iteration
	// is randomized in Go, so an unsorted slice makes tabs swap positions
	// on every refresh.
	for _, sess := range s.sessions {
		if sess.Ended {
			continue
		}
		out = append(out, Snapshot{
			ID:                sess.ID,
			TranscriptPath:    sess.TranscriptPath,
			CWD:               sess.CWD,
			DisplayCWD:        toDisplayCWD(sess.CWD),
			PID:               sess.PID,
			WM:                sess.WM,
			WindowID:          sess.WindowID,
			TmuxPane:          sess.TmuxPane,
			Title:             sess.resolvedTitle(),
			Activity:          sess.Activity.String(),
			Waiting:           waitingString(sess.Waiting),
			Attention:         sess.Attention.String(),
			BackgroundRunning: len(sess.BackgroundRunning),
			BackgroundOutcome: sess.BackgroundOutcome,
			FirstSeen:         sess.FirstSeen,
			LastUpdate:        sess.LastUpdate,
			DisplayName:       projectName(sess.resolvedTitle(), sess.CWD, sess.ID),
			StateSince:        sess.StateSince,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := dockRank(out[i]), dockRank(out[j])
		if pi != pj {
			return pi < pj
		}
		return out[i].FirstSeen.Before(out[j].FirstSeen)
	})
	assignGlyphs(out)
	return out
}

// projectName is what the HUD shows on the panel's first line: the session
// title if Claude or the user set one, else the cwd's basename, else a short
// id. Computed server-side so backends stay logic-free.
func projectName(title, cwd, id string) string {
	if title != "" {
		return title
	}
	if base := filepath.Base(cwd); cwd != "" && base != "." && base != "/" {
		return base
	}
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// assignGlyphs fills Glyph with a 1-2 character project identifier. A single
// uppercase initial is used unless two live sessions collide on it, in which
// case every colliding session widens to two letters — a lone widened glyph
// would be more confusing than helpful.
//
// Runes, not bytes: DisplayName may be non-ASCII (accented Latin, Hebrew,
// etc.), and slicing raw UTF-8 bytes would produce a mangled partial rune.
func assignGlyphs(snaps []Snapshot) {
	counts := map[string]int{}
	for _, s := range snaps {
		if s.DisplayName == "" {
			continue
		}
		first, _ := utf8.DecodeRuneInString(s.DisplayName)
		counts[strings.ToUpper(string(first))]++
	}
	for i := range snaps {
		n := snaps[i].DisplayName
		if n == "" {
			continue
		}
		runes := []rune(n)
		first := strings.ToUpper(string(runes[0]))
		if counts[first] > 1 && len(runes) > 1 {
			snaps[i].Glyph = strings.ToUpper(string(runes[:2]))
			continue
		}
		snaps[i].Glyph = first
	}
}

// dockRank: smaller = higher in the dock. Sessions are tiered so the eye
// can sweep top-to-bottom in order of "how much should I care right now":
//
//	0  needs      — waiting for you (idle or permission)
//	1  working    — busy, doesn't need you but worth watching
//	2  idle       — waiting but not nagging (you already engaged)
//	3  dismissed  — silenced until next state change
func dockRank(s Snapshot) int {
	switch s.Attention {
	case "needs":
		return 0
	case "dismissed":
		return 3
	}
	if s.Activity == "working" {
		return 1
	}
	return 2
}

// MarshalSnapshot returns the snapshot as indented JSON for ctl/HUD consumption.
func (s *Store) MarshalSnapshot() ([]byte, error) {
	return json.MarshalIndent(s.Snapshot(), "", "  ")
}
