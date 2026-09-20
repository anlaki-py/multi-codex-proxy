package codex

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func mkAccount(id string, enabled bool, status TokenStatus, used float64, hasUsage bool) Account {
	a := Account{ID: id, Name: id, ChatGPTAccount: "ws-" + id, AccessToken: "t", RefreshToken: "r", ExpiresAt: 1 << 60, Enabled: enabled, TokenStatus: status}
	if hasUsage {
		a.Usage = &UsageSnapshot{Primary: &UsageWindow{UsedPercent: used, ResetsAt: ptrInt64(2000000000)}}
	}
	return a
}

func ptrInt64(v int64) *int64 { return &v }

func TestSelectSkipsUnhealthy(t *testing.T) {
	accounts := []Account{
		mkAccount("disabled", false, StatusAvailable, 0, false),
		mkAccount("invalid", true, StatusInvalid, 0, false),
		mkAccount("tired", true, StatusAvailable, 100, true),
		mkAccount("good", true, StatusAvailable, 10, true),
	}
	idx := SelectIndex(accounts, 0, 1000)
	if idx == nil || *idx != 3 {
		t.Fatalf("want 3 got %v", idx)
	}
	if SelectIndex(accounts[:3], 0, 1000) != nil {
		t.Fatal("want nil when none healthy")
	}
}

func TestUsageHeadersResetAfter(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "50")
	h.Set("x-codex-primary-reset-after-seconds", "60")
	u := ParseUsageHeaders(h)
	if u == nil || u.Primary == nil || u.Primary.UsedPercent != 50 {
		t.Fatalf("bad parse %+v", u)
	}
	if u.Primary.ResetsAt == nil {
		t.Fatal("want resetsAt set")
	}
}

func TestUsageJSONWindows(t *testing.T) {
	obj := map[string]any{"rate_limit": map[string]any{
		"primary_window":   map[string]any{"used_percent": 25.5, "limit_window_seconds": float64(18000), "reset_at": float64(2000000000)},
		"secondary_window": map[string]any{"used_percent": 70.0, "limit_window_seconds": float64(604800)},
	}}
	u := ParseUsageJSON(obj)
	if u.Primary == nil || u.Primary.WindowMinutes == nil || *u.Primary.WindowMinutes != 300 {
		t.Fatalf("primary %+v", u.Primary)
	}
	if u.Secondary == nil || u.Secondary.WindowMinutes == nil || *u.Secondary.WindowMinutes != 10080 {
		t.Fatalf("secondary %+v", u.Secondary)
	}
}

func TestIdentity(t *testing.T) {
	payload := `{"sub":"user-1","email":"user@example.com","name":"Test","https://api.openai.com/auth":{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}}`
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	id, err := ParseIdentity(token)
	if err != nil {
		t.Fatal(err)
	}
	if id.AccountID != "acc-1" || id.Email != "user@example.com" {
		t.Fatalf("%+v", id)
	}
}

func TestReasoning(t *testing.T) {
	if ReasoningEffort("auto") != nil {
		t.Fatal("auto must omit")
	}
	if got := ReasoningEffort("high"); got == nil || *got != "high" {
		t.Fatalf("high %+v", got)
	}
	if got := ReasoningEffort("max"); got == nil || *got != "xhigh" {
		t.Fatalf("max %+v", got)
	}
}

func TestRefreshFailure(t *testing.T) {
	if !IsRefreshAuthFailure(401, "") {
		t.Fatal("401 must retire")
	}
	if !IsRefreshAuthFailure(400, `{"error":"invalid_grant"}`) {
		t.Fatal("invalid_grant must retire")
	}
	if IsRefreshAuthFailure(500, `{"error":"server_error"}`) {
		t.Fatal("500 must not retire")
	}
}
