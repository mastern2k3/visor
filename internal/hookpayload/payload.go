// Package hookpayload defines the JSON shape exchanged between the
// `visor hook` CLI and the daemon. The hook CLI parses Claude Code's stdin
// payload (documented fields: session_id, transcript_path, cwd, message,
// title) and augments it with locally-discovered metadata (pid, wm info).
package hookpayload

// FromClaude is the documented stdin payload Claude Code sends to hooks.
// Unknown fields are tolerated by json.Unmarshal.
type FromClaude struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Message        string `json:"message,omitempty"`
	Title          string `json:"title,omitempty"`
	HookEventName  string `json:"hook_event_name,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

// Enriched is the payload the daemon receives over the socket.
type Enriched struct {
	FromClaude
	PID      int    `json:"pid,omitempty"`
	WM       string `json:"wm,omitempty"`
	WindowID string `json:"window_id,omitempty"`
	TmuxPane string `json:"tmux_pane,omitempty"`
	// JumpCmd is the value of $VISOR_JUMP_CMD captured at SessionStart.
	// When non-empty, focus.Dispatch runs this via `sh -c` instead of the
	// built-in WM and tmux focus paths.
	JumpCmd string `json:"jump_cmd,omitempty"`
	// Matcher is the hook-config matcher string we registered with, used to
	// distinguish Notification subtypes (permission_prompt vs idle_prompt)
	// since Claude doesn't put that in the payload.
	Matcher string `json:"matcher,omitempty"`
}
