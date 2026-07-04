package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tmuxMarkerStart = "# >>> cmanager >>>"
	tmuxMarkerEnd   = "# <<< cmanager <<<"
)

// hookEvents are the Claude Code events we point at `cmanager hook`.
// UserPromptSubmit/PostToolUse drive the "working" state — they fire as Claude
// resumes, so a pending ⚠ clears immediately instead of lingering until Stop.
var hookEvents = []string{
	"Notification", "UserPromptSubmit", "PostToolUse", "Stop", "SessionStart", "SessionEnd",
}

// runSetup wires the Claude Code hooks (settings.json) and the tmux keybinding
// (~/.tmux.conf) to this binary, after showing a preview and asking to apply.
// Both files are backed up before being written.
func runSetup() {
	exe := resolveExe()

	settingsP := filepath.Join(homeDir(), ".claude", "settings.json")
	settingsOrig, _ := os.ReadFile(settingsP)
	settingsNew, settingsChanged := wireSettings(settingsOrig, exe)

	tmuxP := filepath.Join(homeDir(), ".tmux.conf")
	tmuxOrig, _ := os.ReadFile(tmuxP)
	tmuxNew, tmuxChanged := wireTmux(string(tmuxOrig), exe)

	if !settingsChanged && !tmuxChanged {
		fmt.Println("✓ Already set up — nothing to change.")
		return
	}

	fmt.Printf("cmanager setup will use this binary:\n  %s\n\n", exe)
	if settingsChanged {
		fmt.Printf("• %s\n  wire %s → %q\n\n", settingsP, strings.Join(hookEvents, ", "), exe+" hook")
	}
	if tmuxChanged {
		fmt.Printf("• %s\n  add this block:\n\n%s\n\n", tmuxP, indentBlock(tmuxBlock(exe)))
	}
	fmt.Println("Existing files are backed up first (.bak-<timestamp>).")

	if !confirm("Apply these changes? [y/N] ") {
		fmt.Println("Aborted — no changes made.")
		return
	}

	if settingsChanged {
		if err := backupAndWrite(settingsP, settingsNew); err != nil {
			fmt.Fprintln(os.Stderr, "settings.json:", err)
		} else {
			fmt.Println("✓ wrote", settingsP)
		}
	}
	if tmuxChanged {
		if err := backupAndWrite(tmuxP, []byte(tmuxNew)); err != nil {
			fmt.Fprintln(os.Stderr, ".tmux.conf:", err)
		} else {
			fmt.Println("✓ wrote", tmuxP)
		}
	}

	fmt.Println("\nNext:")
	fmt.Println("  • reload tmux:   tmux source-file ~/.tmux.conf")
	fmt.Println("  • restart Claude sessions so the new hooks attach")
	fmt.Println("  • open the picker: prefix + a")
}

// wireSettings returns the updated settings.json with the four hook events
// pointing at `<exe> hook`, replacing any prior cmanager hook entries.
func wireSettings(orig []byte, exe string) (out []byte, changed bool) {
	root := map[string]any{}
	if len(bytes.TrimSpace(orig)) > 0 {
		_ = json.Unmarshal(orig, &root)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	cmd := exe + " hook"
	group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}

	for _, ev := range hookEvents {
		arr, _ := hooks[ev].([]any)
		var kept []any
		for _, g := range arr {
			if !groupRefersCmanager(g) { // drop our own / stale cmanager entries
				kept = append(kept, g)
			}
		}
		kept = append(kept, group)
		hooks[ev] = kept
	}
	root["hooks"] = hooks

	out, _ = json.MarshalIndent(root, "", "  ")
	out = append(out, '\n')

	// Compare against the normalized original to detect a real change.
	var origRoot map[string]any
	if len(bytes.TrimSpace(orig)) > 0 {
		_ = json.Unmarshal(orig, &origRoot)
	}
	origNorm, _ := json.MarshalIndent(origRoot, "", "  ")
	origNorm = append(origNorm, '\n')
	return out, !bytes.Equal(origNorm, out)
}

// groupRefersCmanager reports whether a hook matcher-group runs a cmanager command.
func groupRefersCmanager(g any) bool {
	gm, ok := g.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := gm["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if c, _ := hm["command"].(string); strings.Contains(c, "cmanager") {
			return true
		}
	}
	return false
}

// wireTmux returns ~/.tmux.conf content with the cmanager marker block added or
// updated in place.
func wireTmux(orig, exe string) (out string, changed bool) {
	block := tmuxBlock(exe)
	start := strings.Index(orig, tmuxMarkerStart)
	if start >= 0 {
		end := strings.Index(orig, tmuxMarkerEnd)
		if end > start {
			end += len(tmuxMarkerEnd)
			updated := orig[:start] + block + orig[end:]
			return updated, updated != orig
		}
	}
	sep := ""
	if len(orig) > 0 && !strings.HasSuffix(orig, "\n") {
		sep = "\n"
	}
	if len(orig) > 0 {
		sep += "\n"
	}
	return orig + sep + block + "\n", true
}

// aiStatusGlyph renders the @ai_status window option (set by `cmanager hook`)
// as a trailing per-window glyph:  working → …   needs input → ⚠   done → ✓
// (unset → nothing).
const aiStatusGlyph = "#{?#{==:#{@ai_status},needs}, ⚠," +
	"#{?#{==:#{@ai_status},working}, …," +
	"#{?#{==:#{@ai_status},done}, ✓,}}}"

func tmuxBlock(exe string) string {
	// tmux re-runs the #() command every status-interval, which is what keeps
	// the usage-limit countdown ticking without any resident process.
	limitBadge := fmt.Sprintf("#(%s limit #{window_id})", exe)
	return tmuxMarkerStart + "\n" +
		"# Open the cmanager session picker.\n" +
		fmt.Sprintf("bind a display-popup -E -w 80%% -h 70%% '%s pick'\n", exe) +
		"# Show each Claude session's state on its window: … working · ⚠ needs you · ✓ done,\n" +
		"# plus a usage-limit countdown (⏳ 35m), refreshed every status-interval.\n" +
		"# The current window is bracketed; edit to taste (this block wins, being last).\n" +
		"set -g window-status-format         '  #I:#W" + aiStatusGlyph + limitBadge + "  '\n" +
		"set -g window-status-current-format ' [#I:#W" + aiStatusGlyph + limitBadge + "] '\n" +
		tmuxMarkerEnd
}

// ---- helpers ----

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func resolveExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "cmanager"
	}
	// Keep a Homebrew-style stable symlink (/opt/homebrew/bin/cmanager) rather
	// than its versioned Cellar target, which breaks on every brew upgrade.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil &&
		!strings.Contains(resolved, string(filepath.Separator)+"Cellar"+string(filepath.Separator)) {
		return resolved
	}
	return exe
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(sc.Text()))
	return a == "y" || a == "yes"
}

func backupAndWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		bak := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(bak, existing, 0o644); err != nil {
			return err
		}
		fmt.Println("  backup:", bak)
	}
	return os.WriteFile(path, content, 0o644)
}

func indentBlock(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
