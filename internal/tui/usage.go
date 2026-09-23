package tui

import (
	"fmt"
	"strings"
	"time"

	"multi-codex-proxy/internal/codex"
)

// Quota display lives here. One job: show remaining quota in calm words.
// Bars track what is left, not what is gone. Color stays quiet until
// quota runs low, so a full account does not shout.

// quotaLevel groups remaining percent into calm tiers.
func quotaLevel(rem float64) int {
	if rem < 10 {
		return 2
	}
	if rem < 30 {
		return 1
	}
	return 0
}

func remainingPercent(w *codex.UsageWindow) float64 {
	if w == nil {
		return 0
	}
	rem := 100.0 - w.UsedPercent
	if rem < 0 {
		rem = 0
	}
	if rem > 100 {
		rem = 100
	}
	return rem
}

// statusDot shows one dot per account. Disabled is hollow. Bad token is
// red. Quota blocked is yellow. Everything else is green.
func statusDot(a codex.Account) string {
	if !a.Enabled {
		return dimStyle.Render("○")
	}
	switch a.TokenStatus {
	case codex.StatusInvalid, codex.StatusExpired:
		return badStyle.Render("●")
	default:
		if !a.IsAvailable(codex.NowMillis()) {
			return warnStyle.Render("●")
		}
		return okStyle.Render("●")
	}
}

// tokenText names the login state. Styled true returns colored text for
// detail views. Styled false returns plain text for rows.
func tokenText(a codex.Account, styled bool) string {
	word := "unknown"
	switch a.TokenStatus {
	case codex.StatusAvailable:
		word = "ready"
	case codex.StatusExpired:
		word = "expired"
	case codex.StatusInvalid:
		word = "invalid"
	}
	if !styled {
		return word
	}
	switch a.TokenStatus {
	case codex.StatusAvailable:
		return okStyle.Render(word)
	case codex.StatusExpired:
		return warnStyle.Render(word)
	case codex.StatusInvalid:
		return badStyle.Render(word)
	default:
		return dimStyle.Render(word)
	}
}

// quotaLabel is the short per row summary. Positive framing on purpose:
// "62% left" reads calmer than "38% used".
func quotaLabel(a codex.Account) string {
	if a.Usage == nil || a.Usage.Primary == nil {
		return dimStyle.Render("quota --")
	}
	rem := remainingPercent(a.Usage.Primary)
	if quotaLevel(rem) == 2 {
		return badStyle.Render(fmt.Sprintf("%.0f%% left", rem))
	}
	if quotaLevel(rem) == 1 {
		return warnStyle.Render(fmt.Sprintf("%.0f%% left", rem))
	}
	return fmt.Sprintf("%.0f%% left", rem)
}

// quotaBar renders one window as remaining first. Filled cells mean quota
// left. Empty cells mean quota spent.
func quotaBar(w *codex.UsageWindow, name string, cells int) string {
	if w == nil {
		return dimStyle.Render(name + "  no data")
	}
	if cells < 6 {
		cells = 6
	}
	if cells > 24 {
		cells = 24
	}
	rem := remainingPercent(w)
	filled := int(rem/100*float64(cells) + 0.5)
	if filled > cells {
		filled = cells
	}
	bar := strings.Repeat("=", filled) + strings.Repeat(".", cells-filled)
	switch quotaLevel(rem) {
	case 2:
		return fmt.Sprintf("%s [%s] %.0f%% left", name, badStyle.Render(bar), rem)
	case 1:
		return fmt.Sprintf("%s [%s] %.0f%% left", name, warnStyle.Render(bar), rem)
	default:
		return fmt.Sprintf("%s [%s] %.0f%% left", name, barFill.Render(bar), rem)
	}
}

// resetText names when a window refills. Empty when unknown or past.
func resetText(w *codex.UsageWindow) string {
	if w == nil || w.ResetsAt == nil {
		return ""
	}
	t := time.Unix(*w.ResetsAt, 0)
	if time.Until(t) < 0 {
		return ""
	}
	return "resets " + t.Format("01-02 15:04")
}

// accountBlock renders one account as header plus quota bars. The email
// is the only field ever cut, and it is cut plain before styling, so
// wide screens never slice escape codes. Bars are sized to fit, with
// the reset time added only when it fits.
func accountBlock(a codex.Account, cw int, selected bool) []string {
	var lines []string
	if cw < 40 {
		emailW := cw - 2
		if emailW < 8 {
			emailW = 8
		}
		head := statusDot(a) + " " + truncate(a.Email, emailW)
		sub := "  " + tokenText(a, false) + "  " + quotaLabel(a)
		if selected {
			head = selStyle.Render(head)
		}
		lines = append(lines, head, sub)
	} else {
		emailW := cw - 21
		if emailW < 8 {
			emailW = 8
		}
		head := statusDot(a) + "  " + truncatePad(a.Email, emailW) +
			"  " + tokenText(a, true) + "  " + quotaLabel(a)
		if selected {
			head = selStyle.Render(head)
		}
		lines = append(lines, head)
	}
	lines = append(lines, barLines(a, cw)...)
	return lines
}

func barLines(a codex.Account, cw int) []string {
	if a.Usage == nil {
		return []string{dimStyle.Render("  quota no data yet")}
	}
	cells := cw - 18
	if cells < 6 {
		cells = 6
	}
	if cells > 20 {
		cells = 20
	}
	windows := []struct {
		name string
		win  *codex.UsageWindow
	}{
		{"5h", a.Usage.Primary},
		{"7d", a.Usage.Secondary},
	}
	out := make([]string, 0, 2)
	for _, w := range windows {
		line := "  " + quotaBar(w.win, w.name, cells)
		if r := resetText(w.win); r != "" && cells+16+20 <= cw {
			line += dimStyle.Render("  " + r)
		}
		out = append(out, line)
	}
	return out
}
