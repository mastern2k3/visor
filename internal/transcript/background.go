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
var startRe = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9]+)`)

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
				continue
			}
			// Fix 2: find ALL task-notification blocks in the text.
			ids := taskIDRe.FindAllStringSubmatch(text, -1)
			statuses := statusRe.FindAllStringSubmatch(text, -1)
			for i, id := range ids {
				taskID := strings.TrimSpace(id[1]) // Fix 4: trim captured groups
				failed := true
				if i < len(statuses) && strings.TrimSpace(statuses[i][1]) == "completed" {
					failed = false
				}
				out = append(out, BackgroundEvent{TaskID: taskID, Kind: BackgroundFinish, Failed: failed})
			}
		}
	}
	return out
}
