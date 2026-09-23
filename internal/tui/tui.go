package tui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
type srvLogMsg struct{ line string }

// ServerLog formats one proxy server line for the log view. main sends it
// through the Bubble Tea program, so server output lands in the log store
// instead of stdout while the fullscreen TUI runs.
func ServerLog(format string, args ...any) tea.Msg {
	return srvLogMsg{line: fmt.Sprintf(format, args...)}
}

// Model owns TUI state. No globals, deps passed in.
// Rendering lives in view.go, quota display in usage.go.
type Model struct {
	repo       *accounts.Repository
	server     *proxy.Server
	http       *http.Client
	addr       string
	state      codex.State
	cursor     int
	top        int
	logs       []string
	serving    bool
	srv        *http.Server
	confirmDel bool
	status     string
	lastErr    string
	lastHint   string
	width      int
	height     int
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
	case srvLogMsg:
		// Quiet on purpose: request traffic must not wipe the error box
		// or steal the status line. It only appends to the log store.
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
			"port is busy or host is wrong; restart with --host/--port, e.g. --host 127.0.0.1 --port 18790")
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
