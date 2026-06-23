package main

import "testing"

func TestPollSessions(t *testing.T) {
	sessions, err := pollSessions()
	if err != nil {
		t.Skipf("pollSessions unavailable (claude CLI?): %v", err)
	}
	t.Logf("found %d sessions", len(sessions))
	for _, s := range sessions {
		t.Logf("  %s  %-6s  %s", truncate(s.SessionID, 8), s.Status, prettyCwd(s.Cwd))
	}
}
