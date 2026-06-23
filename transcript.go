package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// latestPrompt returns a one-line summary of the most recent real user prompt
// in a session's transcript — i.e. what the session is working on. It reads only
// the tail of the JSONL log, so it's cheap to call for every session during the
// background refresh.
func latestPrompt(sessionID string) string {
	path := transcriptPath(sessionID)
	if path == "" {
		return ""
	}
	data := readTail(path, 256*1024)
	if len(data) == 0 {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text := userText(lines[i]); text != "" {
			return oneLine(text)
		}
	}
	return ""
}

// transcriptPath finds a session's transcript JSONL under ~/.claude/projects.
func transcriptPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// userText returns the prompt text of a transcript line if it is a genuine user
// message (not a tool result), else "".
func userText(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	var e transcriptEntry
	if json.Unmarshal([]byte(line), &e) != nil {
		return ""
	}
	if e.Type != "user" && e.Message.Role != "user" {
		return ""
	}
	// content is either a bare string or an array of typed blocks.
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// readTail returns up to max trailing bytes of a file, starting at a line
// boundary when truncated.
func readTail(path string, max int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if fi.Size() > max {
		start = fi.Size() - max
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil
	}
	buf := make([]byte, fi.Size()-start)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:] // drop the partial first line
		}
	}
	return buf
}
