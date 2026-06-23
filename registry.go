package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The registry maps each Claude session to the tmux pane it lives in and tracks
// whether it currently needs attention. It is written by `cmanager hook` (which
// learns the pane from $TMUX_PANE) and read by the picker. One small JSON file
// per session under ~/.claude/cmanager/sessions/ keeps writers contention-free.

// sessionRec is one session's registry entry.
type sessionRec struct {
	SessionID string `json:"sessionId"`
	Pane      string `json:"pane"`    // tmux pane id, e.g. "%3"
	Cwd       string `json:"cwd"`     // session working directory
	Needs     bool   `json:"needs"`   // last event was a Notification, still unanswered
	Message   string `json:"message"` // notification text, when Needs
	Ts        int64  `json:"ts"`      // epoch ms of last update
}

// baseDirOverride lets tests point the registry at a temp dir.
var baseDirOverride string

func baseDir() string {
	if baseDirOverride != "" {
		return baseDirOverride
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "cmanager")
}

func sessionsDir() string { return filepath.Join(baseDir(), "sessions") }

func sessionRecPath(id string) string {
	return filepath.Join(sessionsDir(), id+".json")
}

// saveSessionRec atomically persists one record.
func saveSessionRec(r sessionRec) error {
	if err := os.MkdirAll(sessionsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := sessionRecPath(r.SessionID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, sessionRecPath(r.SessionID))
}

// loadSessionRec reads one record by session id; ok is false if absent.
func loadSessionRec(id string) (sessionRec, bool) {
	var r sessionRec
	b, err := os.ReadFile(sessionRecPath(id))
	if err != nil {
		return r, false
	}
	if json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}

// listSessionRecs returns every stored record, keyed by session id.
func listSessionRecs() map[string]sessionRec {
	out := map[string]sessionRec{}
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		if r, ok := loadSessionRec(id); ok {
			out[id] = r
		}
	}
	return out
}

// needsAttention reports whether a session is actually blocked on the user.
// It re-checks the message at read time so a stale idle-nudge record (e.g.
// written by an older build, or never followed by a Stop) doesn't show as
// waiting.
func (r sessionRec) needsAttention() bool {
	return r.Needs && !isIdleNudge(r.Message)
}

// removeSessionRec drops a session's record (on SessionEnd).
func removeSessionRec(id string) { os.Remove(sessionRecPath(id)) }
