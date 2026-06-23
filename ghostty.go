package main

import (
	"os/exec"
	"strings"
)

// Ghostty has no tmux-style control protocol, but ≥1.3.0 ships an AppleScript
// dictionary: every terminal surface has a stable `id`, a `working directory`,
// and a `name` (its title). We can't read a surface's pid/tty, so a Claude
// session is matched to its surface by cwd — preferring the one whose title
// starts with the ✳ that Claude Code puts there — and then focused by id.

// claudeTitleMark is the glyph Claude Code prefixes onto the terminal title; a
// surface showing it at a session's cwd is almost certainly that session.
const claudeTitleMark = "✳"

type ghosttyTerm struct {
	id   string
	cwd  string
	name string
}

// osa runs an AppleScript and returns its trimmed stdout. Broken out so the
// Ghostty helpers stay readable and tests can stub it.
var osa = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return strings.TrimSpace(string(out)), err
}

// ghosttyTerminals lists every Ghostty surface, or nil if Ghostty isn't running
// (the `is running` guard means we never launch it just to enumerate). Fields
// are tab-separated since a cwd or title can contain spaces and commas.
func ghosttyTerminals() []ghosttyTerm {
	// TB/LF are bound outside the tell block on purpose: inside it, `tab` resolves
	// to Ghostty's tab *class*, not the tab character — so we'd get literal "tab".
	const script = `if application "Ghostty" is running then
	set TB to tab
	set LF to linefeed
	tell application "Ghostty"
		set out to ""
		repeat with t in terminals
			set out to out & (id of t) & TB & (working directory of t) & TB & (name of t) & LF
		end repeat
		return out
	end tell
end if`
	out, err := osa(script)
	if err != nil || out == "" {
		return nil
	}
	var terms []ghosttyTerm
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || f[0] == "" {
			continue
		}
		terms = append(terms, ghosttyTerm{id: f[0], cwd: f[1], name: f[2]})
	}
	return terms
}

// resolveGhostty returns the id of the Ghostty surface to jump to for a session
// at cwd: a ✳-titled (live Claude) surface there wins; otherwise the first
// surface at that cwd. "" means no Ghostty surface matches.
func resolveGhostty(cwd string, terms []ghosttyTerm) string {
	if cwd == "" {
		return ""
	}
	var fallback string
	for _, t := range terms {
		if t.cwd != cwd {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(t.name), claudeTitleMark) {
			return t.id
		}
		if fallback == "" {
			fallback = t.id
		}
	}
	return fallback
}

// resolveGhosttyTargets fills ghosttyID on session roots that have no tmux pane,
// so they're reachable (and shown jumpable) when they live in a Ghostty surface.
func resolveGhosttyTargets(roots []*treeNode) {
	var terms []ghosttyTerm
	enumerated := false
	for _, n := range roots {
		if n.kind != kindSession || n.pane != "" {
			continue
		}
		if !enumerated { // only pay the osascript cost if a non-tmux session exists
			terms = ghosttyTerminals()
			enumerated = true
		}
		n.ghosttyID = resolveGhostty(n.cwd, terms)
	}
}

// focusGhostty brings the Ghostty surface with the given id to the front. The id
// comes from our own enumeration (a UUID), so it's safe to interpolate.
func focusGhostty(id string) error {
	script := `if application "Ghostty" is running then
	tell application "Ghostty" to focus (first terminal whose id is "` + id + `")
end if`
	_, err := osa(script)
	return err
}
