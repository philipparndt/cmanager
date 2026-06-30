package main

import (
	"fmt"
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

// rowIndent pads every tree row in from the left so the tree sits clear of the
// terminal/popup border.
const rowIndent = "  "

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

	agentLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	winStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
)

// selSGR highlights the selected row as white-on-blue (like tmux's
// window-status-current-style), readable regardless of the row's own colors.
const selSGR = "\x1b[48;5;25m\x1b[38;5;231m"

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
	resolvePanes(roots, listSessionRecs())
	resolveGhosttyTargets(roots)
	resolveOwners(roots)
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
			setState(rec.Pane, "")
			removeSessionRec(id)
		}
	}
}

func (m pickerModel) Init() tea.Cmd { return tea.Batch(refresh, tick()) }

// nodeKey identifies a node for remembering its expand state across refreshes.
func nodeKey(n *treeNode) string {
	switch n.kind {
	case kindAgent:
		return "a:" + n.agentID
	case kindWindow:
		return "w:" + n.winKey
	case kindApp:
		return "app:" + n.winKey
	}
	return "s:" + n.sessionID
}

// nodeComplete reports whether a node itself is finished (not working/needing input).
func (m pickerModel) nodeComplete(n *treeNode) bool {
	switch n.kind {
	case kindAgent:
		return !n.agentRunning()
	case kindWindow, kindApp:
		return true // a container; childrenComplete drives whether it collapses
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
	// tmux sessions nest under a collapsible node per window; ghostty and
	// unreachable sessions follow flat under their own headers. Root depth is 0
	// so the grouping can't disturb tree prefixes.
	walk(m.displayRoots(), "", 0)
	return out
}

// displayRoots returns the top-level rows: tmux sessions grouped under a
// collapsible node per window, ghostty sessions flat, and unreachable sessions
// grouped under a node per hosting app. Both group kinds collapse like any tree
// node (idle groups fold to a single line).
func (m pickerModel) displayRoots() []*treeNode {
	var tmux, ghostty, unreach []*treeNode
	for _, n := range m.roots {
		switch n.group() {
		case groupTmux:
			tmux = append(tmux, n)
		case groupGhostty:
			ghostty = append(ghostty, n)
		default:
			unreach = append(unreach, n)
		}
	}
	out := groupNodes(tmux, kindWindow, func(s *treeNode) (string, string) {
		if s.winLabel == "" {
			return s.winKey, "tmux"
		}
		return s.winKey, s.winLabel
	})
	out = append(out, ghostty...)
	out = append(out, groupNodes(unreach, kindApp, func(s *treeNode) (string, string) {
		owner := s.owner
		if owner == "" {
			owner = "unknown"
		}
		return owner, owner
	})...)
	return out
}

// groupNodes wraps session roots under a synthetic parent of the given kind,
// keyed by keyLabel(session)->(key,label). Groups appear in the order their key
// is first seen, sessions in their original order within each group.
func groupNodes(sessions []*treeNode, kind nodeKind, keyLabel func(*treeNode) (key, label string)) []*treeNode {
	var order []string
	by := map[string]*treeNode{}
	for _, s := range sessions {
		key, label := keyLabel(s)
		g := by[key]
		if g == nil {
			g = &treeNode{kind: kind, label: label, winKey: key}
			by[key] = g
			order = append(order, key)
		}
		g.children = append(g.children, s)
	}
	out := make([]*treeNode, 0, len(order))
	for _, k := range order {
		out = append(out, by[k])
	}
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
	case "alt+up", "alt+k":
		m.cursor = m.nextActive(vis, -1)
		m.expandAt(vis, m.cursor)
	case "alt+down", "alt+j":
		m.cursor = m.nextActive(vis, +1)
		m.expandAt(vis, m.cursor)
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
		m.collapseLeft(vis)
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

// collapseLeft collapses an expanded parent in place; on a leaf or an already
// collapsed node it jumps to the parent and collapses that (tree-nav style).
func (m *pickerModel) collapseLeft(vis []*treeNode) {
	if m.cursor >= len(vis) {
		return
	}
	n := vis[m.cursor]
	if len(n.children) > 0 && m.effectiveExpanded(n) {
		m.userExpand[nodeKey(n)] = false
		return
	}
	for i := m.cursor - 1; i >= 0; i-- {
		if vis[i].depth < n.depth {
			m.cursor = i
			if len(vis[i].children) > 0 {
				m.userExpand[nodeKey(vis[i])] = false
			}
			return
		}
	}
}

// rowActive reports whether a row is non-idle: a working/waiting session, a
// running agent, or a window with any active session under it.
func (m pickerModel) rowActive(n *treeNode) bool {
	if n.kind == kindWindow || n.kind == kindApp {
		return !m.childrenComplete(n)
	}
	return !m.nodeComplete(n)
}

// nextActive returns the index of the next non-idle row from the cursor in
// direction dir (+1 down / -1 up), wrapping around. Stays put if none qualify.
func (m pickerModel) nextActive(vis []*treeNode, dir int) int {
	n := len(vis)
	if n == 0 {
		return 0
	}
	for i := 1; i <= n; i++ {
		j := ((m.cursor+dir*i)%n + n) % n
		if m.rowActive(vis[j]) {
			return j
		}
	}
	return m.cursor
}

// expandAt expands the node at index i (when it has children), so option-nav
// reveals what's inside the instance it lands on — e.g. a collapsed window's
// active sessions or a session's running subagents.
func (m *pickerModel) expandAt(vis []*treeNode, i int) {
	if i >= 0 && i < len(vis) && len(vis[i].children) > 0 {
		m.userExpand[nodeKey(vis[i])] = true
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

	// A group node (window / app) has no session of its own — act on its first
	// session, which for a window lands the client on that window.
	if n.kind == kindWindow || n.kind == kindApp {
		if len(n.children) == 0 {
			return m, nil
		}
		n = n.children[0]
	}

	// Prefer tmux: switch straight to the pane.
	pane := m.recs[n.sessionID].Pane
	if pane == "" {
		pane = pidToPane(m.sessionPID(n.sessionID))
	}
	if pane != "" {
		logf("pick: jump session=%s pane=%q", n.sessionID, pane)
		if err := jumpToPane(pane); err != nil {
			logf("pick: jump error: %v", err)
			m.errMsg = "jump failed: " + err.Error()
			return m, nil
		}
		return m, tea.Quit
	}

	// Otherwise, focus the matching Ghostty surface.
	if gid := m.ghosttyTarget(n); gid != "" {
		logf("pick: jump session=%s ghostty=%q", n.sessionID, gid)
		if err := focusGhostty(gid); err != nil {
			logf("pick: ghostty focus error: %v", err)
			m.errMsg = "Ghostty focus failed: " + err.Error()
			return m, nil
		}
		return m, tea.Quit
	}

	m.errMsg = "can't jump — this session isn't in tmux or a matched Ghostty surface"
	return m, nil
}

// ghosttyTarget returns the Ghostty surface id for the session owning n: the id
// resolved at refresh, or a fresh cwd lookup when that's empty (e.g. first paint).
func (m pickerModel) ghosttyTarget(n *treeNode) string {
	root := m.sessionNode(n.sessionID)
	if root == nil {
		return ""
	}
	if root.ghosttyID != "" {
		return root.ghosttyID
	}
	return resolveGhostty(root.cwd, ghosttyTerminals())
}

// sessionNode finds the session root for a session id (any descendant agent row
// resolves back to it).
func (m pickerModel) sessionNode(sessionID string) *treeNode {
	var find func(ns []*treeNode) *treeNode
	find = func(ns []*treeNode) *treeNode {
		for _, n := range ns {
			if n.kind == kindSession && n.sessionID == sessionID {
				return n
			}
			if r := find(n.children); r != nil {
				return r
			}
		}
		return nil
	}
	return find(m.roots)
}

func (m pickerModel) sessionPID(sessionID string) int {
	if n := m.sessionNode(sessionID); n != nil {
		return n.pid
	}
	return 0
}

// sessionGroups maps every session id to its group, so each row (including a
// subagent, via its owning session) knows which section header precedes it and
// whether it renders dimmed.
func (m pickerModel) sessionGroups() map[string]sessionGroup {
	gm := map[string]sessionGroup{}
	for _, n := range m.roots {
		if n.kind == kindSession {
			gm[n.sessionID] = n.group()
		}
	}
	return gm
}

// rowGroup classifies a visible row into its section: a window node is tmux, an
// app node is unreachable, and any session/agent row inherits its owning
// session's group.
func rowGroup(n *treeNode, groups map[string]sessionGroup) sessionGroup {
	switch n.kind {
	case kindWindow:
		return groupTmux
	case kindApp:
		return groupUnreachable
	default:
		return groups[n.sessionID]
	}
}

// groupHeader is the dim divider+label shown above a session group.
func groupHeader(label string) string {
	return "\n" + dimStyle.Render("  ─── "+label+" ───") + "\n\n"
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
	groups := m.sessionGroups()
	shown := map[sessionGroup]bool{}
	for i, n := range vis {
		// tmux rows (window nodes and everything under them) carry no header — the
		// window tree is the grouping. ghostty and the can't-jump section still get
		// a divider. Suppressed while filtering (rows are a flat list).
		g := rowGroup(n, groups)
		if m.query == "" && !shown[g] {
			switch g {
			case groupGhostty:
				b.WriteString(groupHeader("ghostty"))
			case groupUnreachable:
				b.WriteString(groupHeader("can't jump · not in tmux or Ghostty"))
			}
			shown[g] = true
		}
		b.WriteString(rowIndent + m.renderRow(n, i == m.cursor, g == groupUnreachable) + "\n")
	}

	if m.cursor < len(vis) {
		b.WriteString(m.detailPanel(vis[m.cursor]))
	}

	help := "↑/↓ move · ⌥↑/↓ next active · enter jump · space expand · / filter · r refresh · q quit"
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
	if n.kind == kindWindow || n.kind == kindApp {
		noun := "tmux window"
		if n.kind == kindApp {
			noun = "app · can't jump"
		}
		return sep + winStyle.Render(truncate(n.label, w-24)) + dimStyle.Render(fmt.Sprintf("  ·  %s · %d", noun, len(n.children))) + "\n"
	}
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

func (m pickerModel) renderRow(n *treeNode, selected, unjumpable bool) string {
	filtering := m.query != ""

	prefix := n.prefix
	if filtering {
		prefix = ""
	}
	prefixW := len([]rune(prefix))

	caret := "  "
	if !filtering && len(n.children) > 0 {
		if m.effectiveExpanded(n) {
			caret = "▼ "
		} else {
			caret = "▶ "
		}
	}

	if n.kind == kindWindow || n.kind == kindApp {
		return m.renderGroupRow(n, prefix, caret, selected)
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
	// layout: prefix(pw) caret(2) dot(1) sp(1) label(lw) sp(1) status(sw) sp(1) time
	lw := timeCol - 6 - prefixW - statusW
	if lw < 8 {
		lw = 8
	}
	// Unjumpable rows carry a ⊘ marker at the right of the label column; reserve
	// its width so the status/time columns stay aligned with the jumpable rows.
	const markerW = 2
	marker := ""
	label := fit(n.label, lw)
	if unjumpable {
		marker = "⊘ "
		label = fit(n.label, lw-markerW)
	}
	status := fit(statusTxt, statusW)
	timeStr := uptime(n.startedAt)

	// Selected: a full-width black-on-yellow bar with plain (uncolored) text.
	if selected {
		return m.highlightRow(prefix + caret + dotCh + " " + label + marker + " " + status + " " + timeStr)
	}

	// Unjumpable: dim the whole row, since Enter can't jump to it.
	if unjumpable {
		return dimStyle.Render(prefix + caret + dotCh + " " + label + marker + " " + status + " " + timeStr)
	}

	labelStyled := label
	if n.kind == kindAgent {
		labelStyled = agentLabelStyle.Render(label)
	}
	return dimStyle.Render(prefix) + dimStyle.Render(caret) +
		st.Render(dotCh) + " " + labelStyled + " " + st.Render(status) + " " + dimStyle.Render(timeStr)
}

// renderGroupRow draws a grouping node (a tmux window or a hosting app): a dot
// reflecting the most urgent session under it, the group label, and a session
// count. When collapsed the dot is the only hint of what's happening inside.
func (m pickerModel) renderGroupRow(n *treeNode, prefix, caret string, selected bool) string {
	dotCh, st := m.groupDot(n)
	count := fmt.Sprintf(" (%d)", len(n.children))
	if selected {
		return m.highlightRow(prefix + caret + dotCh + " " + n.label + count)
	}
	return dimStyle.Render(prefix) + dimStyle.Render(caret) +
		st.Render(dotCh) + " " + winStyle.Render(n.label) + dimStyle.Render(count)
}

// groupDot aggregates the state of a group's sessions into one status dot: red
// if any session is waiting on the user, amber if any is working, else idle.
func (m pickerModel) groupDot(n *treeNode) (string, lipgloss.Style) {
	busy := false
	for _, s := range n.children {
		if m.recs[s.sessionID].needsAttention() {
			return "●", helpStyle
		}
		if s.status == "busy" {
			busy = true
		}
	}
	if busy {
		return "●", busyStyle
	}
	return "○", idleStyle
}

// highlightRow renders a plain (uncolored) row as a full-width black-on-yellow
// bar, padded to the window width — like tmux's choose-tree selection.
func (m pickerModel) highlightRow(plain string) string {
	if pad := m.width - len(rowIndent) - lipgloss.Width(plain); pad > 0 {
		plain += strings.Repeat(" ", pad)
	}
	return selSGR + plain + "\x1b[0m"
}
