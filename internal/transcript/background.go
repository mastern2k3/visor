package transcript

import "regexp"

// BackgroundKind distinguishes a task launch from a task completion.
type BackgroundKind int

const (
	BackgroundStart  BackgroundKind = iota
	BackgroundFinish BackgroundKind = iota
)

// BackgroundEvent is one background-task lifecycle marker found in the
// transcript. TaskID is the Claude-assigned background task id (e.g. "bkgABC").
// Failed is meaningful only for BackgroundFinish.
type BackgroundEvent struct {
	TaskID string
	Kind   BackgroundKind
	Failed bool
}

// startRe matches the tool_result text emitted when a command is launched in
// the background. The id is alphanumeric (Claude uses a short "bkg…" token).
var startRe = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9]+)`)

// taskIDRe / statusRe extract fields from a <task-notification> finish block.
var taskIDRe = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
var statusRe = regexp.MustCompile(`<status>([^<]+)</status>`)

// ScanBackground walks parsed lines (any order) and returns the background
// lifecycle events found in user-line content. Both markers live inside
// user-line content blocks, so we decode content the same way Classify does.
//
// A <task-notification> can appear in the JSONL twice (a queue-operation line
// and a user line); only the user line is inspected here, and callers key
// events by TaskID into a set so a duplicate finish is a harmless no-op.
func ScanBackground(lines []Line) []BackgroundEvent {
	var out []BackgroundEvent
	for _, ln := range lines {
		if ln.Type != "user" || ln.Message == nil {
			continue
		}
		for _, b := range DecodeContent(ln.Message.Content) {
			text := b.Text
			if b.Type == "tool_result" {
				// tool_result content is itself polymorphic; the human-readable
				// string lands in Block.Text when content is a bare string, or
				// in ContentRM otherwise. Check both.
				if text == "" && len(b.ContentRM) > 0 {
					text = string(b.ContentRM)
				}
			}
			if text == "" {
				continue
			}
			if m := startRe.FindStringSubmatch(text); m != nil {
				out = append(out, BackgroundEvent{TaskID: m[1], Kind: BackgroundStart})
				continue
			}
			if id := taskIDRe.FindStringSubmatch(text); id != nil {
				failed := true
				if st := statusRe.FindStringSubmatch(text); st != nil && st[1] == "completed" {
					failed = false
				}
				out = append(out, BackgroundEvent{TaskID: id[1], Kind: BackgroundFinish, Failed: failed})
			}
		}
	}
	return out
}
