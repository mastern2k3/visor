package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// parseLines runs raw JSONL through the real parser, which is the only way
// Line.Notif gets populated — finish markers are read off the raw line, so a
// hand-built Line literal can never carry one.
func parseLines(t *testing.T, jsonl ...string) []Line {
	t.Helper()
	lines, err := parseReader(strings.NewReader(strings.Join(jsonl, "\n")))
	if err != nil {
		t.Fatalf("parseReader: %v", err)
	}
	return lines
}

// userJSONL is one `user` JSONL record whose message.content is contentJSON.
func userJSONL(contentJSON string) string {
	return `{"type":"user","message":{"role":"user","content":` + contentJSON + `}}`
}

// userLine builds a `user` Line whose message.content is the given JSON blocks.
func userLine(contentJSON string) Line {
	return Line{Type: "user", Message: &MessageBody{Role: "user", Content: json.RawMessage(contentJSON)}}
}

func TestScanBackground_QuotedLaunchPhraseIsNotAStart(t *testing.T) {
	quoted := userJSONL(`[{"type":"tool_result","content":"Command running in background with ID: bo96ttzfi\nCommand running in background with ID: ba4wemrkv"}]`)
	if got := ScanBackground(parseLines(t, quoted)); len(got) != 0 {
		t.Errorf("ScanBackground = %+v, want no events for a quoted launch phrase", got)
	}
}

// A task stopped with TaskStop must count as finished. It never receives a
// <task-notification>, so its toolUseResult is the only finish marker there
// is; missing it left every stopped task counted as running forever.
func TestScanBackground_TaskStopFinishes(t *testing.T) {
	line := `{"type":"user","toolUseResult":{"message":"Successfully stopped task: bxugk6q45 (python deploy.py)","task_id":"bxugk6q45","task_type":"local_bash"}}`
	got := ScanBackground(parseLines(t, line))
	if len(got) != 1 {
		t.Fatalf("ScanBackground = %+v, want exactly 1 finish event", got)
	}
	if got[0].TaskID != "bxugk6q45" || got[0].Kind != BackgroundFinish || got[0].Failed {
		t.Errorf("got %+v, want {bxugk6q45 BackgroundFinish false}", got[0])
	}
}

func TestScanBackground_FinishCompleted(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgABC</task-id>\n<status>completed</status>\n<summary>ok</summary>\n</task-notification>"}]`
	got := ScanBackground(parseLines(t, userJSONL(content)))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Kind != BackgroundFinish || got[0].TaskID != "bkgABC" || got[0].Failed {
		t.Errorf("got %+v, want Finish bkgABC failed=false", got[0])
	}
}

func TestScanBackground_FinishFailed(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgZ9</task-id>\n<status>failed</status>\n</task-notification>"}]`
	got := ScanBackground(parseLines(t, userJSONL(content)))
	if len(got) != 1 || got[0].Kind != BackgroundFinish || !got[0].Failed {
		t.Fatalf("got %+v, want Finish failed=true", got)
	}
}

func TestScanBackground_IgnoresUnrelated(t *testing.T) {
	lines := []Line{
		userLine(`[{"type":"text","text":"just a normal message"}]`),
		{Type: "assistant", Message: &MessageBody{Role: "assistant", StopReason: "end_turn"}},
	}
	if got := ScanBackground(lines); len(got) != 0 {
		t.Errorf("got %d events, want 0: %+v", len(got), got)
	}
}

// TestScanBackground_MultipleNotificationsInOneLine exercises Fix 2: a single
// user line carrying two <task-notification> blocks with different task-ids
// and mixed statuses. Both finish events must be returned.
func TestScanBackground_MultipleNotificationsInOneLine(t *testing.T) {
	text := "<task-notification>\n<task-id>bkgA1</task-id>\n<status>completed</status>\n</task-notification>\n" +
		"<task-notification>\n<task-id>bkgB2</task-id>\n<status>failed</status>\n</task-notification>"
	content := `[{"type":"text","text":"` + escapeJSON(text) + `"}]`
	got := ScanBackground(parseLines(t, userJSONL(content)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	// first notification: bkgA1 completed
	if got[0].Kind != BackgroundFinish || got[0].TaskID != "bkgA1" || got[0].Failed {
		t.Errorf("event[0] = %+v, want Finish bkgA1 failed=false", got[0])
	}
	// second notification: bkgB2 failed
	if got[1].Kind != BackgroundFinish || got[1].TaskID != "bkgB2" || !got[1].Failed {
		t.Errorf("event[1] = %+v, want Finish bkgB2 failed=true", got[1])
	}
}

// escapeJSON escapes a string for embedding in a JSON string literal, with
// HTML escaping OFF: json.Marshal would turn `<` into `\u003c`, which no
// transcript writer does, and which would hide the markers from the raw-line
// scan the way a real file never does.
func escapeJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b := bytes.TrimSpace(buf.Bytes())
	return string(b[1 : len(b)-1])
}

// Every carrier a <task-notification> has actually been observed on must
// register a finish. Missing one leaves the task counted as running forever,
// which the HUD shows as a tab animating with nothing actually running — the
// queue-operation shape leaked that way first, then the attachment shape.
func TestScanBackground_FinishOnEveryCarrier(t *testing.T) {
	notif := `<task-notification>\n<task-id>%s</task-id>\n<status>completed</status>\n</task-notification>`
	for _, tc := range []struct{ name, jsonl, id string }{
		{"user", userJSONL(`[{"type":"text","text":"` + strings.Replace(notif, "%s", "bUser1", 1) + `"}]`), "bUser1"},
		{"queue-operation", `{"type":"queue-operation","content":"` + strings.Replace(notif, "%s", "bQueue1", 1) + `"}`, "bQueue1"},
		{"attachment", `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"` + strings.Replace(notif, "%s", "bAttach1", 1) + `"}}`, "bAttach1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanBackground(parseLines(t, tc.jsonl))
			if len(got) != 1 {
				t.Fatalf("ScanBackground = %+v, want exactly 1 finish event", got)
			}
			if got[0].TaskID != tc.id || got[0].Kind != BackgroundFinish || got[0].Failed {
				t.Errorf("got %+v, want {%s BackgroundFinish false}", got[0], tc.id)
			}
		})
	}
}

// A line that merely mentions background work without a task-id must not
// register anything — the raw-line scan is deliberately carrier-blind, so this
// pins that it is still marker-gated.
func TestScanBackground_NoMarkerNoEvents(t *testing.T) {
	lines := parseLines(t,
		`{"type":"queue-operation","content":"please run the tests in the background"}`,
		`{"type":"attachment","attachment":{"prompt":"task-notification"}}`,
	)
	if got := ScanBackground(lines); len(got) != 0 {
		t.Errorf("ScanBackground = %+v, want no events", got)
	}
}

// A Monitor is background work too, but reports its launch as `taskId` +
// `timeoutMs` rather than `backgroundTaskId` — so a session with a monitor
// open showed no background work at all.
func TestScanBackground_MonitorStart(t *testing.T) {
	line := `{"type":"user","toolUseResult":{"taskId":"b2z0e7oia","timeoutMs":1800000,"persistent":false}}`
	got := ScanBackground(parseLines(t, line))
	if len(got) != 1 || got[0].Kind != BackgroundStart || got[0].TaskID != "b2z0e7oia" {
		t.Fatalf("got %+v, want Start b2z0e7oia", got)
	}
}

// A bare `taskId` with no timeoutMs is some other tool's result, not a launch.
func TestScanBackground_TaskIDWithoutTimeoutIsNotAStart(t *testing.T) {
	line := `{"type":"user","toolUseResult":{"taskId":"t123","success":true,"statusChange":"done"}}`
	if got := ScanBackground(parseLines(t, line)); len(got) != 0 {
		t.Errorf("ScanBackground = %+v, want no events", got)
	}
}

// A Monitor reports every event it sees as a task-notification with no
// <status>. Those are progress, not completion: treating one as a finish
// stopped the tab breathing on the monitor's first event, while it watched on.
func TestScanBackground_MonitorEventIsNotAFinish(t *testing.T) {
	notif := `<task-notification>\n<task-id>b2z0e7oia</task-id>\n<summary>Monitor event: job status</summary>\n<event>19:23:32 INTAKE_PREPROCESSING</event>\n</task-notification>`
	line := `{"type":"queue-operation","content":"` + notif + `"}`
	if got := ScanBackground(parseLines(t, line)); len(got) != 0 {
		t.Errorf("ScanBackground = %+v, want no events for a status-less notification", got)
	}
}

// …and the terminal notification, which does carry a status, finishes it.
func TestScanBackground_MonitorTerminalStatusFinishes(t *testing.T) {
	notif := `<task-notification>\n<task-id>b2z0e7oia</task-id>\n<status>completed</status>\n</task-notification>`
	line := `{"type":"queue-operation","content":"` + notif + `"}`
	got := ScanBackground(parseLines(t, line))
	if len(got) != 1 || got[0].Kind != BackgroundFinish || got[0].Failed {
		t.Fatalf("got %+v, want a clean finish", got)
	}
}

// Blocks are read one at a time, so a status-less block in the middle cannot
// shift every later id onto the wrong status.
func TestScanBackground_MixedBlocksDoNotMisalignStatuses(t *testing.T) {
	text := `<task-notification>\n<task-id>bEvent</task-id>\n<summary>Monitor event</summary>\n</task-notification>\n` +
		`<task-notification>\n<task-id>bDone</task-id>\n<status>completed</status>\n</task-notification>`
	line := `{"type":"queue-operation","content":"` + text + `"}`
	got := ScanBackground(parseLines(t, line))
	if len(got) != 1 {
		t.Fatalf("got %+v, want exactly 1 finish", got)
	}
	if got[0].TaskID != "bDone" || got[0].Failed {
		t.Errorf("got %+v, want bDone completed — the status belongs to the block it sits in", got[0])
	}
}
