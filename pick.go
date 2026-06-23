package main

import (
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// escGrace ignores an Esc that arrives right after open, so a terminal's
// startup query-response bytes can't be misread as Esc and close the popup.
const escGrace = 250 * time.Millisecond

// refreshInterval is how often the open picker re-runs the live session query
// in the background to stay current. A refresh in flight is never duplicated.
const refreshInterval = 2 * time.Second

// timeCol is the fixed display column where the uptime starts, so it lines up
// across every row regardless of tree depth, label, or status width.
const timeCol = 64

// statusW is the fixed display width of the status column.
const statusW = 14

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E88"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	helpBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	busyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220")) // amber
	idleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	doneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))             // green
	helpStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203")) // red

	selectedRowStyle = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	agentLabelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)

// runPicker paints the full tree instantly from cache (no claude query, no
// subagent glob), then refreshes in the background.
func runPicker() {
	logf("pick: start")
	m := pickerModel{width: 100, ready: true, started: time.Now(), loading: true,
		recs: listSessionRecs(), roots: loadTreeCache(), userExpand: map[string]bool{}}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		logf("pick: run error: %v", err)
		os.Exit(1)
	}
	logf("pick: exit")
}

type pickerModel struct {
	roots      []*treeNode
	recs       map[string]sessionRec
	cursor     int
	width      int
	ready      bool
	loading    bool
	started    time.Time
	errMsg     string
	filtering  bool
	query      string
	userExpand map[string]bool // node key -> explicit expand state (overrides auto)
}

type loadedMsg struct {
	roots []*treeNode
	err   string
}

// refresh runs the live query and builds the full tree off the UI thread,
// caches it, and reports back via loadedMsg.
func refresh() tea.Msg {
	sessions, err := pollSessions()
	if err != nil {
		logf("pick: pollSessions error: %v", err)
		return loadedMsg{err: err.Error()}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt < sessions[j].StartedAt
	})
	reconcileStale(sessions)
	roots := buildTree(sessions, true)
	saveTreeCache(roots)
	return loadedMsg{roots: roots}
}

// reconcileStale clears the tmux badge and drops the registry record for any
// session that is no longer live — covering abnormal exits (crash/kill) where
// no SessionEnd hook fired to clean up.
func reconcileStale(sessions []sessionInfo) {
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.SessionID] = true
	}
	for id, rec := range listSessionRecs() {
		if !live[id] {
			setAttention(rec.Pane, false)
			removeSessionRec(id)
		}
	}
}

func (m pickerModel) Init() tea.Cmd { return tea.Batch(refresh, tick()) }

// nodeKey identifies a node for remembering its expand state across refreshes.
func nodeKey(n *treeNode) string {
	if n.kind == kindAgent {
		return "a:" + n.agentID
	}
	return "s:" + n.sessionID
}

// nodeComplete reports whether a node itself is finished (not working/needing input).
func (m pickerModel) nodeComplete(n *treeNode) bool {
	if n.kind == kindAgent {
		return !n.agentRunning()
	}
	if n.status == "busy" || m.recs[n.sessionID].needsAttention() {
		return false
	}
	return true
}

// subtreeComplete is true when a node and everything under it is finished.
func (m pickerModel) subtreeComplete(n *treeNode) bool {
	if !m.nodeComplete(n) {
		return false
	}
	for _, c := range n.children {
		if !m.subtreeComplete(c) {
			return false
		}
	}
	return true
}

// childrenComplete reports whether every child subtree is finished. Collapsing
// hides the children, so this — not the node's own status — decides the default:
// a working session whose subagents are all done still collapses.
func (m pickerModel) childrenComplete(n *treeNode) bool {
	for _, c := range n.children {
		if !m.subtreeComplete(c) {
			return false
		}
	}
	return true
}

// effectiveExpanded honors an explicit user toggle, else defaults to collapsed
// when the children are all done and expanded when a child is still active.
func (m pickerModel) effectiveExpanded(n *treeNode) bool {
	if v, ok := m.userExpand[nodeKey(n)]; ok {
		return v
	}
	return !m.childrenComplete(n)
}

// visibleFlat is the list of rows to show: when filtering, every matching node
// flat; otherwise a pre-order walk honoring collapse, with tree prefixes set.
func (m pickerModel) visibleFlat() []*treeNode {
	var out []*treeNode
	if m.query != "" {
		q := strings.ToLower(m.query)
		var walk func(ns []*treeNode)
		walk = func(ns []*treeNode) {
			for _, n := range ns {
				if strings.Contains(strings.ToLower(n.label), q) {
					out = append(out, n)
				}
				walk(n.children)
			}
		}
		walk(m.roots)
		return out
	}
	var walk func(ns []*treeNode, prefix string, depth int)
	walk = func(ns []*treeNode, prefix string, depth int) {
		for i, n := range ns {
			last := i == len(ns)-1
			n.depth = depth
			switch {
			case depth == 0:
				n.prefix = ""
			case last:
				n.prefix = prefix + "└─ "
			default:
				n.prefix = prefix + "├─ "
			}
			out = append(out, n)
			if len(n.children) > 0 && m.effectiveExpanded(n) {
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
	}
	walk(m.roots, "", 0)
	return out
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.ready = true

	case tickMsg:
		if m.loading {
			return m, tick()
		}
		m.loading = true
		return m, tea.Batch(refresh, tick())

	case loadedMsg:
		m.loading = false
		logf("pick: loaded err=%q", msg.err)
		if msg.err != "" {
			m.errMsg = msg.err // keep the cached tree on screen
			return m, nil
		}
		m.errMsg = ""
		m.recs = listSessionRecs()
		m.roots = msg.roots
		m.clampCursor()

	case tea.KeyMsg:
		if m.filtering {
			return m.updateFiltering(msg)
		}
		return m.updateNormal(msg)
	}
	return m, nil
}

func (m pickerModel) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := m.visibleFlat()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if time.Since(m.started) < escGrace {
			return m, nil
		}
		return m, tea.Quit
	case "/":
		m.filtering = true
		m.cursor = 0
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(vis)-1)
	case "r":
		if !m.loading {
			m.loading = true
			return m, refresh
		}
	case " ", "tab":
		m.setExpand(vis, !m.curExpanded(vis))
	case "right", "l":
		m.setExpand(vis, true)
	case "left", "h":
		m.setExpand(vis, false)
	case "enter":
		return m.jump(vis)
	}
	return m, nil
}

func (m pickerModel) updateFiltering(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filtering = false
		m.query = ""
		m.cursor = 0
	case tea.KeyEnter:
		return m.jump(m.visibleFlat())
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyBackspace:
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.cursor = 0
		}
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyDown:
		if m.cursor < len(m.visibleFlat())-1 {
			m.cursor++
		}
	case tea.KeySpace:
		m.query += " "
		m.cursor = 0
	case tea.KeyRunes:
		m.query += string(msg.Runes)
		m.cursor = 0
	}
	return m, nil
}

// curExpanded / setExpand operate on the row under the cursor.
func (m pickerModel) curExpanded(vis []*treeNode) bool {
	if m.cursor < len(vis) {
		return m.effectiveExpanded(vis[m.cursor])
	}
	return false
}

func (m *pickerModel) setExpand(vis []*treeNode, v bool) {
	if m.cursor < len(vis) {
		n := vis[m.cursor]
		if len(n.children) > 0 {
			m.userExpand[nodeKey(n)] = v
		}
	}
}

func (m *pickerModel) clampCursor() {
	if n := len(m.visibleFlat()); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

// jump resolves the selected row's session to a tmux pane and goes there.
func (m pickerModel) jump(vis []*treeNode) (tea.Model, tea.Cmd) {
	if m.cursor >= len(vis) {
		return m, nil
	}
	n := vis[m.cursor]
	pane := m.recs[n.sessionID].Pane
	if pane == "" {
		pane = pidToPane(m.sessionPID(n.sessionID))
	}
	logf("pick: jump session=%s pane=%q", n.sessionID, pane)
	if pane == "" {
		m.errMsg = "no tmux pane found for this session (was it started under the hook?)"
		return m, nil
	}
	if err := jumpToPane(pane); err != nil {
		logf("pick: jump error: %v", err)
		m.errMsg = "jump failed: " + err.Error()
		return m, nil
	}
	return m, tea.Quit
}

func (m pickerModel) sessionPID(sessionID string) int {
	var find func(ns []*treeNode) int
	find = func(ns []*treeNode) int {
		for _, n := range ns {
			if n.kind == kindSession && n.sessionID == sessionID {
				return n.pid
			}
			if p := find(n.children); p != 0 {
				return p
			}
		}
		return 0
	}
	return find(m.roots)
}

func (m pickerModel) View() string {
	if !m.ready {
		return "Loading…"
	}
	var b strings.Builder
	refreshing := ""
	if m.loading {
		refreshing = " ⟳"
	}
	b.WriteString(titleStyle.Render("cmanager") + dimStyle.Render("  ·  jump to a Claude session"+refreshing) + "\n")

	if m.filtering || m.query != "" {
		b.WriteString(dimStyle.Render("/") + m.query + dimStyle.Render("▏") + "\n\n")
	} else {
		b.WriteString("\n")
	}

	if m.errMsg != "" {
		b.WriteString(helpStyle.Render(m.errMsg) + "\n\n")
	}

	vis := m.visibleFlat()
	if len(vis) == 0 {
		if m.query != "" {
			b.WriteString(dimStyle.Render("(no matches)") + "\n")
		} else {
			b.WriteString(dimStyle.Render("No Claude sessions found.") + "\n")
		}
	}
	for i, n := range vis {
		b.WriteString(m.renderRow(n, i == m.cursor) + "\n")
	}

	if m.cursor < len(vis) {
		b.WriteString(m.detailPanel(vis[m.cursor]))
	}

	help := "↑/↓ move · enter jump · space expand · / filter · r refresh · q quit"
	if m.filtering {
		help = "type to filter · ↑/↓ move · enter jump · esc clear"
	}
	b.WriteString("\n" + helpBarStyle.Render(help))
	return b.String()
}

// detailPanel shows insights for the selected row beneath the list: the full
// cwd and what a session is currently working on, or a subagent's identity.
func (m pickerModel) detailPanel(n *treeNode) string {
	w := max(20, m.width-2)
	sep := dimStyle.Render(strings.Repeat("─", w)) + "\n"
	if n.kind == kindAgent {
		state := doneStyle.Render("done")
		if n.agentRunning() {
			state = busyStyle.Render("running")
		}
		return sep + state + dimStyle.Render(" · subagent · ") + agentLabelStyle.Render(truncate(n.label, w-16)) + "\n"
	}
	out := sep + titleStyle.Render(truncate(n.cwd, w))
	if rec := m.recs[n.sessionID]; rec.needsAttention() && rec.Message != "" {
		out += "\n" + helpStyle.Render("● "+truncate(oneLine(rec.Message), w-2))
	} else if n.task != "" {
		out += "\n" + dimStyle.Render("→ "+truncate(oneLine(n.task), w-2))
	}
	return out + "\n"
}

// fit truncates or pads s to exactly w display cells (assuming 1 cell/rune,
// which holds for our box-drawing/status glyphs and ASCII labels).
func fit(s string, w int) string {
	if w < 1 {
		return ""
	}
	r := []rune(s)
	switch {
	case len(r) > w:
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	default:
		return s + strings.Repeat(" ", w-len(r))
	}
}

func (m pickerModel) renderRow(n *treeNode, selected bool) string {
	sel := "  "
	if selected {
		sel = "▸ "
	}
	filtering := m.query != ""

	prefix := n.prefix
	if filtering {
		prefix = ""
	}
	prefixW := len([]rune(prefix))

	caret := "  "
	if !filtering && len(n.children) > 0 {
		if m.effectiveExpanded(n) {
			caret = "▾ "
		} else {
			caret = "▸ "
		}
	}

	// Status dot + text, with their color.
	var dotCh, statusTxt string
	var st lipgloss.Style
	if n.kind == kindAgent {
		if n.agentRunning() {
			dotCh, statusTxt, st = "●", "running", busyStyle
		} else {
			dotCh, statusTxt, st = "✓", "done", doneStyle
		}
	} else {
		rec := m.recs[n.sessionID]
		switch {
		case n.status == "busy":
			dotCh, statusTxt, st = "●", "working", busyStyle
		case rec.needsAttention():
			// A blocking prompt (permission / decision) — clean red label; the
			// actual question shows in the detail panel.
			dotCh, statusTxt, st = "●", "waiting", helpStyle
		default:
			dotCh, statusTxt, st = "○", "idle", idleStyle
		}
	}

	// Label width shrinks with depth so the status/time columns stay aligned.
	// layout: sel(2) prefix(pw) caret(2) dot(1) sp(1) label(lw) sp(1) status(sw) sp(1) time
	lw := timeCol - 8 - prefixW - statusW
	if lw < 8 {
		lw = 8
	}
	label := fit(n.label, lw)
	if n.kind == kindAgent {
		label = agentLabelStyle.Render(label)
	}
	status := st.Render(fit(statusTxt, statusW))

	line := sel + dimStyle.Render(prefix) + dimStyle.Render(caret) +
		st.Render(dotCh) + " " + label + " " + status + " " + dimStyle.Render(uptime(n.startedAt))
	if selected {
		return selectedRowStyle.Render(line)
	}
	return line
}
