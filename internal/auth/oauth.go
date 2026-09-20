package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"multi-codex-proxy/internal/accounts"
	"multi-codex-proxy/internal/apperr"
	"multi-codex-proxy/internal/codex"
)

// StartLogin mirrors CodexOAuthManager.startLogin + exchangeCode.
// Spins a local server on 1455 or 1457, opens the browser,
// waits for the callback, exchanges the code, saves the account.
func StartLogin(ctx context.Context, repo *accounts.Repository, client *http.Client) (codex.Account, error) {
	state := randomURLSafe(32)
	verifier := randomURLSafe(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv, port, err := serveCallback(ctx, state, codeCh, errCh)
	if err != nil {
		return codex.Account{}, apperr.New("login", apperr.CodeAuth, err,
			"close anything on ports 1455/1457, then press a again")
	}
	defer srv.Close()
	redirect := fmt.Sprintf("http://localhost:%d/auth/callback", port)

	q := url.Values{
		"response_type":                {"code"},
		"client_id":                    {codex.ClientID},
		"redirect_uri":                 {redirect},
		"scope":                        {codex.DefaultScopes},
		"state":                        {state},
		"code_challenge":               {challenge},
		"code_challenge_method":        {"S256"},
		"id_token_add_organizations":   {"true"},
		"codex_cli_simplified_flow":    {"true"},
		"originator":                   {codex.Originator},
	}
	authURL := codex.AuthorizeURL + "?" + q.Encode()
	_ = openBrowser(authURL)
	fmt.Printf("If the browser did not open, visit:\n%s\n", authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return codex.Account{}, apperr.New("login callback", apperr.CodeAuth, err,
			"retry sign in, approve in browser, keep this terminal open")
	case <-ctx.Done():
		return codex.Account{}, apperr.New("login", apperr.CodeAuth, fmt.Errorf("cancelled"),
			"press a to start over")
	case <-time.After(5 * time.Minute):
		return codex.Account{}, apperr.New("login", apperr.CodeAuth, fmt.Errorf("timed out after 5 minutes"),
			"press a to start over, approve faster in browser")
	}

	tokenJSON, err := exchangeCode(ctx, client, code, redirect, verifier)
	if err != nil {
		return codex.Account{}, apperr.New("login exchange", apperr.CodeUpstream, err,
			"check network, then press a to retry")
	}
	acct, err := repo.SaveLogin(tokenJSON)
	if err != nil {
		return codex.Account{}, apperr.New("login save", apperr.CodeAccounts, err,
			"config dir may be unwritable; check ~/.config perms")
	}
	// Pull usage right away like the app does after OAuth success.
	_, _ = repo.RefreshAccount(acct.ID)
	return repo.RefreshAccount(acct.ID)
}

func exchangeCode(ctx context.Context, client *http.Client, code, redirect, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {codex.ClientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", codex.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token exchange: HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

func serveCallback(ctx context.Context, wantState string, codeCh chan string, errCh chan error) (*http.Server, int, error) {
	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != wantState {
			errCh <- fmt.Errorf("oauth state mismatch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Sign-in failed: state mismatch. Return to the app."))
			return
		}
		if e := q.Get("error"); e != "" {
			errCh <- fmt.Errorf("provider error: %s", e)
			_, _ = w.Write([]byte("Sign-in failed. You can close this tab."))
			return
		}
		code := q.Get("code")
		if code == "" {
			errCh <- fmt.Errorf("missing authorization code")
			_, _ = w.Write([]byte("Sign-in failed: missing code."))
			return
		}
		_, _ = w.Write([]byte("<p>Signed in. Return to the terminal.</p>"))
		select {
		case codeCh <- code:
		default:
		}
	})
	for _, port := range codex.CallbackPorts {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		go func() {
			_ = srv.Serve(ln)
		}()
		return srv, port, nil
	}
	return nil, 0, fmt.Errorf("oauth callback ports 1455 and 1457 are busy")
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
