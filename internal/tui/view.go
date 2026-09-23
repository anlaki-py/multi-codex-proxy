package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Layout and rendering live here. Model updates and account actions stay
// in tui.go. Rules: never panic on tiny screens, reflow on every
// WindowSizeMsg, one list renderer for all widths. Bars are built to fit
// by construction, never cut after styling, so wide screens stay clean.

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	badStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15"))
	barFill     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	headerStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	errBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("9")).Padding(0, 1)
)

func (m Model) contentWidth() int {
	if m.width <= 0 {
		return 76
	}
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	if w > m.width {
		w = m.width
	}
	return w
}

func (m Model) isPortrait() bool { return m.width < 80 }

// clampViewport keeps cursor visible for any height.
func (m *Model) clampViewport() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if len(m.state.Accounts) > 0 && m.cursor >= len(m.state.Accounts) {
		m.cursor = len(m.state.Accounts) - 1
	}
	rows := m.visibleRows()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m Model) errorLines() int {
	if m.lastErr == "" {
		return 0
	}
	if m.lastHint != "" {
		return 4
	}
	return 3
}

func (m Model) logsKeep() int {
	keep := 2
	switch {
	case m.height > 40:
		keep = 6
	case m.height > 30:
		keep = 4
	case m.height > 22:
		keep = 3
	}
	if m.isPortrait() && keep > 3 {
		keep = 3
	}
	return keep
}

// linesPerAccount is the worst case block height: header plus two bars.
// Narrow screens use a two line header, so they need one more row.
func (m Model) linesPerAccount() int {
	if m.contentWidth() < 40 {
		return 4
	}
	return 3
}

// visibleRows counts accounts, not lines, so every account on screen
// shows its full quota block.
func (m Model) visibleRows() int {
	reserved := 8 + m.errorLines()
	if m.confirmDel {
		reserved++
	}
	reserved += m.logsKeep() + 1
	avail := m.height - reserved
	count := avail / m.linesPerAccount()
	if count < 1 {
		count = 1
	}
	return count
}

func (m Model) View() string {
	var b strings.Builder
	cw := m.contentWidth()
	serverDot := badStyle.Render("stopped")
	if m.serving {
		serverDot = okStyle.Render("live " + m.addr)
	}
	authLabel := dimStyle.Render("open")
	if m.server != nil && m.server.APIKey != "" {
		authLabel = warnStyle.Render("locked")
	}
	b.WriteString(headerStyle.Render(
		titleStyle.Render("multi-codex-proxy")+" "+serverDot+" "+authLabel+
			dimStyle.Render("  "+truncate(m.status, max(10, cw-40)))) + "\n")

	if m.lastErr != "" {
		box := "ERR " + m.lastErr
		if m.lastHint != "" {
			box += "\nHint: " + m.lastHint
		}
		b.WriteString(errBoxStyle.Render(truncateLines(box, cw)) + "\n")
	}

	if len(m.state.Accounts) == 0 {
		b.WriteString(dimStyle.Render("No accounts yet. Press a to sign in.") + "\n")
	} else {
		b.WriteString(m.listView(cw))
	}

	if m.confirmDel {
		b.WriteString(warnStyle.Render("Delete selected account? y / n") + "\n")
	}
	b.WriteString(m.logsView(cw))
	b.WriteString("\n" + dimStyle.Render(m.footerKeys()))
	return b.String()
}

// listView shows every visible account with its quota bars. No selection
// needed to see details. Extra width goes into longer bars and full
// reset times, not into a side panel.
func (m Model) listView(cw int) string {
	var b strings.Builder
	end := min(len(m.state.Accounts), m.top+m.visibleRows())
	for i := m.top; i < end; i++ {
		a := m.state.Accounts[i]
		for _, line := range accountBlock(a, cw, i == m.cursor) {
			b.WriteString(line + "\n")
		}
	}
	if len(m.state.Accounts) > m.visibleRows() {
		b.WriteString(dimStyle.Render(
			fmt.Sprintf("-- %d/%d --", m.cursor+1, len(m.state.Accounts))) + "\n")
	}
	return b.String()
}

func (m Model) logsView(cw int) string {
	if len(m.logs) == 0 {
		return ""
	}
	keep := m.logsKeep()
	start := max(0, len(m.logs)-keep)
	var b strings.Builder
	b.WriteString(dimStyle.Render("logs") + "\n")
	for _, l := range m.logs[start:] {
		b.WriteString(dimStyle.Render(truncate(l, cw)) + "\n")
	}
	return b.String()
}

// footerKeys shortens with width so keys never wrap on small screens.
func (m Model) footerKeys() string {
	if m.width < 60 {
		return "a add  r ref  e on/off  d del  s go  q out"
	}
	if m.width < 100 {
		return "a add   r refresh   e enable   d delete   s serve   q quit"
	}
	return "a add   r refresh   R all   e enable   d delete   s serve   q quit"
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func truncatePad(s string, n int) string {
	t := truncate(s, n)
	if len([]rune(t)) < n {
		return t + strings.Repeat(" ", n-len([]rune(t)))
	}
	return t
}

func truncateLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = truncate(l, width-4)
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
