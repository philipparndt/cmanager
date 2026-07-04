package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The picker caches the full tree (sessions *and* nested subagents) from the
// last refresh, so opening it paints everything at once instantly — no waiting
// on the ~1.7s `claude agents` query or the subagent glob. The tree is stored
// hierarchically so the picker can collapse completed subtrees. The background
// refresh then updates it in place.

// cachedNode is the serializable shape of a tree node and its children.
type cachedNode struct {
	Kind      nodeKind     `json:"k"`
	Label     string       `json:"l"`
	SessionID string       `json:"s"`
	AgentID   string       `json:"a,omitempty"`
	Status    string       `json:"st,omitempty"`
	Task      string       `json:"tk,omitempty"`
	StartedAt int64        `json:"t,omitempty"`
	LimitAt   int64        `json:"lr,omitempty"`  // usage-limit reset, epoch ms; 0 = not limited
	Running   bool         `json:"r,omitempty"`   // agent running snapshot
	Pid       int          `json:"pid,omitempty"` // session pid, for the pid→pane fallback
	Cwd       string       `json:"cwd,omitempty"` // session cwd, for the Ghostty cwd match
	Pane      string       `json:"pn,omitempty"`  // resolved tmux pane; "" = not in tmux
	WinKey    string       `json:"wk,omitempty"`  // tmux window grouping key
	WinLabel  string       `json:"wl,omitempty"`  // tmux window header label
	Ghostty   string       `json:"gh,omitempty"`  // resolved Ghostty surface id, when not in tmux
	Owner     string       `json:"ow,omitempty"`  // hosting process, when not in tmux/Ghostty
	Children  []cachedNode `json:"c,omitempty"`
}

func treeCachePath() string { return filepath.Join(baseDir(), "cache-tree.json") }

func toCached(n *treeNode) cachedNode {
	cn := cachedNode{
		Kind: n.kind, Label: n.label, SessionID: n.sessionID, AgentID: n.agentID,
		Status: n.status, Task: n.task, StartedAt: n.startedAt, Running: n.agentRunning(),
		Pid: n.pid, Cwd: n.cwd, Pane: n.pane, WinKey: n.winKey, WinLabel: n.winLabel, Ghostty: n.ghosttyID, Owner: n.owner,
	}
	if !n.limitReset.IsZero() {
		cn.LimitAt = n.limitReset.UnixMilli()
	}
	for _, c := range n.children {
		cn.Children = append(cn.Children, toCached(c))
	}
	return cn
}

func fromCached(cn cachedNode) *treeNode {
	n := &treeNode{
		kind: cn.Kind, label: cn.Label, sessionID: cn.SessionID, agentID: cn.AgentID,
		status: cn.Status, task: cn.Task, startedAt: cn.StartedAt,
		pid: cn.Pid, cwd: cn.Cwd, pane: cn.Pane, winKey: cn.WinKey, winLabel: cn.WinLabel, ghosttyID: cn.Ghostty, owner: cn.Owner,
	}
	if cn.Running { // make agentRunning() report true again
		n.lastMod = time.Now()
	}
	if cn.LimitAt != 0 {
		n.limitReset = time.UnixMilli(cn.LimitAt)
	}
	for _, c := range cn.Children {
		n.children = append(n.children, fromCached(c))
	}
	return n
}

func saveTreeCache(roots []*treeNode) {
	cns := make([]cachedNode, len(roots))
	for i, r := range roots {
		cns[i] = toCached(r)
	}
	b, err := json.Marshal(cns)
	if err != nil || os.MkdirAll(baseDir(), 0o755) != nil {
		return
	}
	tmp := treeCachePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, treeCachePath())
	}
}

func loadTreeCache() []*treeNode {
	b, err := os.ReadFile(treeCachePath())
	if err != nil {
		return nil
	}
	var cns []cachedNode
	if json.Unmarshal(b, &cns) != nil {
		return nil
	}
	roots := make([]*treeNode, len(cns))
	for i, cn := range cns {
		roots[i] = fromCached(cn)
	}
	return roots
}
