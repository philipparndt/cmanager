package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// helpRec is one line written by the Notification hook when a session needs
// the user's attention (a permission prompt or an idle-input wait).
type helpRec struct {
	SessionID string  `json:"sessionId"`
	Message   string  `json:"message"`
	Cwd       string  `json:"cwd"`
	Ts        float64 `json:"ts"` // epoch seconds (float)
}

// helpFilePath is where the hook appends records.
func helpFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "cmanager", "needs-help.jsonl")
}

// readNewHelpLines reads records appended after the given byte offset and
// returns them along with the new offset. If the file shrank (truncated or
// rotated) it restarts from the beginning.
func readNewHelpLines(offset int64) ([]helpRec, int64) {
	path := helpFilePath()
	f, err := os.Open(path)
	if err != nil {
		return nil, offset // file may not exist yet; that's fine
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, offset
	}
	if fi.Size() < offset {
		offset = 0 // file was truncated/rotated
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset
	}

	var recs []helpRec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r helpRec
		if json.Unmarshal(line, &r) == nil && r.SessionID != "" {
			recs = append(recs, r)
		}
	}
	return recs, fi.Size()
}
