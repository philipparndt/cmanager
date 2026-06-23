package main

import (
	"encoding/json"
	"os/exec"
)

// sessionInfo mirrors one entry of `claude agents --json --all`.
type sessionInfo struct {
	PID       int    `json:"pid"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	StartedAt int64  `json:"startedAt"` // epoch milliseconds
	SessionID string `json:"sessionId"`
	Status    string `json:"status"` // "busy" | "idle"
}

// pollSessions shells out to the claude CLI and returns every known session,
// including completed ones. Returns an error string suitable for display.
func pollSessions() ([]sessionInfo, error) {
	cmd := exec.Command("claude", "agents", "--json", "--all")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var sessions []sessionInfo
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}
