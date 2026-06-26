package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// All tmux interaction goes through tmuxRunner so tests can capture the exact
// command lines without spawning tmux.
var tmuxRunner = func(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func tmux(args ...string) (string, error) { return tmuxRunner(args...) }

// inTmux reports whether we're running inside a tmux server (so tmux commands
// will do something). Outside tmux every helper below is a no-op.
func inTmux() bool { return os.Getenv("TMUX") != "" }

// displayMessage flashes a transient message on the attached client's status
// line — the completion "toast".
func displayMessage(text string) {
	if !inTmux() {
		return
	}
	_, _ = tmux("display-message", text)
}

// setAttention marks (or clears) the window containing pane as needing
// attention, via a window-level user option the user's status-line format can
// render (see README for the .tmux.conf snippet).
func setAttention(pane string, on bool) {
	if !inTmux() || pane == "" {
		return
	}
	if on {
		_, _ = tmux("set-option", "-t", pane, "-w", "@ai_status", "needs")
	} else {
		_, _ = tmux("set-option", "-t", pane, "-wu", "@ai_status")
	}
}

// paneIsActive reports whether pane is the one currently in view (its pane and
// window are both active), so notifications for it can be suppressed.
func paneIsActive(pane string) bool {
	if !inTmux() || pane == "" {
		return false
	}
	out, err := tmux("display-message", "-pt", pane, "#{pane_active}#{window_active}")
	return err == nil && out == "11"
}

// panePidMap maps each tmux pane's shell pid to its pane id. Outside tmux (or on
// error) it returns nil, so lookups against it simply miss.
func panePidMap() map[int]string {
	if !inTmux() {
		return nil
	}
	out, err := tmux("list-panes", "-a", "-F", "#{pane_pid} #{pane_id}")
	if err != nil {
		return nil
	}
	panePids := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		var pp int
		var id string
		if _, e := fmt.Sscanf(line, "%d %s", &pp, &id); e == nil {
			panePids[pp] = id
		}
	}
	return panePids
}

// pidToPane finds the tmux pane whose shell is an ancestor of pid — a fallback
// for sessions started before the hook recorded a pane. Returns "" if not found.
func pidToPane(pid int) string {
	return pidToPaneWith(pid, panePidMap())
}

// pidToPaneWith is pidToPane against an already-built pane map, so resolving many
// sessions at once needs only one `tmux list-panes`.
func pidToPaneWith(pid int, panePids map[int]string) string {
	if pid <= 0 || len(panePids) == 0 {
		return ""
	}
	for cur, i := pid, 0; cur > 1 && i < 20; i++ {
		if id, ok := panePids[cur]; ok {
			return id
		}
		parent := ppid(cur)
		if parent == cur || parent <= 0 {
			break
		}
		cur = parent
	}
	return ""
}

// winInfo identifies the tmux window a pane lives in: key is a stable grouping
// id (session_name:window_index), label is the header text shown to the user.
type winInfo struct {
	key   string
	label string
}

// paneWindowMap maps each tmux pane id to its window. Outside tmux (or on error)
// it returns nil, so lookups simply miss and sessions fall back to no window.
func paneWindowMap() map[string]winInfo {
	if !inTmux() {
		return nil
	}
	out, err := tmux("list-panes", "-a", "-F", "#{pane_id}\t#{session_name}:#{window_index}\t#{window_name}")
	if err != nil {
		return nil
	}
	m := map[string]winInfo{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		key, name := parts[1], parts[2]
		label := key
		if name != "" {
			label = key + " " + name
		}
		m[parts[0]] = winInfo{key: key, label: label}
	}
	return m
}

func ppid(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return p
}

// procInfo returns a process's parent pid and short command name in one ps call.
// The name is basenamed (GUI apps report a full path) but kept whole otherwise,
// so e.g. "Code Helper (Plugin)" survives intact.
func procInfo(pid int) (parent int, name string) {
	if pid <= 0 {
		return 0, ""
	}
	out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, ""
	}
	s := strings.TrimSpace(string(out))
	i := strings.IndexByte(s, ' ')
	if i < 0 {
		p, _ := strconv.Atoi(s)
		return p, ""
	}
	p, _ := strconv.Atoi(s[:i])
	return p, filepath.Base(strings.TrimSpace(s[i+1:]))
}

// ownerProcess walks up from pid to the top-most ancestor under launchd — the
// app hosting the session (a terminal like iTerm2/Ghostty/Code Helper, or the
// process itself if detached). Used to label sessions we can't reach via tmux
// or Ghostty. Returns "" if it can't be determined.
func ownerProcess(pid int) string {
	for cur, i := pid, 0; cur > 1 && i < 30; i++ {
		parent, name := procInfo(cur)
		if parent <= 1 {
			return name
		}
		if parent == cur {
			break
		}
		cur = parent
	}
	return ""
}

// resolveOwners fills in the hosting process for sessions reachable via neither
// tmux nor Ghostty, so the picker can at least say where they're running.
func resolveOwners(roots []*treeNode) {
	for _, n := range roots {
		if n.kind == kindSession && n.pane == "" && n.ghosttyID == "" {
			n.owner = ownerProcess(n.pid)
		}
	}
}

// resolvePanes fills in each session root's tmux pane — the hook-recorded pane,
// else the pid→pane ancestry fallback. A root left with pane "" isn't reachable
// via tmux (started outside it), so the picker shows it separately as unjumpable.
func resolvePanes(roots []*treeNode, recs map[string]sessionRec) {
	pm := panePidMap()
	wm := paneWindowMap()
	for _, n := range roots {
		if n.kind != kindSession {
			continue
		}
		pane := recs[n.sessionID].Pane
		if pane == "" {
			pane = pidToPaneWith(n.pid, pm)
		}
		n.pane = pane
		if wi, ok := wm[pane]; ok {
			n.winKey = wi.key
			n.winLabel = wi.label
		}
	}
}

// jumpToPane navigates the attached client to pane: switch to its session, then
// select its window and pane.
func jumpToPane(pane string) error {
	if pane == "" {
		return nil
	}
	target, err := tmux("display-message", "-pt", pane, "#{session_name}:#{window_index}.#{pane_index}")
	if err != nil {
		return err
	}
	sess := target
	if i := strings.IndexByte(target, ':'); i >= 0 {
		sess = target[:i]
	}
	_, _ = tmux("switch-client", "-t", sess)
	_, _ = tmux("select-window", "-t", target)
	_, err = tmux("select-pane", "-t", pane)
	return err
}
