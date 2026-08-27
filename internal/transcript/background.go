package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

// BackgroundKind distinguishes a task launch from a task completion.
type BackgroundKind int

const (
	BackgroundStart  BackgroundKind = iota
	BackgroundFinish                // Fix 3: drop redundant = iota
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
//
// The "Output is being written to" clause is required, not decoration: without
// it the phrase also matches itself *quoted* in ordinary text — a session that
// greps a transcript, or writes about this feature, books a start whose finish
// can never arrive, and the tab breathes forever. Every real launch in the
// local corpus carries the clause; a wording change upstream would cost us
// starts, which is the failure direction to notice here.
var startRe = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9]+)\. Output is being written to:`)

// taskIDRe / statusRe extract all fields from <task-notification> finish blocks.
var taskIDRe = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
var statusRe = regexp.MustCompile(`<status>([^<]+)</status>`)

// toolResultText extracts the human-readable text from a tool_result block,
// whose `content` is polymorphic: a bare JSON string, or an array of
// content blocks (each possibly carrying its own Text). Mirrors DecodeContent.
func toolResultText(b Block) string {
	if b.Text != "" {
		return b.Text
	}
	if len(b.ContentRM) == 0 {
		return ""
	}
	// Try bare JSON string first.
	var s string
	if err := json.Unmarshal(b.ContentRM, &s); err == nil {
		return s
	}
	// Try array of content blocks.
	var blocks []Block
	if err := json.Unmarshal(b.ContentRM, &blocks); err == nil {
		var out string
		for _, ib := range blocks {
			out += ib.Text
		}
		return out
	}
	return ""
}

// ScanBackground walks parsed lines (any order) and returns the background
// lifecycle events found in them.
//
// Starts live in user-line tool_result content, decoded the way Classify does.
// Finishes are read off Line.Notif — the raw JSON of any line that mentions a
// task-id — because a <task-notification> lands on a user line, a
// queue-operation line, or an attachment line depending on how the session was
// running, and each carrier missed leaks the task as permanently "running".
// Lines built by hand rather than by ParseFile therefore report no finishes.
func ScanBackground(lines []Line) []BackgroundEvent {
	var out []BackgroundEvent
	for _, ln := range lines {
		out = append(out, finishEvents(ln.Notif)...)
		if ln.Type != "user" || ln.Message == nil {
			continue
		}
		for _, b := range DecodeContent(ln.Message.Content) {
			var text string
			if b.Type == "tool_result" {
				// Fix 1: properly decode polymorphic tool_result content.
				text = toolResultText(b)
			} else {
				text = b.Text
			}
			if text == "" {
				continue
			}
			if m := startRe.FindStringSubmatch(text); m != nil {
				out = append(out, BackgroundEvent{TaskID: strings.TrimSpace(m[1]), Kind: BackgroundStart})
			}
		}
	}
	return out
}

// finishEvents extracts every <task-notification> finish marker in text. A
// single blob can hold more than one, and the status decides Failed.
func finishEvents(text string) []BackgroundEvent {
	ids := taskIDRe.FindAllStringSubmatch(text, -1)
	if ids == nil {
		return nil
	}
	statuses := statusRe.FindAllStringSubmatch(text, -1)
	out := make([]BackgroundEvent, 0, len(ids))
	for i, id := range ids {
		failed := true
		if i < len(statuses) && strings.TrimSpace(statuses[i][1]) == "completed" {
			failed = false
		}
		out = append(out, BackgroundEvent{
			TaskID: strings.TrimSpace(id[1]),
			Kind:   BackgroundFinish,
			Failed: failed,
		})
	}
	return out
}
