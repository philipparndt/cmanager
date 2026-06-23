#!/usr/bin/env bash
# cmanager Notification hook.
# Claude Code runs this on the `Notification` event (a session needs permission
# or has been waiting on input). It appends one JSON record per event to
# ~/.claude/cmanager/needs-help.jsonl, which the cmanager TUI tails.
#
# Wire it up in ~/.claude/settings.json under hooks.Notification (see README).

set -euo pipefail

dir="$HOME/.claude/cmanager"
mkdir -p "$dir"

input="$(cat)"

# Extract session_id, message and cwd from the hook payload; stamp with epoch
# seconds. Falls back gracefully if jq is missing.
if command -v jq >/dev/null 2>&1; then
  printf '%s' "$input" | jq -c \
    '{sessionId: (.session_id // ""), message: (.message // ""), cwd: (.cwd // ""), ts: now}' \
    >> "$dir/needs-help.jsonl"
else
  printf '%s\n' "$input" >> "$dir/needs-help.jsonl"
fi

exit 0
