package main

import (
	"fmt"
	"strings"
	"time"
)

// `cmanager limit <window_id>` is invoked by tmux itself: the setup block puts
// #(cmanager limit #{window_id}) into the window-status formats, and tmux
// re-runs that command every status-interval. It prints a usage-limit countdown
// (" ⏳ 35m") when a session in the window is blocked on its usage limit, else
// nothing — this is what keeps the tab badge ticking without a resident process.
func runLimitBadge(win string) {
	if win == "" {
		return
	}
	cur, _ := tmux("show-option", "-wqv", "-t", win, "@ai_status")
	text, newStatus := limitBadge(windowLimitReset(win, listSessionRecs()), cur, time.Now())
	if newStatus != "" {
		_, _ = tmux("set-option", "-t", win, "-w", "@ai_status", newStatus)
	}
	fmt.Print(text)
}

// limitBadge decides the badge text and the @ai_status transition ("" = leave
// as is). While the countdown shows, @ai_status is parked on "limited" — a
// state the glyph conditional renders as nothing — so a stale ✓/… doesn't sit
// next to it; any hook event (work resuming) simply overwrites it.
func limitBadge(reset time.Time, cur string, now time.Time) (text, newStatus string) {
	if !reset.After(now) {
		if cur == "limited" {
			return "", "done" // limit over — hand the glyph back to the hook states
		}
		return "", ""
	}
	if cur != "limited" {
		newStatus = "limited"
	}
	return " ⏳ " + countdown(reset), newStatus
}

// windowLimitReset returns the furthest usage-limit reset among the sessions
// whose pane sits in the given tmux window (zero when none is limited).
func windowLimitReset(win string, recs map[string]sessionRec) time.Time {
	var best time.Time
	var paneWin map[string]string
	for _, rec := range recs {
		if rec.Pane == "" {
			continue
		}
		if paneWin == nil {
			paneWin = paneWindowIDMap() // lazy: one tmux call, only when recs exist
		}
		if paneWin[rec.Pane] != win {
			continue
		}
		if _, reset := scanTranscript(rec.SessionID); reset.After(best) {
			best = reset
		}
	}
	return best
}

// paneWindowIDMap maps each tmux pane id to its window id.
func paneWindowIDMap() map[string]string {
	out, err := tmux("list-panes", "-a", "-F", "#{pane_id} #{window_id}")
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}
