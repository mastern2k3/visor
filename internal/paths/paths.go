// Package paths centralises filesystem locations.
package paths

import (
	"os"
	"path/filepath"
)

// ProjectsDir is the Claude Code transcripts root.
// Honours $CLAUDE_CONFIG_DIR (per ccusage conventions), else ~/.claude/projects.
func ProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// Socket is the daemon's Unix socket path.
// Honours $VISOR_SOCK, else $XDG_RUNTIME_DIR/visor.sock, else /tmp.
func Socket() string {
	if s := os.Getenv("VISOR_SOCK"); s != "" {
		return s
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "visor.sock")
	}
	return filepath.Join(os.TempDir(), "visor.sock")
}

// StateDir is where the daemon persists crash-recovery state.
func StateDir() string {
	if rt := os.Getenv("XDG_STATE_HOME"); rt != "" {
		return filepath.Join(rt, "visor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "visor")
}
