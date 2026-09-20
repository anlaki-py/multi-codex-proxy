package tui

import (
	"errors"
	"strings"
	"testing"

	"multi-codex-proxy/internal/apperr"
	"multi-codex-proxy/internal/codex"
)

func testModel(w, h int) Model {
	m := New(nil, nil, nil, "127.0.0.1:18789")
	m.width, m.height = w, h
	rem := 12.0
	m.state = codex.State{Accounts: []codex.Account{
		{ID: "u:a", Name: "T", Email: "long.email.address@example.com",
			ChatGPTAccount: "ws", Enabled: true, TokenStatus: codex.StatusAvailable,
			Usage: &codex.UsageSnapshot{Primary: &codex.UsageWindow{UsedPercent: 100 - rem}}},
	}}
	return m
}

func TestPortraitRenders(t *testing.T) {
	m := testModel(50, 30)
	out := m.View()
	if out == "" {
		t.Fatal("empty portrait view")
	}
	if !strings.Contains(out, "a add") {
		t.Fatal("portrait footer keys missing")
	}
}

func TestLandscapeRenders(t *testing.T) {
	m := testModel(120, 30)
	out := m.View()
	if out == "" {
		t.Fatal("empty landscape view")
	}
}

func TestTinyTerminalRenders(t *testing.T) {
	m := testModel(40, 12)
	m.setError(errTest())
	out := m.View()
	if !strings.Contains(out, "Hint:") {
		t.Fatal("error hint missing on tiny screen")
	}
}

func errTest() error {
	return apperr.New("serve", apperr.CodeConfig, errors.New("boom"), "try --port 18790")
}
