// Package ipc defines the daemon's Unix-socket wire format.
//
// One JSON object per line. Client sends a Request, server replies with a
// single Response and closes. Keep this dumb — it's not a long-lived stream.
package ipc

import "encoding/json"

type Request struct {
	Cmd  string          `json:"cmd"`            // list | jump | dismiss | ack | hook | json
	ID   string          `json:"id,omitempty"`   // session id (for jump/dismiss/ack)
	Hook string          `json:"hook,omitempty"` // SessionStart | Stop | Notification | ...
	Body json.RawMessage `json:"body,omitempty"` // hook payload, etc.
}

type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`

	// Stream, when non-nil, switches the server into streaming mode after
	// sending the initial response: each subsequent []byte is written as a
	// newline-terminated frame until the channel closes or the client
	// disconnects. Marshaled JSON drops this field via the dash tag.
	Stream <-chan []byte `json:"-"`
}
