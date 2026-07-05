package main

import (
	"strings"
	"testing"
	"time"
)

func TestLimitBadgeActive(t *testing.T) {
	now := time.Now()
	reset := now.Add(35 * time.Minute)

	text, newStatus := limitBadge(reset, "done", now)
	if !strings.HasPrefix(text, " ⏳ ") {
		t.Errorf("text: got %q, want countdown with hourglass", text)
	}
	if newStatus != "limited" {
		t.Errorf("newStatus: got %q, want %q (suppress the stale glyph)", newStatus, "limited")
	}

	// Already parked on limited: no redundant option write.
	_, newStatus = limitBadge(reset, "limited", now)
	if newStatus != "" {
		t.Errorf("newStatus: got %q, want no change", newStatus)
	}
}

func TestLimitBadgeReadyToContinue(t *testing.T) {
	now := time.Now()

	// Reset passed but the session is still paused (non-zero reset): show the
	// continue marker and keep the glyph parked on "limited".
	text, newStatus := limitBadge(now.Add(-time.Minute), "done", now)
	if text != " ▶" || newStatus != "limited" {
		t.Errorf("got (%q, %q), want (\" ▶\", \"limited\")", text, newStatus)
	}

	// Already parked: no redundant write.
	_, newStatus = limitBadge(now.Add(-time.Minute), "limited", now)
	if newStatus != "" {
		t.Errorf("newStatus: got %q, want no change", newStatus)
	}
}

func TestLimitBadgeResumed(t *testing.T) {
	now := time.Now()

	// Zero reset = session resumed (a newer message landed): hand the glyph
	// back to the hook states.
	text, newStatus := limitBadge(time.Time{}, "limited", now)
	if text != "" || newStatus != "done" {
		t.Errorf("got (%q, %q), want (\"\", \"done\")", text, newStatus)
	}

	// No limit and glyph owned by the hooks: fully inert.
	text, newStatus = limitBadge(time.Time{}, "working", now)
	if text != "" || newStatus != "" {
		t.Errorf("got (%q, %q), want no output and no change", text, newStatus)
	}
}

func TestWindowLimitResetFiltersByWindow(t *testing.T) {
	// No tmux call may happen when no rec has a pane.
	old := tmuxRunner
	tmuxRunner = func(args ...string) (string, error) {
		t.Errorf("unexpected tmux call: %v", args)
		return "", nil
	}
	defer func() { tmuxRunner = old }()

	got := windowLimitReset("@1", map[string]sessionRec{
		"s1": {SessionID: "s1"}, // no pane — never resolved, must not hit tmux
	})
	if !got.IsZero() {
		t.Errorf("got %v, want zero", got)
	}
}

func TestTmuxBlockIncludesLimitBadge(t *testing.T) {
	block := tmuxBlock("/usr/local/bin/cmanager")
	want := "#(/usr/local/bin/cmanager limit #{window_id})"
	if !strings.Contains(block, want) {
		t.Errorf("tmux block missing limit badge %q:\n%s", want, block)
	}
}
