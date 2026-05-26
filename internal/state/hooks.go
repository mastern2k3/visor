package state

import (
	"time"

	"github.com/nitzanz/visor/internal/hookpayload"
	"github.com/nitzanz/visor/internal/transcript"
)

// ApplyHook folds a hook event into the session store. Returns the (possibly
// newly-created) session.
//
// Event semantics:
//
//   SessionStart        bind metadata (pid, wm, window_id, tmux pane, cwd)
//   SessionEnd          delete session
//   UserPromptSubmit    activity → working
//   Stop                activity → waiting (user)
//   Notification        matcher=permission_prompt → waiting (permission)
//                       matcher=idle_prompt       → waiting (user)
//   PreCompact          (ignored — transient)
func (s *Store) ApplyHook(event string, p hookpayload.Enriched) *Session {
	defer s.notify()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Resolve session: prefer transcript_path (stable across rename to real ID).
	var sess *Session
	if p.TranscriptPath != "" {
		if id, ok := s.byPath[p.TranscriptPath]; ok {
			sess = s.sessions[id]
		}
	}
	if sess == nil && p.SessionID != "" {
		sess = s.sessions[p.SessionID]
	}
	if sess == nil {
		sess = &Session{
			ID:             firstNonEmpty(p.SessionID, p.TranscriptPath),
			TranscriptPath: p.TranscriptPath,
			FirstSeen:      time.Now(),
		}
		s.sessions[sess.ID] = sess
		if p.TranscriptPath != "" {
			s.byPath[p.TranscriptPath] = sess.ID
		}
	}
	if p.SessionID != "" && sess.ID != p.SessionID {
		s.adoptID(sess, p.SessionID)
	}

	// Always-apply metadata.
	if p.CWD != "" {
		sess.CWD = p.CWD
	}
	if p.PID != 0 {
		sess.PID = p.PID
	}
	if p.WM != "" {
		sess.WM = p.WM
	}
	if p.WindowID != "" {
		sess.WindowID = p.WindowID
	}
	if p.TmuxPane != "" {
		sess.TmuxPane = p.TmuxPane
	}
	sess.LastUpdate = time.Now()

	switch event {
	case "SessionStart":
		// metadata already captured above
	case "SessionEnd":
		delete(s.sessions, sess.ID)
		if sess.TranscriptPath != "" {
			delete(s.byPath, sess.TranscriptPath)
		}
	case "UserPromptSubmit":
		s.transition(sess, transcript.ActivityWorking, WaitingNone)
	case "Stop":
		s.transition(sess, transcript.ActivityWaitingUser, WaitingUser)
	case "Notification":
		switch p.Matcher {
		case "permission_prompt":
			s.transition(sess, transcript.ActivityWaitingUser, WaitingPermission)
		case "idle_prompt":
			s.transition(sess, transcript.ActivityWaitingUser, WaitingUser)
		}
	}
	return sess
}

// transition applies an activity/waiting change and arms attention on the
// edge into a waiting state. Must be called with s.mu held.
func (s *Store) transition(sess *Session, act transcript.SessionActivity, w Waiting) {
	prevAct := sess.Activity
	sess.Activity = act
	sess.Waiting = w
	switch {
	case act == transcript.ActivityWaitingUser && prevAct != act:
		sess.LastWaiting = time.Now()
		if sess.Attention != AttentionDismiss {
			sess.Attention = AttentionNeeds
		}
	case act == transcript.ActivityWorking && prevAct != act:
		// Working means the user has engaged — clear any prior attention,
		// including dismiss (so the next wait re-arms cleanly).
		sess.Attention = AttentionAck
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
