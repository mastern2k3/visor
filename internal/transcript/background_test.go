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
