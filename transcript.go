package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// scanTranscript reads the tail of a session's transcript and returns a
// one-line summary of the most recent real user prompt (what the session is
// working on) and, when the session's last message is a usage-limit error,
// the time the limit resets (zero otherwise). It reads only the tail of the
// JSONL log, so it's cheap to call for every session during the background
// refresh.
func scanTranscript(sessionID string) (prompt string, limitReset time.Time) {
	path := transcriptPath(sessionID)
	if path == "" {
		return "", time.Time{}
	}
	return scanTail(readTail(path, 256*1024), time.Now())
}

// scanTail walks transcript lines from the end. The usage-limit check only
// looks at the newest message entry: once anything else follows the limit
// error, the session has moved on and the countdown must clear.
func scanTail(data []byte, now time.Time) (prompt string, limitReset time.Time) {
	lines := strings.Split(string(data), "\n")
	newest := true
	for i := len(lines) - 1; i >= 0; i-- {
		e, ok := parseEntry(lines[i])
		if !ok {
			continue
		}
		if newest {
			newest = false
			if e.Error != "" {
				limitReset = parseLimitReset(contentText(e.Message.Content), entryTime(e, now))
			}
		}
		if text := userText(e); text != "" {
			prompt = oneLine(text)
			break
		}
	}
	return prompt, limitReset
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
	Type      string `json:"type"`
	Error     string `json:"error"` // e.g. "rate_limit" on API-error entries
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// parseEntry parses one transcript line, keeping only real conversation
// messages (user/assistant) — summaries, system lines etc. are skipped.
func parseEntry(line string) (transcriptEntry, bool) {
	var e transcriptEntry
	line = strings.TrimSpace(line)
	if line == "" || json.Unmarshal([]byte(line), &e) != nil {
		return e, false
	}
	role := e.Message.Role
	if e.Type != "user" && e.Type != "assistant" && role != "user" && role != "assistant" {
		return e, false
	}
	return e, true
}

// userText returns the prompt text of an entry if it is a genuine user
// message (not a tool result), else "".
func userText(e transcriptEntry) string {
	if e.Type != "user" && e.Message.Role != "user" {
		return ""
	}
	return contentText(e.Message.Content)
}

// contentText extracts the plain text of a message content, which is either a
// bare string or an array of typed blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
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

func entryTime(e transcriptEntry, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
		return t
	}
	return fallback
}

// Usage-limit errors land in the transcript as a synthetic assistant message,
// e.g. "You've hit your session limit · resets 10:50pm (Europe/Berlin)".
// Older builds used "Claude AI usage limit reached|<epoch-seconds>".
var (
	resetClockRe = regexp.MustCompile(`(?i)limit.*resets?(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)
	resetEpochRe = regexp.MustCompile(`(?i)limit reached\|(\d{9,})`)
)

// parseLimitReset extracts the reset time from a usage-limit error text: the
// next occurrence of the given wall-clock time after ref (the entry's
// timestamp). Returns zero when the text isn't a usage-limit message — e.g.
// the transient "Server is temporarily limiting requests" rate limit.
func parseLimitReset(text string, ref time.Time) time.Time {
	if m := resetEpochRe.FindStringSubmatch(text); m != nil {
		sec, _ := strconv.ParseInt(m[1], 10, 64)
		return time.Unix(sec, 0)
	}
	m := resetClockRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	hour, _ := strconv.Atoi(m[1])
	min := 0
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	if hour > 12 || min > 59 {
		return time.Time{}
	}
	hour %= 12
	if strings.EqualFold(m[3], "pm") {
		hour += 12
	}
	loc := time.Local
	if m[4] != "" {
		if l, err := time.LoadLocation(m[4]); err == nil {
			loc = l
		}
	}
	r := ref.In(loc)
	t := time.Date(r.Year(), r.Month(), r.Day(), hour, min, 0, 0, loc)
	if !t.After(r) {
		t = t.AddDate(0, 0, 1)
	}
	return t
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
