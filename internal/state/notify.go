package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// Subscriber receives one JSON snapshot per HUD-meaningful state change.
//
// Channels are buffered; if a slow consumer fills its buffer the daemon
// drops the message rather than block state-mutation paths.
type Subscriber struct {
	ch chan []byte
}

func (s *Subscriber) Chan() <-chan []byte { return s.ch }

// Subscribers is a registry of live subscribers + last-broadcast digest.
type Subscribers struct {
	mu           sync.Mutex
	subs         map[*Subscriber]struct{}
	lastDigest   string
	lastSnapshot []byte
}

func NewSubscribers() *Subscribers {
	return &Subscribers{subs: map[*Subscriber]struct{}{}}
}

// Add registers a new subscriber and immediately sends the current snapshot
// so a fresh client doesn't have to wait for the next change to draw anything.
func (s *Subscribers) Add() *Subscriber {
	sub := &Subscriber{ch: make(chan []byte, 4)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub] = struct{}{}
	if s.lastSnapshot != nil {
		select {
		case sub.ch <- s.lastSnapshot:
		default:
		}
	}
	return sub
}

func (s *Subscribers) Remove(sub *Subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		close(sub.ch)
	}
}

// Broadcast emits the snapshot to all subscribers if its HUD-relevant digest
// has changed since the last broadcast. The digest hashes only the fields
// the HUD actually renders — so background tailer activity (LastUpdate
// changing every second) doesn't trigger redundant redraws.
func (s *Subscribers) Broadcast(snaps []Snapshot) {
	d := hudDigest(snaps)
	b, err := json.Marshal(snaps)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == s.lastDigest {
		// Update the cached snapshot bytes so newly-added subscribers still
		// get the latest data — but don't push to existing subscribers.
		s.lastSnapshot = b
		return
	}
	s.lastDigest = d
	s.lastSnapshot = b
	for sub := range s.subs {
		select {
		case sub.ch <- b:
		default:
			// drop on backpressure
		}
	}
}

// hudDigest hashes only the fields that affect HUD rendering. Excluded:
// LastUpdate (changes constantly), FirstSeen (doesn't change), full paths
// (use display_cwd), pid (not rendered), wm/window/tmuxpane (not rendered).
func hudDigest(snaps []Snapshot) string {
	h := sha256.New()
	for _, s := range snaps {
		h.Write([]byte(s.ID))
		h.Write([]byte{0})
		h.Write([]byte(s.Activity))
		h.Write([]byte{0})
		h.Write([]byte(s.Waiting))
		h.Write([]byte{0})
		h.Write([]byte(s.Attention))
		h.Write([]byte{0})
		h.Write([]byte(s.DisplayCWD))
		h.Write([]byte{0})
		h.Write([]byte(s.Title))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}
