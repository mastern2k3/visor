package transcript

import (
	"encoding/json"
	"testing"
)

// userLine builds a `user` Line whose message.content is the given JSON blocks.
func userLine(contentJSON string) Line {
	return Line{Type: "user", Message: &MessageBody{Role: "user", Content: json.RawMessage(contentJSON)}}
}

func TestScanBackground_Start(t *testing.T) {
	lines := []Line{userLine(`[{"type":"tool_result","content":"Command running in background with ID: bkgABC. Output is being written to: /tmp/x. You will be notified when it completes."}]`)}
	got := ScanBackground(lines)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Kind != BackgroundStart || got[0].TaskID != "bkgABC" {
		t.Errorf("got %+v, want Start bkgABC", got[0])
	}
}

func TestScanBackground_FinishCompleted(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgABC</task-id>\n<status>completed</status>\n<summary>ok</summary>\n</task-notification>"}]`
	got := ScanBackground([]Line{userLine(content)})
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Kind != BackgroundFinish || got[0].TaskID != "bkgABC" || got[0].Failed {
		t.Errorf("got %+v, want Finish bkgABC failed=false", got[0])
	}
}

func TestScanBackground_FinishFailed(t *testing.T) {
	content := `[{"type":"text","text":"<task-notification>\n<task-id>bkgZ9</task-id>\n<status>failed</status>\n</task-notification>"}]`
	got := ScanBackground([]Line{userLine(content)})
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

// TestScanBackground_StartFromContentArray exercises Fix 1: the tool_result
// whose `content` field is a JSON array of blocks (not a bare string).
func TestScanBackground_StartFromContentArray(t *testing.T) {
	// content is an array of blocks containing the launch message
	inner := `[{"type":"text","text":"Command running in background with ID: bkgXYZ. Output is being written to: /tmp/out. You will be notified when it completes."}]`
	content := `[{"type":"tool_result","tool_use_id":"tu1","content":` + inner + `}]`
	got := ScanBackground([]Line{userLine(content)})
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got), got)
	}
	if got[0].Kind != BackgroundStart || got[0].TaskID != "bkgXYZ" {
		t.Errorf("got %+v, want Start bkgXYZ", got[0])
	}
}

// TestScanBackground_MultipleNotificationsInOneLine exercises Fix 2: a single
// user line carrying two <task-notification> blocks with different task-ids
// and mixed statuses. Both finish events must be returned.
func TestScanBackground_MultipleNotificationsInOneLine(t *testing.T) {
	text := "<task-notification>\n<task-id>bkgA1</task-id>\n<status>completed</status>\n</task-notification>\n" +
		"<task-notification>\n<task-id>bkgB2</task-id>\n<status>failed</status>\n</task-notification>"
	content := `[{"type":"text","text":"` + escapeJSON(text) + `"}]`
	got := ScanBackground([]Line{userLine(content)})
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

// escapeJSON escapes a string for embedding in a JSON string literal.
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// strip surrounding quotes
	return string(b[1 : len(b)-1])
}
