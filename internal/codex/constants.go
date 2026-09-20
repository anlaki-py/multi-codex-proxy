package codex

import "fmt"

// Mirror of CodexOAuthManager companion + CodexAccountRepository companion
// in RikkaHub Agent. Keep values identical so upstream keeps accepting us.
const (
	ClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	TokenURL       = "https://auth.openai.com/oauth/token"
	AuthorizeURL   = "https://auth.openai.com/oauth/authorize"
	DefaultScopes  = "openid profile email offline_access"
	RefreshScopes  = "openid profile email"
	CodexBaseURL   = "https://chatgpt.com/backend-api"
	CodexAPI       = "https://chatgpt.com/backend-api/codex"
	ClientVersion  = "0.144.5"
	Originator     = "codex_cli_rs"
	ResponsesPath  = "/responses"
	RefreshMargin  = int64(30_000)
)

// CallbackPorts mirrors CALLBACK_PORTS in CodexOAuthManager.kt.
var CallbackPorts = []int{1455, 1457}

// UserAgent mirrors CODEX_USER_AGENT in CodexProvider.kt.
// Format is "<originator>/<version> (<os>; <arch>)".
func UserAgent(osName, arch string) string {
	if osName == "" {
		osName = "Linux"
	}
	if arch == "" {
		arch = "x86_64"
	}
	return fmt.Sprintf("%s/%s (%s; %s)", Originator, ClientVersion, osName, arch)
}
