package transcript

// SessionActivity is the activity state derived from the transcript.
// Orthogonal to attention state (which is daemon-managed).
type SessionActivity int

const (
	ActivityUnknown SessionActivity = iota
	ActivityWaitingUser              // last conversation line: assistant end_turn, or real user prompt
	ActivityWorking                  // last conversation line: assistant tool_use, or user tool_result
)

func (a SessionActivity) String() string {
	switch a {
	case ActivityWaitingUser:
		return "waiting"
	case ActivityWorking:
		return "working"
	}
	return "unknown"
}

// isMetadata reports whether a line type carries no conversational meaning
// for state classification. Such lines are walked past when searching for
// the last conversation-bearing record.
func isMetadata(typ string) bool {
	switch typ {
	case "user", "assistant":
		return false
	}
	return true
}

// Classify inspects a window of recent lines (oldest first) and returns the
// activity inferred from the most recent non-sidechain conversation line.
//
// Rules (from Owloops/claude-powerline metrics.ts + ccdiag analyzer):
//   - last is assistant, stop_reason="tool_use"     → Working
//   - last is assistant, stop_reason="end_turn"     → WaitingUser
//   - last is user, content[0].type=="tool_result"  → Working (model processing)
//   - last is user (real prompt), otherwise         → Unknown (don't classify)
//     A real user prompt as the latest line means Claude is processing it —
//     but the transcript alone can't tell us whether Claude has started
//     writing yet. Returning Unknown leaves the hook-driven activity intact
//     (UserPromptSubmit already set Working); we'd otherwise flip to Waiting
//     for a second until the assistant's first tool_use lands.
//   - sidechain lines ignored (subagent forks)
func Classify(lines []Line) SessionActivity {
	for i := len(lines) - 1; i >= 0; i-- {
		ln := lines[i]
		if ln.IsSidechain {
			continue
		}
		if isMetadata(ln.Type) {
			continue
		}
		if ln.Message == nil {
			continue
		}
		switch ln.Type {
		case "assistant":
			switch ln.Message.StopReason {
			case "tool_use":
				return ActivityWorking
			case "end_turn", "stop_sequence", "max_tokens":
				return ActivityWaitingUser
			default:
				// Mid-stream or unknown stop_reason; streaming isn't persisted
				// incrementally per our research, so this is rare. Treat as
				// working — better to over-report busy than to falsely nudge.
				return ActivityWorking
			}
		case "user":
			blocks := DecodeContent(ln.Message.Content)
			for _, b := range blocks {
				if b.Type == "tool_result" {
					return ActivityWorking
				}
			}
			// Real user prompt as the latest line — can't infer state from
			// transcript alone. Hooks (UserPromptSubmit / Stop) authoritatively
			// drive the working↔waiting transition; we defer to them.
			return ActivityUnknown
		}
	}
	return ActivityUnknown
}
