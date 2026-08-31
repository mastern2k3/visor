package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// scannerBufMax must exceed the largest tool_result payload Claude persists.
// ccdiag uses 10MB; tool results occasionally hit a few MB.
const scannerBufMax = 10 * 1024 * 1024

// bgStartKey / bgStopMsg gate the second, targeted unmarshal that lifts the
// background task ids out of `toolUseResult`. Cheap byte scans first because
// re-parsing every line to reach one nested field would mean walking multi-MB
// tool results for nothing.
var bgStartKey = []byte(`"backgroundTaskId"`)
var bgStopMsg = []byte("Successfully stopped task:")

// notifTag marks a line as carrying a background task-notification. Only such
// lines keep their raw JSON (Line.Notif) — copying every line's bytes would
// mean holding a second copy of multi-MB tool results for nothing.
// Assumes the writer does not HTML-escape `<` (Claude Code uses
// JSON.stringify, which does not); a \u003c-escaping writer would hide it.
var notifTag = []byte("<task-id>")

func decodeLine(b []byte) (Line, bool) {
	var ln Line
	if err := json.Unmarshal(b, &ln); err != nil {
		return ln, false
	}
	if bytes.Contains(b, notifTag) {
		ln.Notif = string(b)
	}
	if bytes.Contains(b, bgStartKey) || bytes.Contains(b, bgStopMsg) {
		// toolUseResult is polymorphic across tools (some write a bare
		// string), so a decode failure here is expected and ignored — Bash
		// results, the only ones that matter, are always objects.
		var t struct {
			R struct {
				BackgroundTaskID string `json:"backgroundTaskId"`
				TaskID           string `json:"task_id"`
				Message          string `json:"message"`
			} `json:"toolUseResult"`
		}
		if json.Unmarshal(b, &t) == nil {
			ln.BgStartID = t.R.BackgroundTaskID
			// task_id alone is not a stop: gate on the message so a future
			// result that merely names a task cannot finish a running one.
			if strings.HasPrefix(t.R.Message, "Successfully stopped task:") {
				ln.BgStopID = t.R.TaskID
			}
		}
	}
	return ln, true
}

// ParseFile reads every line of a JSONL transcript. Malformed lines are
// silently skipped (callers may want to log; for state classification we
// just need the recent good ones).
func ParseFile(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseReader(f)
}

func parseReader(r io.Reader) ([]Line, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBufMax)
	var out []Line
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		ln, ok := decodeLine(b)
		if !ok {
			continue
		}
		out = append(out, ln)
	}
	return out, sc.Err()
}

// ParseAppended reads from offset to EOF, returning new lines and the new
// offset. Used by the tailer to incrementally consume appended JSONL.
//
// If the file shrank below offset (truncate/rotate), we reread from 0.
func ParseAppended(path string, offset int64) (lines []Line, newOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	start := offset
	if fi.Size() < offset {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, offset, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBufMax)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		ln, ok := decodeLine(b)
		if !ok {
			continue
		}
		lines = append(lines, ln)
	}
	// Scanner may have read partial trailing line; report position via Stat
	// rather than scanner internals. Re-stat to capture appends during scan.
	if fi2, e := os.Stat(path); e == nil {
		newOffset = fi2.Size()
	} else {
		newOffset = start
	}
	return lines, newOffset, sc.Err()
}
