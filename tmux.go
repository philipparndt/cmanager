package main

import (
	"fmt"
	"os"
	"os/exec"
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

// pidToPane finds the tmux pane whose shell is an ancestor of pid — a fallback
// for sessions started before the hook recorded a pane. Returns "" if not found.
func pidToPane(pid int) string {
	if !inTmux() || pid <= 0 {
		return ""
	}
	out, err := tmux("list-panes", "-a", "-F", "#{pane_pid} #{pane_id}")
	if err != nil {
		return ""
	}
	panePids := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		var pp int
		var id string
		if _, e := fmt.Sscanf(line, "%d %s", &pp, &id); e == nil {
			panePids[pp] = id
		}
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

func ppid(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return p
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
