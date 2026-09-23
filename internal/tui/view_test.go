package tui

import (
	"strings"
	"testing"

	"multi-codex-proxy/internal/codex"
)

func twoAccounts() codex.State {
	mk := func(email string, used float64) codex.Account {
		return codex.Account{ID: "u:" + email, Name: "T", Email: email,
			ChatGPTAccount: "ws", Enabled: true, TokenStatus: codex.StatusAvailable,
			Usage: &codex.UsageSnapshot{Primary: &codex.UsageWindow{UsedPercent: used}}}
	}
	return codex.State{Accounts: []codex.Account{
		mk("first@example.com", 20),
		mk("second@example.com", 60),
	}}
}

func TestFooterAdapts(t *testing.T) {
	small := testModel(50, 30).footerKeys()
	mid := testModel(70, 30).footerKeys()
	full := testModel(120, 30).footerKeys()
	if !strings.Contains(small, "a add") {
		t.Fatalf("small footer: %s", small)
	}
	if !strings.Contains(mid, "refresh") {
		t.Fatalf("mid footer: %s", mid)
	}
	if !strings.Contains(full, "R all") {
		t.Fatalf("full footer: %s", full)
	}
}

func TestTinyWidthStaysOnScreen(t *testing.T) {
	m := testModel(10, 10)
	if m.contentWidth() > m.width {
		t.Fatal("content wider than screen")
	}
	if m.visibleRows() < 1 {
		t.Fatal("rows below one")
	}
	if m.View() == "" {
		t.Fatal("empty tiny view")
	}
}

func TestEmptyStateHintsAdd(t *testing.T) {
	m := New(nil, nil, nil, "127.0.0.1:18789")
	m.width, m.height = 80, 24
	if !strings.Contains(m.View(), "Press a") {
		t.Fatal("empty hint missing")
	}
}

func TestServerLogKeepsErrorVisible(t *testing.T) {
	m := testModel(80, 24)
	m.setError(errTest())
	before := len(m.logs)
	updated, _ := m.Update(ServerLog("responses model=%s account=%s stream=%v", "m", "a@b.c", true))
	got := updated.(Model)
	if got.lastErr == "" {
		t.Fatal("server traffic wiped the error box")
	}
	if len(got.logs) != before+1 {
		t.Fatal("server line missing from logs")
	}
	if !strings.Contains(got.View(), "Hint:") {
		t.Fatal("error hint lost after server log")
	}
}

func TestDetailShowsBothWindows(t *testing.T) {
	m := testModel(120, 30)
	out := m.View()
	if !strings.Contains(out, "5h") || !strings.Contains(out, "7d") {
		t.Fatalf("both quota windows missing: %s", out)
	}
}

func TestWideViewShowsAllDetailsUnselected(t *testing.T) {
	m := New(nil, nil, nil, "127.0.0.1:18789")
	m.width, m.height = 150, 40
	m.state = twoAccounts()
	m.cursor = 0
	out := m.View()
	if strings.Count(out, "5h") < 2 {
		t.Fatalf("each account must show its own bars: %s", out)
	}
	if !strings.Contains(out, "first@example.com") || !strings.Contains(out, "second@example.com") {
		t.Fatalf("both emails must show: %s", out)
	}
	if strings.Contains(out, "lef…") {
		t.Fatalf("quota words must not be cut on wide screens: %s", out)
	}
}
