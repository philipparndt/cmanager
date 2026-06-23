package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cmanager/internal/agentfs"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// detailHeaderRows is how many lines the detail view draws above the mirrored
// screen (title, status, divider) — used to map mouse coordinates back into the
// session's own screen space.
const detailHeaderRows = 3

const pollInterval = 1500 * time.Millisecond

// toastTTL is how long a completion toast stays on screen before fading.
const toastTTL = 6 * time.Second

// screenInterval is the (much faster) cadence for refreshing the live mirror of
// a managed session, so typing feels responsive (~60fps). It only re-reads a
// small file, unlike the full session poll, and cld dedups unchanged frames so
// an idle session costs nothing.
const screenInterval = 16 * time.Millisecond

// ---- styles -------------------------------------------------------------

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

	roleUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	roleClaudeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E88"))
	toolStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
)

// ---- model --------------------------------------------------------------

type mode int

const (
	modeOverview mode = iota
	modeDetail
)

type model struct {
	sessions   []sessionInfo
	flat       []*treeNode        // visible nodes, pre-order
	needsHelp  map[string]helpRec // sessionId -> latest unanswered prompt
	managed    map[int]agentfs.Meta
	helpOffset int64
	cursor     int
	mode       mode

	// detail-view state
	detailKey     string // identity of what's displayed (logPath or "screen:<id>")
	detailLabel   string
	detailManaged bool
	detailCldID   string
	prefixArmed   bool // ctrl+g pressed; next key is a detach command or forwarded
	blurred       bool // cmanager's window lost focus — release any size request

	width  int
	height int
	vp     viewport.Model
	ready  bool
	errMsg string

	// notifications
	prevStatus map[string]string // sessionID -> last seen status, for transitions
	alerted    map[string]bool   // sessionIDs already beeped for needing attention
	toasts     []toast           // recent completion toasts, pruned by age

	// mouse text selection (screen coordinates; finalized copy goes via OSC 52)
	selecting            bool // a drag is in progress
	hasSel               bool // a selection exists and should be highlighted
	selStartX, selStartY int
	selEndX, selEndY     int
}

// toast is a transient completion notice shown for toastTTL then dropped.
type toast struct {
	text string
	born time.Time
}

func newModel() model {
	return model{
		needsHelp:  map[string]helpRec{},
		managed:    map[int]agentfs.Meta{},
		mode:       modeOverview,
		prevStatus: map[string]string{},
		alerted:    map[string]bool{},
	}
}

// ---- messages & commands ------------------------------------------------

type tickMsg struct{}
type screenTickMsg struct{}
type refreshMsg struct {
	sessions  []sessionInfo
	helpLines []helpRec
	managed   map[int]agentfs.Meta
	offset    int64
	err       string
}
type detailMsg struct {
	key     string
	content string
}
type toastTickMsg struct{}

// copiedMsg reports that a mouse selection was copied to the clipboard.
type copiedMsg struct{ n int }

// copyToClipboard writes the text to the system clipboard via OSC 52 (works
// locally and over SSH, given a terminal that honors clipboard writes). It goes
// out on stderr so it doesn't disturb bubbletea's stdout frame.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stderr, ansi.SetSystemClipboard(text))
		return copiedMsg{n: len([]rune(text))}
	}
}

// bell emits a terminal BEL on stderr (separate from bubbletea's stdout render)
// so a session newly needing attention makes an audible alert.
func bell() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stderr, "\a")
		return nil
	}
}

func toastTick() tea.Cmd {
	return tea.Tick(toastTTL, func(time.Time) tea.Msg { return toastTickMsg{} })
}

// prune drops toasts older than toastTTL.
func pruneToasts(ts []toast, now time.Time) []toast {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t.born) < toastTTL {
			out = append(out, t)
		}
	}
	return out
}

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func screenTick() tea.Cmd {
	return tea.Tick(screenInterval, func(time.Time) tea.Msg { return screenTickMsg{} })
}

func (m model) refresh() tea.Cmd {
	offset := m.helpOffset
	return func() tea.Msg {
		sessions, err := pollSessions()
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		lines, newOffset := readNewHelpLines(offset)
		return refreshMsg{
			sessions:  sessions,
			helpLines: lines,
			managed:   managedByPID(),
			offset:    newOffset,
			err:       errStr,
		}
	}
}

// loadDetail fetches the content for the current detail target: a live cld
// screen for managed sessions, otherwise the rendered transcript.
func (m model) loadDetail() tea.Cmd {
	if m.detailManaged {
		id := m.detailCldID
		key := "screen:" + id
		return func() tea.Msg {
			s, err := agentfs.ReadScreen(id)
			if err != nil {
				s = "(waiting for screen… is cld still running?)"
			}
			return detailMsg{key: key, content: s}
		}
	}
	path, width := m.detailKey, m.vp.Width
	return func() tea.Msg {
		return detailMsg{key: path, content: renderTranscriptFile(path, 80, width)}
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(), screenTick())
}

// rebuildTree recomputes the flattened tree from the current sessions.
func (m *model) rebuildTree() {
	roots := buildTree(m.sessions, m.managed)
	m.flat = flattenTree(roots)
	if m.cursor >= len(m.flat) {
		m.cursor = max(0, len(m.flat)-1)
	}
}

// ---- update -------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		vpHeight := msg.Height - 6
		if vpHeight < 3 {
			vpHeight = 3
		}
		if !m.ready {
			m.vp = viewport.New(msg.Width, vpHeight)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = vpHeight
		}
		// re-render detail at new width
		if m.mode == modeDetail {
			m.requestRemoteSize()
			return m, m.loadDetail()
		}
		return m, nil

	case tickMsg:
		return m, m.refresh()

	case screenTickMsg:
		// Fast path: re-read just the live screen of a managed session so typing
		// feels responsive. Read-only transcripts ride the slower session poll.
		if m.mode == modeDetail && m.detailManaged {
			return m, tea.Batch(m.loadDetail(), screenTick())
		}
		return m, screenTick()

	case toastTickMsg:
		before := len(m.toasts)
		m.toasts = pruneToasts(m.toasts, time.Now())
		if len(m.toasts) > 0 && len(m.toasts) != before {
			return m, toastTick()
		}
		return m, nil

	case refreshMsg:
		if msg.err != "" {
			m.errMsg = msg.err
		} else {
			m.errMsg = ""
			m.sessions = msg.sessions
			sort.Slice(m.sessions, func(i, j int) bool {
				return m.sessions[i].StartedAt < m.sessions[j].StartedAt
			})
		}
		if msg.managed != nil {
			m.managed = msg.managed
		}
		m.helpOffset = msg.offset
		for _, r := range msg.helpLines {
			m.needsHelp[r.SessionID] = r
		}
		for _, s := range m.sessions {
			if s.Status == "busy" {
				delete(m.needsHelp, s.SessionID)
			}
		}

		now := time.Now()
		// Completion toasts: a session that went busy -> not-busy without a
		// pending help prompt finished its work.
		for _, s := range m.sessions {
			_, needs := m.needsHelp[s.SessionID]
			if m.prevStatus[s.SessionID] == "busy" && s.Status != "busy" && !needs {
				m.toasts = append(m.toasts, toast{text: "✓ " + prettyCwd(s.Cwd) + " finished", born: now})
			}
		}
		m.prevStatus = make(map[string]string, len(m.sessions))
		for _, s := range m.sessions {
			m.prevStatus[s.SessionID] = s.Status
		}
		m.toasts = pruneToasts(m.toasts, now)

		// Audible alert once per session that newly needs attention; forget
		// sessions that no longer do, so a later prompt beeps again.
		bellNeeded := false
		for id := range m.needsHelp {
			if !m.alerted[id] {
				m.alerted[id] = true
				bellNeeded = true
			}
		}
		for id := range m.alerted {
			if _, ok := m.needsHelp[id]; !ok {
				delete(m.alerted, id)
			}
		}

		m.rebuildTree()

		cmds := []tea.Cmd{tick()}
		if bellNeeded {
			cmds = append(cmds, bell())
		}
		if len(m.toasts) > 0 {
			cmds = append(cmds, toastTick())
		}
		if m.mode == modeDetail {
			m.requestRemoteSize() // keep the size request fresh while attached
			cmds = append(cmds, m.loadDetail())
		}
		return m, tea.Batch(cmds...)

	case detailMsg:
		if m.mode == modeDetail && msg.key == m.detailKey {
			atBottom := m.vp.AtBottom()
			m.vp.SetContent(msg.content)
			if atBottom {
				m.vp.GotoBottom()
			}
		}
		return m, nil

	case tea.BlurMsg:
		// cmanager's window lost focus. If we were holding a session at our
		// mirror size, release it so the user looking at that session's own
		// terminal sees it restored to that terminal's real size.
		m.blurred = true
		if m.mode == modeDetail && m.detailManaged && m.detailCldID != "" {
			agentfs.ClearResize(m.detailCldID)
		}
		return m, nil

	case tea.FocusMsg:
		// Focus returned — reacquire the mirror size if we're viewing a session.
		m.blurred = false
		if m.mode == modeDetail {
			m.requestRemoteSize()
		}
		return m, nil

	case copiedMsg:
		m.toasts = append(m.toasts, toast{text: fmt.Sprintf("⧉ copied %d chars", msg.n), born: time.Now()})
		return m, toastTick()

	case tea.MouseMsg:
		// Wheel scrolls: forward to a managed session, else scroll our viewport.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown ||
			msg.Button == tea.MouseButtonWheelLeft || msg.Button == tea.MouseButtonWheelRight {
			m.hasSel = false // content moves out from under any selection
			if m.mode == modeDetail && m.detailManaged {
				if b := mouseToBytes(msg, detailHeaderRows); len(b) > 0 {
					_ = agentfs.SendInput(m.detailCldID, b)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}

		// Left button drives text selection (in cmanager, not the session).
		if msg.Button == tea.MouseButtonLeft {
			switch msg.Action {
			case tea.MouseActionPress:
				m.selecting, m.hasSel = true, true
				m.selStartX, m.selStartY = msg.X, msg.Y
				m.selEndX, m.selEndY = msg.X, msg.Y
			case tea.MouseActionMotion:
				if m.selecting {
					m.selEndX, m.selEndY = msg.X, msg.Y
				}
			case tea.MouseActionRelease:
				if m.selecting {
					m.selecting = false
					m.selEndX, m.selEndY = msg.X, msg.Y
					// A bare click (no drag) just clears any prior selection.
					if m.selStartX == m.selEndX && m.selStartY == m.selEndY {
						m.hasSel = false
						return m, nil
					}
					if text := m.selectedText(); text != "" {
						return m, copyToClipboard(text)
					}
				}
			}
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.mode == modeDetail {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.hasSel = false // any keystroke dismisses a mouse selection highlight

	// ctrl+c quits cmanager — except while attached to a managed session, where
	// it's forwarded so you can interrupt the session itself (detach is ctrl+]).
	if msg.String() == "ctrl+c" && !(m.mode == modeDetail && m.detailManaged) {
		return m, tea.Quit
	}

	if m.mode == modeOverview {
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.flat)-1 {
				m.cursor++
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(0, len(m.flat)-1)
		case "r":
			return m, m.refresh()
		case "x", "delete":
			if m.cursor < len(m.flat) {
				if n := m.flat[m.cursor]; n.kind == kindSession && n.pid > 0 {
					stopSession(n.pid)
					if n.managed {
						agentfs.ClearResize(n.cldID)
					}
					return m, m.refresh()
				}
			}
		case "enter", "right", "l":
			if m.cursor < len(m.flat) {
				return m.openDetail(m.flat[m.cursor])
			}
		}
		return m, nil
	}

	// ---- detail mode ----

	if m.detailManaged {
		// Managed session: forward every keystroke into the live session so you
		// can navigate, set modes, and answer prompts. Detach is a tmux-style
		// two-key sequence — the ctrl+g prefix, then Esc — so no single keystroke
		// is stolen from claude. ctrl+g (BEL) is unused by claude, so holding it
		// for the next key costs nothing; a bare Esc still passes straight
		// through and interrupts claude as in a real terminal.
		if m.prefixArmed {
			m.prefixArmed = false
			if msg.Type == tea.KeyEsc {
				return m.detach()
			}
			// Not a detach: the prefix wasn't a command, so send it through
			// followed by this key, preserving order.
			_ = agentfs.SendInput(m.detailCldID, []byte{0x07}) // ctrl+g
			if b := keyToBytes(msg); len(b) > 0 {
				_ = agentfs.SendInput(m.detailCldID, b)
			}
			return m, nil
		}
		if msg.String() == "ctrl+g" {
			m.prefixArmed = true
			return m, nil
		}
		if b := keyToBytes(msg); len(b) > 0 {
			_ = agentfs.SendInput(m.detailCldID, b)
		}
		return m, nil
	}

	// Read-only transcript: esc/q/left exit, arrows scroll.
	switch msg.String() {
	case "esc", "q", "left", "h":
		return m.detach()
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// detach leaves the detail view and returns to the overview, releasing any size
// request held on a managed session so it snaps back to its own terminal.
func (m model) detach() (tea.Model, tea.Cmd) {
	if m.detailManaged && m.detailCldID != "" {
		agentfs.ClearResize(m.detailCldID)
	}
	m.mode = modeOverview
	return m, nil
}

// openDetail switches to the detail view for a node.
func (m model) openDetail(n *treeNode) (tea.Model, tea.Cmd) {
	m.mode = modeDetail
	m.detailLabel = n.label
	m.detailManaged = n.managed
	m.detailCldID = n.cldID
	if n.managed {
		m.detailKey = "screen:" + n.cldID
		m.requestRemoteSize() // ask the session to render at our pane size
	} else {
		m.detailKey = n.logPath
	}
	m.vp.SetContent(dimStyle.Render("Loading…"))
	m.vp.GotoBottom()
	return m, m.loadDetail()
}

// requestRemoteSize asks a focused managed session to size its terminal to the
// mirror pane, so its screen fits cmanager's view exactly. It is suppressed
// while cmanager's own window is blurred, so switching back to the session's
// own terminal lets it snap to that terminal's real size. No-op otherwise.
func (m model) requestRemoteSize() {
	if m.detailManaged && !m.blurred && m.detailCldID != "" && m.vp.Width > 0 && m.vp.Height > 0 {
		_ = agentfs.WriteResize(m.detailCldID, m.vp.Width, m.vp.Height)
	}
}

// ---- view ---------------------------------------------------------------

func (m model) View() string {
	if !m.ready {
		return "Loading…"
	}
	s := m.baseView()
	if m.hasSel {
		s = m.applyHighlight(s)
	}
	return s
}

// baseView renders the current screen without the selection overlay.
func (m model) baseView() string {
	if m.mode == modeDetail {
		return m.detailView()
	}
	return m.overviewView()
}

// detailNotices builds the alert segment shown on the detail header's status
// row: a persistent warning for any *other* session needing attention (the one
// you're watching is already visible), plus live completion toasts. Excluding
// the current session is what makes this a useful background alert.
func (m model) detailNotices(cur *treeNode) string {
	var names []string
	for _, n := range m.flat {
		if n.kind != kindSession {
			continue
		}
		if cur != nil && n.sessionID == cur.sessionID {
			continue
		}
		if _, ok := m.needsHelp[n.sessionID]; ok {
			names = append(names, n.label)
		}
	}
	var parts []string
	if len(names) > 0 {
		parts = append(parts, helpStyle.Render(fmt.Sprintf("⚠ %d need attention: %s", len(names), strings.Join(names, ", "))))
	}
	parts = append(parts, m.toastLines()...)
	return strings.Join(parts, "   ")
}

// toastLines renders the live (unexpired) completion toasts, one per line.
func (m model) toastLines() []string {
	now := time.Now()
	var out []string
	for _, t := range m.toasts {
		if now.Sub(t.born) < toastTTL {
			out = append(out, doneStyle.Render(t.text))
		}
	}
	return out
}

func (m model) overviewView() string {
	var b strings.Builder

	working, idle, needs, agents, agentsRun := 0, 0, 0, 0, 0
	for _, n := range m.flat {
		if n.kind == kindAgent {
			agents++
			if n.agentRunning() {
				agentsRun++
			}
			continue
		}
		if _, ok := m.needsHelp[n.sessionID]; ok {
			needs++
		} else if n.status == "busy" {
			working++
		} else {
			idle++
		}
	}

	b.WriteString(titleStyle.Render("cmanager") + dimStyle.Render("  ·  Claude instances on this machine"))
	b.WriteString("\n")
	summary := fmt.Sprintf("%s   %s   %s   %s",
		helpStyle.Render(fmt.Sprintf("● %d needs help", needs)),
		busyStyle.Render(fmt.Sprintf("● %d working", working)),
		idleStyle.Render(fmt.Sprintf("○ %d idle", idle)),
		dimStyle.Render(fmt.Sprintf("· %d subagents (%d active)", agents, agentsRun)),
	)
	b.WriteString(summary + "\n")
	for _, line := range m.toastLines() {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")

	if m.errMsg != "" {
		b.WriteString(helpStyle.Render("error: "+m.errMsg) + "\n\n")
	}
	if len(m.flat) == 0 {
		b.WriteString(dimStyle.Render("No Claude sessions found.") + "\n")
	}

	for i, n := range m.flat {
		b.WriteString(m.renderRow(n, i == m.cursor) + "\n")
	}

	b.WriteString("\n" + helpBarStyle.Render("↑/↓ move · enter open · x stop · r refresh · q quit"))
	return b.String()
}

func (m model) renderRow(n *treeNode, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}

	var dot, statusText, label string

	if n.kind == kindAgent {
		if n.agentRunning() {
			dot = busyStyle.Render("●")
			statusText = busyStyle.Render("running")
		} else {
			dot = doneStyle.Render("✓")
			statusText = doneStyle.Render("done")
		}
		label = agentLabelStyle.Render(truncate(n.label, 44))
	} else {
		if hr, ok := m.needsHelp[n.sessionID]; ok {
			dot = helpStyle.Render("●")
			msg := oneLine(hr.Message)
			if msg == "" {
				msg = "waiting for your input"
			}
			statusText = helpStyle.Render(truncate(msg, 44))
		} else if n.status == "busy" {
			dot = busyStyle.Render("●")
			statusText = busyStyle.Render("working")
		} else {
			dot = idleStyle.Render("○")
			statusText = idleStyle.Render("idle")
		}
		label = truncate(n.label, 44)
		if n.managed {
			label += " " + doneStyle.Render("⚡")
		}
	}

	tree := dimStyle.Render(n.prefix)
	uptimeStr := dimStyle.Render(uptime(n.startedAt))
	line := fmt.Sprintf("%s%s%s %-46s %-26s %s",
		cursor, tree, dot, label, statusText, uptimeStr)

	if selected {
		return selectedRowStyle.Render(line)
	}
	return line
}

func (m model) detailView() string {
	// Find the live node for an up-to-date header.
	var node *treeNode
	for _, n := range m.flat {
		if m.detailManaged && n.cldID == m.detailCldID && n.cldID != "" {
			node = n
			break
		}
		if !m.detailManaged && n.logPath == m.detailKey {
			node = n
			break
		}
	}

	var statusRow string
	var head strings.Builder
	if node != nil {
		head.WriteString(titleStyle.Render(node.label) + "\n")
		statusRow = m.detailStatus(node)
	} else {
		head.WriteString(titleStyle.Render(m.detailLabel) + "\n")
		statusRow = dimStyle.Render("(no longer running)")
	}
	// Append background alerts to the status row, then clamp to one line so the
	// header stays exactly detailHeaderRows tall (mouse mapping depends on it).
	if notices := m.detailNotices(node); notices != "" {
		statusRow += "   " + notices
	}
	head.WriteString(ansi.Truncate(statusRow, max(10, m.width), "…") + "\n")
	head.WriteString(dimStyle.Render(strings.Repeat("─", max(10, m.width))) + "\n")

	if m.detailManaged {
		footer := "\n" + helpBarStyle.Render("keys forward to this session · shift+tab modes · scroll wheel · ctrl+g esc detach")
		return head.String() + m.vp.View() + footer
	}
	footer := "\n" + helpBarStyle.Render("↑/↓ scroll · esc/q back · ctrl+c quit  ·  live, read-only")
	return head.String() + m.vp.View() + footer
}

func (m model) detailStatus(n *treeNode) string {
	if n.kind == kindAgent {
		state := doneStyle.Render("done")
		if n.agentRunning() {
			state = busyStyle.Render("running")
		}
		return fmt.Sprintf("%s   %s", state, dimStyle.Render("subagent · "+n.agentType))
	}
	state := idleStyle.Render("idle")
	if hr, ok := m.needsHelp[n.sessionID]; ok {
		state = helpStyle.Render("needs help: " + oneLine(hr.Message))
	} else if n.status == "busy" {
		state = busyStyle.Render("working")
	}
	return fmt.Sprintf("%s   %s", state, dimStyle.Render(uptime(n.startedAt)+" · session"))
}

// ---- helpers ------------------------------------------------------------

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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cmanager error:", err)
		os.Exit(1)
	}
}
