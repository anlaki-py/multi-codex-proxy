package tui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-codex-proxy/internal/accounts"
	"multi-codex-proxy/internal/apperr"
	"multi-codex-proxy/internal/auth"
	"multi-codex-proxy/internal/codex"
	"multi-codex-proxy/internal/proxy"
)

type tickMsg struct{}
type accountsMsg struct{ state codex.State }
type errMsg struct{ err error }
type logMsg struct{ line string }

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

// Model owns TUI state. No globals, deps passed in.
type Model struct {
	repo      *accounts.Repository
	server    *proxy.Server
	http      *http.Client
	addr      string
	state     codex.State
	cursor    int
	top       int
	logs      []string
	serving   bool
	srv       *http.Server
	confirmDel bool
	status    string
	lastErr   string
	lastHint  string
	width     int
	height    int
}

func New(repo *accounts.Repository, server *proxy.Server, client *http.Client, addr string) Model {
	return Model{repo: repo, server: server, http: client, addr: addr, status: "ready", width: 80, height: 24}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tick(), m.loadAccounts)
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) loadAccounts() tea.Msg {
	return accountsMsg{state: m.repo.List()}
}

func (m *Model) pushLog(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.logs = append(m.logs, line)
	if len(m.logs) > 30 {
		m.logs = m.logs[len(m.logs)-30:]
	}
}

// setError shows a clear message plus hint. Cleared on next success.
func (m *Model) setError(err error) {
	msg, hint := apperr.UserMessage(err)
	if msg == "" {
		msg = "unknown error"
	}
	m.lastErr = msg
	m.lastHint = hint
	m.status = "error: " + truncate(msg, 60)
	m.pushLog("error: %s", msg)
	if hint != "" {
		m.pushLog("hint: %s", hint)
	}
}

func (m *Model) clearError() {
	m.lastErr = ""
	m.lastHint = ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clampViewport()
		return m, nil
	case tickMsg:
		return m, tea.Batch(tick(), m.loadAccounts)
	case accountsMsg:
		m.state = msg.state
		if m.cursor >= len(m.state.Accounts) {
			m.cursor = max(0, len(m.state.Accounts)-1)
		}
		m.clampViewport()
		return m, nil
	case errMsg:
		m.setError(msg.err)
		return m, nil
	case logMsg:
		m.clearError()
		m.status = truncate(msg.line, 60)
		m.pushLog("%s", msg.line)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// clampViewport keeps cursor visible and top in range for any height.
func (m *Model) clampViewport() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if len(m.state.Accounts) > 0 && m.cursor >= len(m.state.Accounts) {
		m.cursor = len(m.state.Accounts) - 1
	}
	rows := m.visibleRows()
	if rows < 1 {
		rows = 1
	}
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

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.confirmDel {
		if key == "y" || key == "Y" {
			m.confirmDel = false
			if len(m.state.Accounts) > 0 {
				id := m.state.Accounts[m.cursor].ID
				email := m.state.Accounts[m.cursor].Email
				if err := m.repo.Delete(id); err != nil {
					m.setError(err)
				} else {
					m.clearError()
					m.status = "deleted " + email
					m.pushLog("deleted %s", email)
				}
			}
			return m, m.loadAccounts
		}
		m.confirmDel = false
		m.status = "delete cancelled"
		return m, nil
	}
	switch key {
	case "q", "ctrl+c":
		m.stopServer()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.clampViewport()
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.state.Accounts)-1 {
			m.cursor++
			m.clampViewport()
		}
		return m, nil
	case "a":
		m.status = "browser login started, approve in browser"
		m.pushLog("login started, approve in browser")
		return m, m.doLogin()
	case "r":
		return m, m.doRefresh(false)
	case "R":
		return m, m.doRefresh(true)
	case "e":
		if len(m.state.Accounts) == 0 {
			m.setError(fmt.Errorf("no accounts yet"))
			m.lastHint = "press a to sign in first"
			return m, nil
		}
		a := m.state.Accounts[m.cursor]
		if err := m.repo.SetEnabled(a.ID, !a.Enabled); err != nil {
			m.setError(err)
		} else {
			m.clearError()
			m.status = "toggled " + a.Email
		}
		return m, m.loadAccounts
	case "d":
		if len(m.state.Accounts) > 0 {
			m.confirmDel = true
		}
		return m, nil
	case "s":
		if m.serving {
			m.stopServer()
			m.status = "server stopped"
			m.pushLog("server stopped")
		} else {
			if err := m.startServer(); err != nil {
				m.setError(err)
			} else {
				m.clearError()
				m.status = "server on " + m.addr
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) doLogin() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		acct, err := auth.StartLogin(ctx, m.repo, m.http)
		if err != nil {
			return errMsg{err}
		}
		return logMsg{line: "signed in " + acct.Email}
	}
}

func (m Model) doRefresh(all bool) tea.Cmd {
	return func() tea.Msg {
		if len(m.state.Accounts) == 0 {
			return errMsg{apperr.New("refresh", apperr.CodeAccounts,
				fmt.Errorf("no accounts yet"),
				"press a to sign in first")}
		}
		if all {
			m.repo.RefreshAll()
			return logMsg{line: "refreshed all accounts"}
		}
		a := m.state.Accounts[m.cursor]
		if _, err := m.repo.RefreshAccount(a.ID); err != nil {
			return errMsg{err}
		}
		return logMsg{line: "refreshed " + a.Email}
	}
}

// startServer binds synchronously so a busy port shows a clear error
// instead of a silent dead server.
func (m *Model) startServer() error {
	if m.serving {
		return nil
	}
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return apperr.New("serve "+m.addr, apperr.CodeConfig, err,
			"port is busy; restart with --port, e.g. --port 18790")
	}
	m.srv = &http.Server{Handler: m.server.Handler()}
	m.serving = true
	m.pushLog("server on http://%s", m.addr)
	go func() {
		_ = m.srv.Serve(ln)
	}()
	return nil
}

func (m *Model) stopServer() {
	if m.srv != nil {
		_ = m.srv.Close()
	}
	m.serving = false
}

// layout helpers: portrait is narrow, landscape is wide.

func (m Model) contentWidth() int {
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	return w
}

func (m Model) isPortrait() bool { return m.width < 80 }

func (m Model) visibleRows() int {
	reserved := 10
	if m.lastErr != "" {
		reserved += 3
	}
	if m.confirmDel {
		reserved++
	}
	rows := m.height - reserved
	if m.isPortrait() {
		rows -= 2
	}
	if rows < 3 {
		rows = 3
	}
	return rows
}

func (m Model) View() string {
	var b strings.Builder
	cw := m.contentWidth()
	serverDot := badStyle.Render("stopped")
	if m.serving {
		serverDot = okStyle.Render("live " + m.addr)
	}
	b.WriteString(headerStyle.Render(
		titleStyle.Render("multi-codex-proxy") + " " + serverDot +
			dimStyle.Render("  " + truncate(m.status, max(10, cw-30)))) + "\n")

	if m.lastErr != "" {
		box := "ERR " + m.lastErr
		if m.lastHint != "" {
			box += "\nHint: " + m.lastHint
		}
		b.WriteString(errBoxStyle.Render(truncateLines(box, cw)) + "\n")
	}

	if len(m.state.Accounts) == 0 {
		b.WriteString(dimStyle.Render("No accounts yet. Press a to sign in.") + "\n")
	} else if m.width >= 110 {
		b.WriteString(m.landscapeView(cw))
	} else {
		b.WriteString(m.stackedView(cw))
	}

	if m.confirmDel {
		b.WriteString(warnStyle.Render("Delete selected account? y / n") + "\n")
	}
	b.WriteString(m.logsView(cw))
	b.WriteString("\n" + dimStyle.Render(m.footerKeys()))
	return b.String()
}

// stackedView fits portrait phones and normal terms: one column.
func (m Model) stackedView(cw int) string {
	var b strings.Builder
	end := min(len(m.state.Accounts), m.top+m.visibleRows())
	for i := m.top; i < end; i++ {
		a := m.state.Accounts[i]
		if m.isPortrait() {
			line := fmt.Sprintf("%s %s", statusDot(a), truncate(a.Email, cw-4))
			sub := fmt.Sprintf("  %s  %s", plainToken(a), usageLabel(a))
			if i == m.cursor {
				b.WriteString(selStyle.Render(truncate(line, cw)) + "\n")
				b.WriteString(selStyle.Render(truncate(sub, cw)) + "\n")
			} else {
				b.WriteString(truncate(line, cw) + "\n")
				b.WriteString(dimStyle.Render(truncate(sub, cw)) + "\n")
			}
		} else {
			line := fmt.Sprintf("%s  %-28s  %s  %s",
				statusDot(a), truncatePad(a.Email, 28), tokenLabel(a), usageLabel(a))
			if i == m.cursor {
				b.WriteString(selStyle.Render(truncate(line, cw)) + "\n")
			} else {
				b.WriteString(truncate(line, cw) + "\n")
			}
		}
		if i == m.cursor && a.Usage != nil {
			b.WriteString("  " + truncate(usageBar(a.Usage.Primary, "5h", 14), cw) + "\n")
			b.WriteString("  " + truncate(usageBar(a.Usage.Secondary, "wk", 14), cw) + "\n")
		}
	}
	if len(m.state.Accounts) > m.visibleRows() {
		b.WriteString(dimStyle.Render(fmt.Sprintf("-- %d/%d --", m.cursor+1, len(m.state.Accounts))) + "\n")
	}
	return b.String()
}

// landscapeView splits wide terms: list left, detail plus quota right.
func (m Model) landscapeView(cw int) string {
	leftW := cw * 3 / 5
	rightW := cw - leftW - 3
	var left strings.Builder
	end := min(len(m.state.Accounts), m.top+m.visibleRows())
	for i := m.top; i < end; i++ {
		a := m.state.Accounts[i]
		line := fmt.Sprintf("%s %-24s %s", statusDot(a), truncatePad(a.Email, 24), plainToken(a))
		if i == m.cursor {
			left.WriteString(selStyle.Render(truncate(line, leftW)) + "\n")
		} else {
			left.WriteString(truncate(line, leftW) + "\n")
		}
	}
	var right strings.Builder
	if len(m.state.Accounts) > 0 {
		a := m.state.Accounts[m.cursor]
		right.WriteString(titleStyle.Render(truncate(a.Name, rightW)) + "\n")
		right.WriteString(dimStyle.Render(truncate(a.Email, rightW)) + "\n")
		right.WriteString(tokenLabel(a) + "  " + usageLabel(a) + "\n")
		right.WriteString(truncate(usageBar(a.Usage.Primary, "5h", 16), rightW) + "\n")
		right.WriteString(truncate(usageBar(a.Usage.Secondary, "week", 16), rightW) + "\n")
		if a.Usage != nil && a.Usage.Primary != nil && a.Usage.Primary.ResetsAt != nil {
			right.WriteString(dimStyle.Render("resets "+time.Unix(*a.Usage.Primary.ResetsAt, 0).Format("01-02 15:04")) + "\n")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(left.String()),
		lipgloss.NewStyle().Width(rightW).Render(right.String()),
	) + "\n"
}

func (m Model) logsView(cw int) string {
	if len(m.logs) == 0 {
		return ""
	}
	keep := 4
	if m.height > 30 {
		keep = 6
	}
	if m.isPortrait() {
		keep = 3
	}
	start := max(0, len(m.logs)-keep)
	var b strings.Builder
	b.WriteString(dimStyle.Render("logs") + "\n")
	for _, l := range m.logs[start:] {
		b.WriteString(dimStyle.Render(truncate(l, cw)) + "\n")
	}
	return b.String()
}

func (m Model) footerKeys() string {
	if m.isPortrait() {
		return "a add  r ref  e on/off  d del  s go  q out"
	}
	return "a add   r refresh   R all   e enable   d delete   s serve   q quit"
}

func statusDot(a codex.Account) string {
	if !a.Enabled {
		return dimStyle.Render("○")
	}
	switch a.TokenStatus {
	case codex.StatusAvailable:
		if a.IsAvailable(codex.NowMillis()) {
			return okStyle.Render("●")
		}
		return warnStyle.Render("●")
	case codex.StatusInvalid, codex.StatusExpired:
		return badStyle.Render("●")
	default:
		return warnStyle.Render("●")
	}
}

func tokenLabel(a codex.Account) string {
	switch a.TokenStatus {
	case codex.StatusAvailable:
		return okStyle.Render("ready")
	case codex.StatusExpired:
		return warnStyle.Render("expired")
	case codex.StatusInvalid:
		return badStyle.Render("invalid")
	default:
		return dimStyle.Render("unknown")
	}
}

func plainToken(a codex.Account) string {
	switch a.TokenStatus {
	case codex.StatusAvailable:
		return "ready"
	case codex.StatusExpired:
		return "expired"
	case codex.StatusInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

func usageLabel(a codex.Account) string {
	if a.Usage == nil || a.Usage.Primary == nil {
		return dimStyle.Render("usage --")
	}
	rem := 100.0 - a.Usage.Primary.UsedPercent
	if rem < 0 {
		rem = 0
	}
	return fmt.Sprintf("%.0f%% left", rem)
}

func usageBar(w *codex.UsageWindow, name string, cells int) string {
	if w == nil {
		return dimStyle.Render(name + "  no data")
	}
	rem := 100.0 - w.UsedPercent
	if rem < 0 {
		rem = 0
	}
	filled := int(rem / 100 * float64(cells))
	bar := strings.Repeat("#", filled) + strings.Repeat("-", cells-filled)
	return fmt.Sprintf("%s [%s] %.0f%%", name, barFill.Render(bar), rem)
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
