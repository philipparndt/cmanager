package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type nodeKind int

const (
	kindSession nodeKind = iota
	kindAgent
	kindWindow // synthetic parent grouping the sessions in one tmux window
	kindApp    // synthetic parent grouping unreachable sessions by hosting app
)

// treeNode is one row in the picker: an interactive session or a (possibly
// nested) subagent spawned by one.
type treeNode struct {
	kind      nodeKind
	label     string
	sessionID string // owning session id (set on sessions and their agents)
	agentID   string // agent id (agents only)
	agentType string
	cwd       string
	status    string // sessions: "busy" | "idle"
	task      string // sessions: latest user prompt (what it's working on)
	startedAt int64  // epoch ms
	lastMod   time.Time
	logPath   string // agent log (agents only), for the running heuristic
	depth     int
	prefix    string // tree connector prefix, set by flattenTree
	children  []*treeNode

	pid       int    // claude process id (sessions only), for pid→pane fallback
	pane      string // resolved tmux pane (sessions only); "" means not in tmux
	winKey    string // tmux window grouping key (session_name:window_index), tmux sessions only
	winLabel  string // tmux window header label, tmux sessions only
	ghosttyID string // resolved Ghostty surface id (sessions only), when not in tmux
	owner     string // hosting process name (sessions only), when not in tmux/Ghostty
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

// agentActiveWindow: an agent whose log was touched within this window is
// treated as still running (there is no explicit status on disk).
const agentActiveWindow = 10 * time.Second

// buildTree turns the flat session list into roots. When includeAgents is true
// it also discovers and nests each session's subagents (a filesystem glob); the
// picker skips that for its instant first paint from cache and fills it in on
// the background refresh.
func buildTree(sessions []sessionInfo, includeAgents bool) []*treeNode {
	roots := make([]*treeNode, 0, len(sessions))
	for _, s := range sessions {
		n := &treeNode{
			kind:      kindSession,
			label:     prettyCwd(s.Cwd),
			sessionID: s.SessionID,
			cwd:       s.Cwd,
			status:    s.Status,
			startedAt: s.StartedAt,
			pid:       s.PID,
		}
		if includeAgents {
			n.task = latestPrompt(s.SessionID)
			n.children = findSubagents(s.SessionID, 1)
			stampSession(n.children, s.SessionID)
		}
		roots = append(roots, n)
	}
	return roots
}

// stampSession records the owning session id on every descendant agent so the
// picker can jump to the session's pane from any row.
func stampSession(nodes []*treeNode, sessionID string) {
	for _, n := range nodes {
		n.sessionID = sessionID
		stampSession(n.children, sessionID)
	}
}

// findSubagents looks for agent logs under <project>/<parentID>/subagents and
// recurses, so nested agents (an agent that itself spawned agents) also appear.
func findSubagents(parentID string, depth int) []*treeNode {
	if depth > 6 || parentID == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	pattern := filepath.Join(home, ".claude", "projects", "*", parentID, "subagents", "agent-*.meta.json")
	metas, _ := filepath.Glob(pattern)

	out := make([]*treeNode, 0, len(metas))
	for _, mp := range metas {
		logPath := strings.TrimSuffix(mp, ".meta.json") + ".jsonl"
		agentID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(mp), "agent-"), ".meta.json")
		meta := readAgentMeta(mp)

		label := meta.AgentType
		if label == "" {
			label = "agent"
		}
		if meta.Description != "" {
			label += ": " + meta.Description
		}

		n := &treeNode{
			kind:      kindAgent,
			label:     label,
			agentID:   agentID,
			agentType: meta.AgentType,
			logPath:   logPath,
		}
		if fi, err := os.Stat(mp); err == nil {
			n.startedAt = fi.ModTime().UnixMilli()
		}
		if fi, err := os.Stat(logPath); err == nil {
			n.lastMod = fi.ModTime()
		}
		n.children = findSubagents(agentID, depth+1)
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].startedAt < out[j].startedAt })
	return out
}

func readAgentMeta(path string) agentMeta {
	var m agentMeta
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// sessionGroup buckets a session by how (and whether) the picker can jump to it.
// The picker lists the groups in this order, each under its own header.
type sessionGroup int

const (
	groupTmux        sessionGroup = iota // reachable via a tmux pane
	groupGhostty                         // reachable via a matched Ghostty surface
	groupUnreachable                     // neither — can't jump
)

// group classifies a session root. tmux wins over Ghostty when both resolve.
func (n *treeNode) group() sessionGroup {
	switch {
	case n.pane != "":
		return groupTmux
	case n.ghosttyID != "":
		return groupGhostty
	default:
		return groupUnreachable
	}
}

func (n *treeNode) agentRunning() bool {
	return !n.lastMod.IsZero() && time.Since(n.lastMod) < agentActiveWindow
}
