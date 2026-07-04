// cmanager is the AI-awareness layer for Claude Code sessions running in tmux.
//
// It does two things and lets tmux do the rest:
//   - `cmanager hook` (a Claude Code hook target) records which tmux pane each
//     session lives in and posts tmux notifications when a session needs input
//     or finishes a turn.
//   - `cmanager` / `cmanager pick` opens a popup listing every live session and
//     jumps the client to the pane of the one you pick.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Build metadata, injected via -ldflags at release time (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hook":
			runHook()
			return
		case "setup":
			runSetup()
			return
		case "limit":
			if len(os.Args) > 2 {
				runLimitBadge(os.Args[2])
			}
			return
		case "pick":
			// fall through to the picker
		case "version", "-v", "--version":
			fmt.Printf("cmanager %s (%s) built %s\n", version, commit, date)
			return
		case "-h", "--help", "help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "cmanager: unknown command %q (try: pick, hook)\n", os.Args[1])
			os.Exit(2)
		}
	}
	runPicker()
}

func usage() {
	fmt.Print(`cmanager — find, jump to, and get notified about Claude Code sessions in tmux

usage:
  cmanager            open the session picker (intended for tmux display-popup)
  cmanager pick       same as above
  cmanager setup      wire the Claude hooks + tmux keybinding (with backups)
  cmanager hook       Claude Code hook target; reads the event JSON on stdin
  cmanager limit <id> print a window's usage-limit countdown (run by tmux #())
  cmanager version    print version information
`)
}

// ---- shared helpers ------------------------------------------------------

func prettyCwd(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	parts := strings.Split(p, string(filepath.Separator))
	if len(parts) > 4 {
		return "…/" + strings.Join(parts[len(parts)-3:], "/")
	}
	return p
}

func uptime(startedAtMs int64) string {
	if startedAtMs == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(startedAtMs))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
