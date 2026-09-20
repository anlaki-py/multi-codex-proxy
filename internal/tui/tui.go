package tui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"multi-codex-proxy/internal/accounts"
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
	barEmpty    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	headerStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// Model owns TUI state. No globals, deps passed in.
type Model struct {
	repo      *accounts.Repository
	server    *proxy.Server
	http      *http.Client
	addr      string
	state     codex.State
	cursor    int
	logs      []string
	serving   bool
	srv       *http.Server
	confirmDel bool
	status    string
	width     int
}

func New(repo *accounts.Repository, server *proxy.Server, client *http.Client, addr string) Model {
	return Model{repo: repo, server: server, http: client, addr: addr, status: "ready"}
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
	if len(m.logs) > 8 {
		m.logs = m.logs[len(m.logs)-8:]
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tickMsg:
		return m, tea.Batch(tick(), m.loadAccounts)
	case accountsMsg:
		m.state = msg.state
		if m.cursor >= len(m.state.Accounts) {
			m.cursor = max(0, len(m.state.Accounts)-1)
		}
		return m, nil
	case errMsg:
		m.status = msg.err.Error()
		m.pushLog("error: %s", msg.err.Error())
		return m, nil
	case logMsg:
		m.pushLog("%s", msg.line)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.confirmDel {
		if key == "y" || key == "Y" {
			m.confirmDel = false
			if len(m.state.Accounts) > 0 {
				id := m.state.Accounts[m.cursor].ID
				if err := m.repo.Delete(id); err != nil {
					m.status = err.Error()
				} else {
					m.status = "account deleted"
					m.pushLog("deleted %s", shortID(id))
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
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.state.Accounts)-1 {
			m.cursor++
		}
		return m, nil
	case "a":
		m.status = "browser login started"
		return m, m.doLogin()
	case "r":
		return m, m.doRefresh(false)
	case "R":
		return m, m.doRefresh(true)
	case "e":
		if len(m.state.Accounts) == 0 {
			return m, nil
		}
		a := m.state.Accounts[m.cursor]
		_ = m.repo.SetEnabled(a.ID, !a.Enabled)
		m.status = "toggled " + a.Email
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
		} else {
			m.startServer()
			m.status = "server on " + m.addr
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
			return errMsg{fmt.Errorf("no accounts yet, press a")}
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

func (m *Model) startServer() {
	if m.serving {
		return
	}
	m.srv = &http.Server{Addr: m.addr, Handler: m.server.Handler()}
	m.serving = true
	m.pushLog("server on http://%s", m.addr)
	go func(s *http.Server) {
		_ = s.ListenAndServe()
	}(m.srv)
}

func (m *Model) stopServer() {
	if m.srv != nil {
		_ = m.srv.Close()
	}
	m.serving = false
}

func (m Model) View() string {
	var b strings.Builder
	serverDot := badStyle.Render("● stopped")
	if m.serving {
		serverDot = okStyle.Render("● live")
	}
	b.WriteString(headerStyle.Render(
		titleStyle.Render("multi-codex-proxy") + "  " + serverDot +
			dimStyle.Render("   http://"+m.addr+"  |  "+m.status)) + "\n\n")

	if len(m.state.Accounts) == 0 {
		b.WriteString(dimStyle.Render("No accounts yet. Press a to sign in with ChatGPT.") + "\n\n")
	} else {
		for i, a := range m.state.Accounts {
			line := fmt.Sprintf("%s  %-28s  %s  %s",
				statusDot(a), trimEmail(a.Email, 28), tokenLabel(a), usageLabel(a))
			if i == m.cursor {
				b.WriteString(selStyle.Render(line) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
			if i == m.cursor && a.Usage != nil {
				b.WriteString("  " + usageBar(a.Usage.Primary, "5h") + "\n")
				b.WriteString("  " + usageBar(a.Usage.Secondary, "week") + "\n")
			}
		}
		b.WriteString("\n")
	}
	if m.confirmDel {
		b.WriteString(warnStyle.Render("Delete selected account? y / n") + "\n")
	}
	if len(m.logs) > 0 {
		b.WriteString(dimStyle.Render("— logs —") + "\n")
		for _, l := range m.logs {
			b.WriteString(dimStyle.Render(l) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("a add   r refresh   R all   e enable   d delete   s serve   q quit"))
	return b.String()
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

func usageLabel(a codex.Account) string {
	if a.Usage == nil || a.Usage.Primary == nil {
		return dimStyle.Render("usage —")
	}
	rem := 100.0 - a.Usage.Primary.UsedPercent
	if rem < 0 {
		rem = 0
	}
	return fmt.Sprintf("%.0f%% left", rem)
}

func usageBar(w *codex.UsageWindow, name string) string {
	if w == nil {
		return dimStyle.Render(name + "  no data")
	}
	rem := 100.0 - w.UsedPercent
	if rem < 0 {
		rem = 0
	}
	filled := int(rem / 100 * 20)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 20-filled)
	reset := ""
	if w.ResetsAt != nil {
		reset = " resets " + time.Unix(*w.ResetsAt, 0).Format("01-02 15:04")
	}
	return fmt.Sprintf("%s %s %.0f%% left%s", name, barFill.Render(bar), rem, dimStyle.Render(reset))
}

func trimEmail(s string, n int) string {
	if len(s) <= n {
		return s + strings.Repeat(" ", n-len(s))
	}
	return s[:n]
}

func shortID(id string) string {
	if len(id) > 24 {
		return id[:24]
	}
	return id
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
