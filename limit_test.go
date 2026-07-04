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

func TestLimitBadgeOver(t *testing.T) {
	now := time.Now()

	// Limit passed while we were showing the countdown: hand back to "done".
	text, newStatus := limitBadge(now.Add(-time.Minute), "limited", now)
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
