package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/glamour"
)

// transcriptPath locates the JSONL transcript for a session id by globbing the
// projects directory, so we don't have to re-derive the cwd->slug encoding.
func transcriptPath(sessionID string) string {
	home, _ := os.UserHomeDir()
	pattern := filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

type rawEntry struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// renderTranscript renders a session's transcript by session id.
func renderTranscript(sessionID string, tail, width int) string {
	return renderTranscriptFile(transcriptPath(sessionID), tail, width)
}

// renderTranscriptFile returns the last `tail` rendered turns of a JSONL log
// (a session transcript or a subagent log) styled and wrapped to `width`.
func renderTranscriptFile(path string, tail, width int) string {
	if path == "" {
		return dimStyle.Render("No transcript found for this session yet.")
	}
	f, err := os.Open(path)
	if err != nil {
		return dimStyle.Render(fmt.Sprintf("Could not open transcript: %v", err))
	}
	defer f.Close()

	var turns []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var e rawEntry
		if json.Unmarshal(b, &e) != nil {
			continue
		}
		if rendered := renderEntry(e, width); rendered != "" {
			turns = append(turns, rendered)
		}
	}
	if len(turns) == 0 {
		return dimStyle.Render("(transcript is empty)")
	}
	if tail > 0 && len(turns) > tail {
		turns = turns[len(turns)-tail:]
	}
	return strings.Join(turns, "\n")
}

func renderEntry(e rawEntry, width int) string {
	switch e.Message.Role {
	case "user":
		body := renderBlocks(e.Message.Content, width, false)
		if strings.TrimSpace(stripStyles(body)) == "" {
			return ""
		}
		return roleUserStyle.Render("❯ you") + "\n" + body + "\n"
	case "assistant":
		body := renderBlocks(e.Message.Content, width, true)
		if strings.TrimSpace(stripStyles(body)) == "" {
			return ""
		}
		return roleClaudeStyle.Render("● claude") + "\n" + body + "\n"
	}
	return ""
}

// renderBlocks renders a content field (bare string or block array). Assistant
// text is run through the markdown renderer (code blocks, lists, wrapping);
// user text and tool annotations are wrapped plainly.
func renderBlocks(raw json.RawMessage, width int, assistant bool) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if assistant {
			return renderMarkdown(s, width)
		}
		return indent(wrap(s, width-2))
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			if assistant {
				parts = append(parts, renderMarkdown(b.Text, width))
			} else {
				parts = append(parts, indent(wrap(b.Text, width-2)))
			}
		case "tool_use":
			arg := compactJSON(b.Input)
			line := fmt.Sprintf("→ %s(%s)", b.Name, truncate(arg, 200))
			parts = append(parts, indent(toolStyle.Render(wrap(line, width-2))))
		case "tool_result":
			res := oneLine(extractPlain(b.Content))
			if res == "" {
				continue
			}
			parts = append(parts, indent(dimStyle.Render(wrap("↳ "+truncate(res, 400), width-2))))
		}
	}
	return strings.Join(parts, "\n")
}

// extractPlain pulls plain text out of a content field for compact summaries.
func extractPlain(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// ---- markdown -----------------------------------------------------------

var (
	mdRenderer *glamour.TermRenderer
	mdWidth    int
)

// renderMarkdown renders markdown (with syntax-highlighted code blocks) wrapped
// to width, caching the renderer per width.
func renderMarkdown(s string, width int) string {
	if width < 20 {
		width = 20
	}
	if mdRenderer == nil || mdWidth != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width-2),
		)
		if err != nil {
			return indent(wrap(s, width-2))
		}
		mdRenderer = r
		mdWidth = width
	}
	out, err := mdRenderer.Render(s)
	if err != nil {
		return indent(wrap(s, width-2))
	}
	return strings.TrimRight(out, "\n")
}

// ---- text helpers -------------------------------------------------------

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// wrap word-wraps prose to width, preserving explicit newlines. ANSI-naive,
// which is fine for the plain text it is applied to.
func wrap(s string, width int) string {
	if width <= 4 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapLine(line, width))
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	cur := 0
	for _, w := range words {
		wl := len([]rune(w))
		switch {
		case cur == 0:
			b.WriteString(w)
			cur = wl
		case cur+1+wl <= width:
			b.WriteString(" " + w)
			cur += 1 + wl
		default:
			b.WriteString("\n" + w)
			cur = wl
		}
	}
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// stripStyles removes ANSI escapes; used only to test for visible content.
func stripStyles(s string) string {
	out := strings.Builder{}
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
