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
	"sort"
	"strings"
	"sync"
	"time"

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
	AttentionAck      Attention = iota // user has acknowledged the current state
	AttentionNeeds                     // session wants attention
	AttentionDismiss                   // silenced until activity changes
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
	ID             string    `json:"id"`
	TranscriptPath string    `json:"transcript_path"`
	CWD            string    `json:"cwd,omitempty"`
	PID            int       `json:"pid,omitempty"`
	WindowID       string    `json:"window_id,omitempty"` // WM-specific locator
	WM             string    `json:"wm,omitempty"`        // "niri" | "sway" | "hypr" | "x11" | "tmux"
	TmuxPane       string    `json:"tmux_pane,omitempty"`
	// JumpCmd is the launcher-declared custom jump command captured at
	// SessionStart from $VISOR_JUMP_CMD. Empty for ordinary sessions.
	JumpCmd string `json:"jump_cmd,omitempty"`

	// Title sources (both come from the JSONL; customTitle wins when set).
	AiTitle     string `json:"ai_title,omitempty"`
	CustomTitle string `json:"custom_title,omitempty"`

	Activity  transcript.SessionActivity `json:"-"`
	Waiting   Waiting                    `json:"-"`
	Attention Attention                  `json:"-"`

	// Tailer cursor.
	Offset int64 `json:"-"`

	FirstSeen   time.Time `json:"first_seen"`
	LastUpdate  time.Time `json:"last_update"`
	LastWaiting time.Time `json:"last_waiting,omitempty"`

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
	ID             string    `json:"id"`
	TranscriptPath string    `json:"transcript_path"`
	CWD            string    `json:"cwd"`
	DisplayCWD     string    `json:"display_cwd"` // CWD with $HOME → "~"
	PID            int       `json:"pid"`
	WM             string    `json:"wm"`
	WindowID       string    `json:"window_id"`
	TmuxPane       string    `json:"tmux_pane"`
	Title          string    `json:"title"`
	Activity       string    `json:"activity"`
	Waiting        string    `json:"waiting"`
	Attention      string    `json:"attention"`
	FirstSeen      time.Time `json:"first_seen"`
	LastUpdate     time.Time `json:"last_update"`
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
	sess := &Session{
		TranscriptPath: path,
		ID:             path,
		FirstSeen:      time.Now(),
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
	// Live transcript appends on a dismissed session clear the dismissal —
	// new activity means the user is engaging again. Backfill (isInitial)
	// must not un-silence; that's the whole point of persisting dismiss.
	if !isInitial && len(lines) > 0 && sess.Attention == AttentionDismiss {
		sess.Attention = AttentionAck
		delete(s.dismissed, sess.ID)
	}
	if sess.Activity != prev {
		changed = true
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
			ID:             sess.ID,
			TranscriptPath: sess.TranscriptPath,
			CWD:            sess.CWD,
			DisplayCWD:     toDisplayCWD(sess.CWD),
			PID:            sess.PID,
			WM:             sess.WM,
			WindowID:       sess.WindowID,
			TmuxPane:       sess.TmuxPane,
			Title:          sess.resolvedTitle(),
			Activity:       sess.Activity.String(),
			Waiting:        waitingString(sess.Waiting),
			Attention:      sess.Attention.String(),
			FirstSeen:      sess.FirstSeen,
			LastUpdate:     sess.LastUpdate,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := dockRank(out[i]), dockRank(out[j])
		if pi != pj {
			return pi < pj
		}
		return out[i].FirstSeen.Before(out[j].FirstSeen)
	})
	return out
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
