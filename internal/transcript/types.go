// Package transcript decodes Claude Code JSONL session files.
//
// Schema borrowed from kolkov/ccdiag (internal/parser/types.go) — undocumented
// upstream and version-dependent, so loose-typed where it matters.
package transcript

import (
	"encoding/json"
	"time"
)

// Line is one JSONL record. Only fields we actually classify on are typed;
// the rest stays as RawMessage to survive schema drift.
type Line struct {
	Type        string    `json:"type"`
	UUID        string    `json:"uuid"`
	ParentUUID  *string   `json:"parentUuid"`
	Timestamp   time.Time `json:"timestamp"`
	SessionID   string    `json:"sessionId"`
	Version     string    `json:"version"`
	CWD         string    `json:"cwd"`
	IsSidechain bool      `json:"isSidechain"`

	Message *MessageBody `json:"message"`

	// ai-title records (Claude auto-generated session label)
	AiTitle string `json:"aiTitle,omitempty"`

	// custom-title records (user-set, e.g. via `/branch <name>`)
	CustomTitle string `json:"customTitle,omitempty"`
}

type MessageBody struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	ID         string          `json:"id"`
}

type Block struct {
	Type string `json:"type"` // text | thinking | tool_use | tool_result

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	ContentRM json.RawMessage `json:"content,omitempty"`

	Text string `json:"text,omitempty"`
}

// DecodeContent handles the polymorphic message.content field (string or array).
// Returns nil for absent/empty content.
func DecodeContent(raw json.RawMessage) []Block {
	if len(raw) == 0 {
		return nil
	}
	var blocks []Block
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []Block{{Type: "text", Text: s}}
	}
	return nil
}
