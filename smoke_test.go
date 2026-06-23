package main

import (
	"strings"
	"testing"
)

func TestPollSessions(t *testing.T) {
	sessions, err := pollSessions()
	if err != nil {
		t.Fatalf("pollSessions: %v", err)
	}
	t.Logf("found %d sessions", len(sessions))
	for _, s := range sessions {
		t.Logf("  %s  %-6s  %s", truncate(s.SessionID, 8), s.Status, prettyCwd(s.Cwd))
	}
}

func TestRenderTranscript(t *testing.T) {
	sessions, err := pollSessions()
	if err != nil || len(sessions) == 0 {
		t.Skip("no sessions to render")
	}
	out := renderTranscript(sessions[0].SessionID, 5, 80)
	if strings.TrimSpace(stripStyles(out)) == "" {
		t.Errorf("empty transcript render for %s", sessions[0].SessionID)
	}
	t.Logf("transcript head:\n%s", truncate(stripStyles(out), 400))
}
