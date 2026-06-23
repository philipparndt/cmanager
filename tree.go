package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cmanager/internal/agentfs"
)

type nodeKind int

const (
	kindSession nodeKind = iota
	kindAgent
)

// treeNode is one row in the tree: an interactive session or a (possibly
// nested) subagent spawned by one.
type treeNode struct {
	kind      nodeKind
	label     string
	sessionID string // session id (sessions only)
	agentID   string // agent id (agents only)
	agentType string
	cwd       string
	status    string // sessions: "busy" | "idle"
	startedAt int64  // epoch ms
	lastMod   time.Time
	logPath   string // transcript (session) or agent log (agent)
	depth     int
	prefix    string // tree connector prefix, set by flattenTree
	children  []*treeNode

	pid     int    // claude process id (sessions only), for stopping it
	managed bool   // wrapped by a cld: live screen + prompt injection
	cldID   string // agentfs id for screen/input, when managed
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

// agentActiveWindow: an agent whose log was touched within this window is
// treated as still running (there is no explicit status on disk).
const agentActiveWindow = 10 * time.Second

// buildTree turns the flat session list into roots with their subagents nested
// underneath. `managed` maps a claude pid to its cld wrapper, if any.
func buildTree(sessions []sessionInfo, managed map[int]agentfs.Meta) []*treeNode {
	roots := make([]*treeNode, 0, len(sessions))
	for _, s := range sessions {
		n := &treeNode{
			kind:      kindSession,
			label:     prettyCwd(s.Cwd),
			sessionID: s.SessionID,
			cwd:       s.Cwd,
			status:    s.Status,
			startedAt: s.StartedAt,
			logPath:   transcriptPath(s.SessionID),
			pid:       s.PID,
		}
		if m, ok := managed[s.PID]; ok {
			n.managed = true
			n.cldID = m.ID
		}
		n.children = findSubagents(s.SessionID, 1)
		roots = append(roots, n)
	}
	return roots
}

// managedByPID indexes live cld wrappers by the claude pid they manage.
func managedByPID() map[int]agentfs.Meta {
	out := map[int]agentfs.Meta{}
	for _, m := range agentfs.List() {
		out[m.ChildPID] = m
	}
	return out
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

func (n *treeNode) agentRunning() bool {
	return !n.lastMod.IsZero() && time.Since(n.lastMod) < agentActiveWindow
}

// flattenTree returns nodes in pre-order, assigning each a display prefix
// (tree connectors) and depth. The flat order matches the rendered order so a
// single cursor index addresses both.
func flattenTree(roots []*treeNode) []*treeNode {
	var flat []*treeNode
	var walk func(nodes []*treeNode, prefix string, depth int)
	walk = func(nodes []*treeNode, prefix string, depth int) {
		for i, n := range nodes {
			last := i == len(nodes)-1
			n.depth = depth
			switch {
			case depth == 0:
				n.prefix = ""
			case last:
				n.prefix = prefix + "└─ "
			default:
				n.prefix = prefix + "├─ "
			}
			flat = append(flat, n)

			childPrefix := prefix
			if depth > 0 {
				if last {
					childPrefix = prefix + "   "
				} else {
					childPrefix = prefix + "│  "
				}
			}
			walk(n.children, childPrefix, depth+1)
		}
	}
	walk(roots, "", 0)
	return flat
}
