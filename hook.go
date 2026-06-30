package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookEvent is the subset of the Claude Code hook payload (delivered as JSON on
// stdin) that we act on. Common fields are present on every event.
type hookEvent struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	Message        string `json:"message"`          // Notification text
	StopHookActive bool   `json:"stop_hook_active"` // Stop: true = intermediate retry
}

// runHook is the entry point for `cmanager hook`, invoked by Claude Code on the
// Notification, Stop, SessionStart and SessionEnd events.
func runHook() {
	data, _ := io.ReadAll(os.Stdin)
	var e hookEvent
	if json.Unmarshal(data, &e) != nil || e.SessionID == "" {
		return // nothing actionable
	}
	handleHook(e, os.Getenv("TMUX_PANE"), time.Now().UnixMilli())
}

// handleHook updates the registry and drives tmux notifications for one event.
// Split out from runHook (with pane/now injected) so it is unit-testable.
func handleHook(e hookEvent, pane string, nowMs int64) {
	if e.HookEventName == "SessionEnd" {
		// Clear the window glyph — exiting Claude with a prompt pending fires
		// SessionEnd but no Stop, so the badge would otherwise stick.
		p := pane
		if p == "" {
			p = mustPane(e.SessionID)
		}
		setState(p, "")
		removeSessionRec(e.SessionID)
		return
	}

	logf("hook: %s session=%s pane=%q cwd=%s", e.HookEventName, e.SessionID, pane, e.Cwd)

	rec, _ := loadSessionRec(e.SessionID)
	rec.SessionID = e.SessionID
	if pane != "" { // keep a previously-recorded pane if this event lacks one
		rec.Pane = pane
	}
	if e.Cwd != "" {
		rec.Cwd = e.Cwd
	}
	rec.Ts = nowMs

	switch e.HookEventName {
	case "Notification":
		// Claude fires Notification both for blocking permission prompts and for
		// the idle "waiting for your input" nudge (~60s after a turn ends). Only
		// the former actually needs the user; the idle nudge reads as finished.
		if isIdleNudge(e.Message) {
			rec.Needs = false
			rec.Message = ""
			_ = saveSessionRec(rec)
			setState(rec.Pane, "done")
			return
		}
		rec.Needs = true
		rec.Message = e.Message
		_ = saveSessionRec(rec)
		setState(rec.Pane, "needs") // ⚠ — cleared the moment work resumes (below)
		if !paneIsActive(rec.Pane) {
			displayMessage(notifyText("⚠", rec.Cwd, "needs your input"))
		}

	case "UserPromptSubmit", "PostToolUse":
		// Claude is actively working — a new turn started, or a tool just ran
		// (e.g. right after you answered a permission prompt). This is what
		// clears a lingering ⚠ promptly, instead of waiting for the turn to end.
		rec.Needs = false
		rec.Message = ""
		_ = saveSessionRec(rec)
		setState(rec.Pane, "working")

	case "Stop":
		if e.StopHookActive {
			// Intermediate block-cap retry, not a finished turn — still working.
			_ = saveSessionRec(rec)
			setState(rec.Pane, "working")
			return
		}
		rec.Needs = false
		rec.Message = ""
		_ = saveSessionRec(rec)
		setState(rec.Pane, "done")
		if !paneIsActive(rec.Pane) {
			displayMessage(notifyText("✓", rec.Cwd, "finished"))
		}

	default: // SessionStart (and any other): just record the pane mapping
		_ = saveSessionRec(rec)
	}
}

// mustPane falls back to a session's recorded pane when the event lacks $TMUX_PANE.
func mustPane(sessionID string) string {
	r, _ := loadSessionRec(sessionID)
	return r.Pane
}

// isIdleNudge reports whether a Notification is the non-blocking idle reminder
// ("Claude is waiting for your input") rather than a permission/action prompt.
func isIdleNudge(message string) bool {
	return strings.Contains(strings.ToLower(message), "waiting for your input")
}

// notifyText builds a short status-line message like "✓ cmanager finished".
func notifyText(icon, cwd, what string) string {
	label := "claude"
	if cwd != "" {
		label = filepath.Base(cwd)
	}
	return icon + " " + label + " " + what
}
