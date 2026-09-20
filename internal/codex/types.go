package codex

import "time"

// Mirror of CodexAccount.kt. Field names match JSON keys on disk.
type TokenStatus string

const (
	StatusUnknown   TokenStatus = "UNKNOWN"
	StatusAvailable TokenStatus = "AVAILABLE"
	StatusExpired   TokenStatus = "EXPIRED"
	StatusInvalid   TokenStatus = "INVALID"
)

type UsageWindow struct {
	UsedPercent  float64 `json:"usedPercent"`
	WindowMinutes *int64  `json:"windowMinutes,omitempty"`
	ResetsAt     *int64  `json:"resetsAt,omitempty"`
}

type UsageSnapshot struct {
	Primary   *UsageWindow `json:"primary,omitempty"`
	Secondary *UsageWindow `json:"secondary,omitempty"`
	UpdatedAt int64        `json:"updatedAt"`
}

type Account struct {
	ID              string        `json:"id"`
	UserID          string        `json:"userId"`
	Name            string        `json:"name"`
	Email           string        `json:"email"`
	ChatGPTAccount  string        `json:"chatgptAccountId"`
	AccessToken     string        `json:"accessToken"`
	RefreshToken    string        `json:"refreshToken"`
	ExpiresAt       int64         `json:"expiresAt"`
	Enabled         bool          `json:"enabled"`
	TokenStatus     TokenStatus   `json:"tokenStatus"`
	Usage           *UsageSnapshot `json:"usage,omitempty"`
}

// State mirrors CodexAccountState in CodexCredentialStore.kt.
type State struct {
	Accounts         []Account `json:"accounts"`
	NextAccountIndex int       `json:"nextAccountIndex"`
}

// IsAvailable mirrors CodexAccount.isAvailable in CodexAccount.kt:42.
// Disabled or INVALID never serve. A window at 100% blocks until reset passes.
func (a Account) IsAvailable(nowMillis int64) bool {
	if !a.Enabled || a.TokenStatus == StatusInvalid {
		return false
	}
	windows := []*UsageWindow{}
	if a.Usage != nil {
		if a.Usage.Primary != nil {
			windows = append(windows, a.Usage.Primary)
		}
		if a.Usage.Secondary != nil {
			windows = append(windows, a.Usage.Secondary)
		}
	}
	for _, w := range windows {
		if w.UsedPercent >= 100.0 && (w.ResetsAt == nil || *w.ResetsAt*1000 > nowMillis) {
			return false
		}
	}
	return true
}

func NowMillis() int64 { return time.Now().UnixMilli() }

// SelectIndex mirrors selectCodexAccountIndex in CodexAccountRepository.kt:247.
func SelectIndex(accounts []Account, startIndex int, nowMillis int64) *int {
	if len(accounts) == 0 {
		return nil
	}
	for offset := 0; offset < len(accounts); offset++ {
		idx := (startIndex + offset) % len(accounts)
		if accounts[idx].IsAvailable(nowMillis) {
			out := idx
			return &out
		}
	}
	return nil
}

// WindowName mirrors the when() in CodexProviderConfigure.kt:350.
func WindowName(w *UsageWindow, fallback string) string {
	if w == nil || w.WindowMinutes == nil {
		return fallback
	}
	switch *w.WindowMinutes {
	case 300:
		return "5 hour limit"
	case 10080:
		return "Weekly limit"
	case 43200:
		return "Monthly limit"
	default:
		return fallback
	}
}

// ReasoningEffort mirrors codexReasoningEffort in CodexProvider.kt:244.
func ReasoningEffort(level string) *string {
	mk := func(s string) *string { return &s }
	switch level {
	case "", "auto":
		return nil
	case "low":
		return mk("low")
	case "medium":
		return mk("medium")
	case "high":
		return mk("high")
	case "xhigh", "max":
		return mk("xhigh")
	case "off":
		return mk("none")
	default:
		return nil
	}
}
