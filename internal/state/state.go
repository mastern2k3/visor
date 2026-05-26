// Package state holds the daemon's view of live Claude sessions.
//
// Two orthogonal axes:
//   - Activity (from JSONL/hooks): working | waiting | unknown
//   - Attention (subjective): needs | acknowledged | dismissed
//
// Dismiss silences a session until the next activity transition (working↔waiting
// re-arms attention). Permission prompts are a separate sub-state of "waiting"
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
	Title          string    `json:"title,omitempty"`

	Activity  transcript.SessionActivity `json:"-"`
	Waiting   Waiting                    `json:"-"`
	Attention Attention                  `json:"-"`

	// Tailer cursor.
	Offset int64 `json:"-"`

	FirstSeen   time.Time `json:"first_seen"`
	LastUpdate  time.Time `json:"last_update"`
	LastWaiting time.Time `json:"last_waiting,omitempty"`
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
	mu       sync.RWMutex
	sessions map[string]*Session // keyed by session ID (UUID)
	byPath   map[string]string   // transcript path → session ID
	subs     *Subscribers
}

func NewStore() *Store {
	return &Store{
		sessions: map[string]*Session{},
		byPath:   map[string]string{},
		subs:     NewSubscribers(),
	}
}

// Subscribers exposes the pub/sub registry so the IPC layer can attach.
func (s *Store) Subscribers() *Subscribers { return s.subs }

// notify computes a fresh snapshot and broadcasts to subscribers.
// The Subscribers layer dedupes by HUD-relevant digest, so this is safe
// to call from any mutation path.
func (s *Store) notify() {
	s.subs.Broadcast(s.Snapshot())
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
		// ai-title records arrive repeatedly through the session; the latest
		// one wins. Treat any non-empty value as an update.
		if ln.Type == "ai-title" && ln.AiTitle != "" {
			sess.Title = ln.AiTitle
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
	if sess.Activity != prev {
		changed = true
		if sess.Activity == transcript.ActivityWaitingUser {
			sess.LastWaiting = time.Now()
			sess.Waiting = WaitingUser
			if !isInitial && sess.Attention != AttentionDismiss {
				sess.Attention = AttentionNeeds
			}
		} else if sess.Activity == transcript.ActivityWorking {
			sess.Waiting = WaitingNone
			// Working means the user has engaged — clear all attention state.
			sess.Attention = AttentionAck
		}
	}
	return changed
}

// Dismiss silences a session until the next activity transition.
func (s *Store) Dismiss(id string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	sess.Attention = AttentionDismiss
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
	// is randomized in Go, so an unsorted slice makes tongues swap positions
	// on every refresh.
	for _, sess := range s.sessions {
		out = append(out, Snapshot{
			ID:             sess.ID,
			TranscriptPath: sess.TranscriptPath,
			CWD:            sess.CWD,
			DisplayCWD:     toDisplayCWD(sess.CWD),
			PID:            sess.PID,
			WM:             sess.WM,
			WindowID:       sess.WindowID,
			TmuxPane:       sess.TmuxPane,
			Title:          sess.Title,
			Activity:       sess.Activity.String(),
			Waiting:        waitingString(sess.Waiting),
			Attention:      sess.Attention.String(),
			FirstSeen:      sess.FirstSeen,
			LastUpdate:     sess.LastUpdate,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := attentionRank(out[i].Attention), attentionRank(out[j].Attention)
		if pi != pj {
			return pi < pj
		}
		return out[i].FirstSeen.Before(out[j].FirstSeen)
	})
	return out
}

// attentionRank: smaller = higher in the dock (more urgent first).
func attentionRank(a string) int {
	switch a {
	case "needs":
		return 0
	case "ack":
		return 1
	case "dismissed":
		return 2
	}
	return 3
}

// MarshalSnapshot returns the snapshot as indented JSON for ctl/HUD consumption.
func (s *Store) MarshalSnapshot() ([]byte, error) {
	return json.MarshalIndent(s.Snapshot(), "", "  ")
}
