package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed install_hook.sh
var hookScript []byte

const settingsSnippet = `{
  "hooks": {
    "SessionStart":     [{"hooks": [{"type": "command", "command": "bash %s SessionStart"}]}],
    "SessionEnd":       [{"hooks": [{"type": "command", "command": "bash %s SessionEnd"}]}],
    "Stop":             [{"hooks": [{"type": "command", "command": "bash %s Stop"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "bash %s UserPromptSubmit"}]}],
    "Notification": [
      {"matcher": "permission_prompt", "hooks": [{"type": "command", "command": "bash %s Notification --matcher permission_prompt"}]},
      {"matcher": "idle_prompt",       "hooks": [{"type": "command", "command": "bash %s Notification --matcher idle_prompt"}]}
    ]
  }
}
`

// runInstall writes the hook wrapper to ~/.local/share/visor/visor-hook.sh
// and prints the snippet the user should merge into ~/.claude/settings.json.
func runInstall(args []string) {
	_ = args
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		os.Exit(1)
	}
	dir := filepath.Join(home, ".local", "share", "visor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		os.Exit(1)
	}
	dst := filepath.Join(dir, "visor-hook.sh")
	if err := os.WriteFile(dst, hookScript, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n\n", dst)
	fmt.Printf("Merge this into ~/.claude/settings.json (under top-level \"hooks\"):\n\n")
	fmt.Printf(settingsSnippet, dst, dst, dst, dst, dst, dst)
}
