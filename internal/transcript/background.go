package transcript

import (
	"regexp"
	"strings"
)

// BackgroundKind distinguishes a task launch from a task completion.
type BackgroundKind int

const (
	BackgroundStart BackgroundKind = iota
	BackgroundFinish
)

// BackgroundEvent is one background-task lifecycle marker found in the
// transcript. TaskID is the Claude-assigned background task id (e.g. "bkgABC").
// Failed is meaningful only for BackgroundFinish.
type BackgroundEvent struct {
	TaskID string
	Kind   BackgroundKind
	Failed bool
}

// taskIDRe / statusRe extract all fields from <task-notification> finish blocks.
var taskIDRe = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
var statusRe = regexp.MustCompile(`<status>([^<]+)</status>`)

// ScanBackground walks parsed lines (any order) and returns the background
// lifecycle events found in them. Lines built by hand rather than by ParseFile
// carry none of this — every field it reads is lifted during parsing.
//
// Starts and stops come from the line's structured `toolUseResult`
// (Line.BgStartID / Line.BgStopID), never from prose. The launch sentence used
// to be regexed out of tool_result text, which meant any session that merely
// *quoted* the sentence — a grep over a transcript, a doc about this feature —
// booked a start whose finish could never arrive, and its tab breathed forever.
//
// Finishes come from <task-notification> markers, read off the raw line
// (Line.Notif) because they land on a user line, a queue-operation line, or an
// attachment line depending on how the session was running. A stop is a finish
// too, and the only one a deliberately stopped task ever gets: TaskStop emits
// no notification at all.
func ScanBackground(lines []Line) []BackgroundEvent {
	var out []BackgroundEvent
	for _, ln := range lines {
		if ln.BgStartID != "" {
			out = append(out, BackgroundEvent{TaskID: ln.BgStartID, Kind: BackgroundStart})
		}
		if ln.BgStopID != "" {
			out = append(out, BackgroundEvent{TaskID: ln.BgStopID, Kind: BackgroundFinish})
		}
		out = append(out, finishEvents(ln.Notif)...)
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
