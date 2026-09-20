package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Identity mirrors CodexIdentity in CodexJson.kt.
type Identity struct {
	AccountID string
	UserID    string
	Email     string
	Name      string
}

// ParseIdentity mirrors parseCodexIdentity in CodexJson.kt:20.
// It decodes the JWT payload without verifying the signature,
// exactly like the app does, to learn account id + email.
func ParseIdentity(idToken string) (Identity, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("identity: token must have 3 parts")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("identity: decode payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, fmt.Errorf("identity: parse claims: %w", err)
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	str := func(m map[string]any, key string) string {
		if m == nil {
			return ""
		}
		s, _ := m[key].(string)
		return s
	}
	accountID := str(auth, "chatgpt_account_id")
	if accountID == "" {
		return Identity{}, fmt.Errorf("identity: missing chatgpt account id")
	}
	userID := str(auth, "chatgpt_user_id")
	if userID == "" {
		userID, _ = claims["sub"].(string)
	}
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)
	if name == "" {
		name = email
	}
	if name == "" {
		name = "OpenAI"
	}
	return Identity{AccountID: accountID, UserID: userID, Email: email, Name: name}, nil
}

// IsRefreshAuthFailure mirrors isCodexRefreshAuthenticationFailure.
// Only 401 or 400 invalid_grant / invalid_token retire an account.
func IsRefreshAuthFailure(status int, body string) bool {
	if status == 401 {
		return true
	}
	if status != 400 {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return false
	}
	code, _ := obj["error"].(string)
	return code == "invalid_grant" || code == "invalid_token"
}
