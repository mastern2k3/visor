#!/bin/bash
# Visor hook dispatcher. Registered in ~/.claude/settings.json for every
# hook event we care about (SessionStart, Stop, UserPromptSubmit, Notification,
# SessionEnd). Stays silent on errors — never blocks Claude.
#
# Usage (from settings.json):
#   bash ~/.local/share/visor/visor-hook.sh <event> [--matcher <name>]
#
# stdin: hook payload from Claude (JSON)
# env:   CLAUDE_PID is set to $PPID (the claude process) before exec'ing visor

VISOR_BIN="${VISOR_BIN:-visor}"

# $PPID here is the claude process (claude → bash → this script).
# stdin/stderr/stdout are inherited by exec; no redirects needed.
exec env CLAUDE_PID="$PPID" "$VISOR_BIN" hook "$@"
