package accounts

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"multi-codex-proxy/internal/codex"
)

// Repository mirrors CodexAccountRepository.kt.
// One mutex guards state + nextAccountIndex. Every mutation persists.
type Repository struct {
	mu     sync.Mutex
	store  *Store
	client *http.Client
	state  codex.State
}

func NewRepository(store *Store, client *http.Client) (*Repository, error) {
	st, err := store.Read()
	if err != nil {
		return nil, err
	}
	now := codex.NowMillis()
	for i, a := range st.Accounts {
		if a.TokenStatus != codex.StatusInvalid && a.ExpiresAt <= now {
			st.Accounts[i].TokenStatus = codex.StatusExpired
		}
	}
	r := &Repository{store: store, client: client, state: st}
	// Persist the EXPIRED relabel so TUI shows truth on boot.
	_ = store.Write(r.state)
	return r, nil
}

func (r *Repository) List() codex.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.state
	out.Accounts = append([]codex.Account(nil), r.state.Accounts...)
	return out
}

// SaveLogin mirrors saveLogin in CodexAccountRepository.kt:42.
func (r *Repository) SaveLogin(tokenJSON string) (codex.Account, error) {
	var token map[string]any
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
		return codex.Account{}, fmt.Errorf("save login: parse token: %w", err)
	}
	idToken, _ := token["id_token"].(string)
	if idToken == "" {
		return codex.Account{}, fmt.Errorf("save login: missing id token")
	}
	access, _ := token["access_token"].(string)
	if access == "" {
		return codex.Account{}, fmt.Errorf("save login: missing access token")
	}
	identity, err := codex.ParseIdentity(idToken)
	if err != nil {
		return codex.Account{}, err
	}
	expiresIn := int64(3600)
	if f, ok := token["expires_in"].(float64); ok && f > 0 {
		expiresIn = int64(f)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var existing *codex.Account
	for i := range r.state.Accounts {
		a := &r.state.Accounts[i]
		if a.ChatGPTAccount != identity.AccountID {
			continue
		}
		if a.UserID != "" && identity.UserID != "" {
			if a.UserID == identity.UserID {
				existing = a
				break
			}
			continue
		}
		if a.Email == identity.Email {
			existing = a
			break
		}
	}
	key := identity.UserID
	if key == "" {
		key = identity.Email
	}
	id := fmt.Sprintf("%s:%s", key, identity.AccountID)
	refresh, _ := token["refresh_token"].(string)
	now := codex.NowMillis()
	acct := codex.Account{
		ID:             id,
		UserID:         identity.UserID,
		Name:           identity.Name,
		Email:          identity.Email,
		ChatGPTAccount: identity.AccountID,
		AccessToken:    access,
		ExpiresAt:      now + expiresIn*1000,
		Enabled:        true,
		TokenStatus:    codex.StatusAvailable,
	}
	if existing != nil {
		acct.ID = existing.ID
		acct.Enabled = existing.Enabled
		acct.Usage = existing.Usage
		if refresh == "" {
			refresh = existing.RefreshToken
		}
	}
	if refresh == "" {
		return codex.Account{}, fmt.Errorf("save login: missing refresh token")
	}
	acct.RefreshToken = refresh
	kept := r.state.Accounts[:0]
	for _, a := range r.state.Accounts {
		if a.ID != acct.ID {
			kept = append(kept, a)
		}
	}
	r.state.Accounts = append(kept, acct)
	if err := r.store.Write(r.state); err != nil {
		return codex.Account{}, err
	}
	return acct, nil
}

// Acquire mirrors acquireAccount in CodexAccountRepository.kt:84.
// Round robin, skip unhealthy, refresh token if stale.
func (r *Repository) Acquire() (codex.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.Accounts) == 0 {
		return codex.Account{}, fmt.Errorf("no Codex account signed in (press a in TUI)")
	}
	for range len(r.state.Accounts) {
		idx := codex.SelectIndex(r.state.Accounts, r.state.NextAccountIndex, codex.NowMillis())
		if idx == nil {
			return codex.Account{}, fmt.Errorf("no available Codex account (all disabled, invalid, or at 100%%)")
		}
		candidate := r.state.Accounts[*idx]
		if !candidate.IsAvailable(codex.NowMillis()) {
			// Advance pointer past the tired account so next loop tries another.
			r.state.NextAccountIndex = (*idx + 1) % len(r.state.Accounts)
			continue
		}
		r.state.NextAccountIndex = (*idx + 1) % len(r.state.Accounts)
		fresh, err := r.ensureFreshLocked(candidate)
		if err != nil {
			continue
		}
		_ = r.store.Write(r.state)
		return fresh, nil
	}
	return codex.Account{}, fmt.Errorf("no available Codex account")
}

func (r *Repository) UpdateUsage(id string, usage *codex.UsageSnapshot) {
	if usage == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.state.Accounts {
		if r.state.Accounts[i].ID == id {
			r.state.Accounts[i].Usage = usage
			break
		}
	}
	_ = r.store.Write(r.state)
}

func (r *Repository) SetEnabled(id string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.state.Accounts {
		if r.state.Accounts[i].ID == id {
			r.state.Accounts[i].Enabled = enabled
			return r.store.Write(r.state)
		}
	}
	return fmt.Errorf("account not found: %s", id)
}

func (r *Repository) MarkInvalid(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.state.Accounts {
		if r.state.Accounts[i].ID == id {
			r.state.Accounts[i].TokenStatus = codex.StatusInvalid
			break
		}
	}
	_ = r.store.Write(r.state)
}

func (r *Repository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := []codex.Account{}
	for _, a := range r.state.Accounts {
		if a.ID != id {
			kept = append(kept, a)
		}
	}
	r.state.Accounts = kept
	r.state.NextAccountIndex = 0
	return r.store.Write(r.state)
}

// RefreshAccount mirrors refreshAccount: fresh token then usage fetch.
func (r *Repository) RefreshAccount(id string) (codex.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var acct *codex.Account
	for i := range r.state.Accounts {
		if r.state.Accounts[i].ID == id {
			acct = &r.state.Accounts[i]
			break
		}
	}
	if acct == nil {
		return codex.Account{}, fmt.Errorf("account not found: %s", id)
	}
	fresh, err := r.ensureFreshLocked(*acct)
	if err != nil {
		return codex.Account{}, err
	}
	updated, err := r.fetchUsageLocked(fresh)
	if err != nil {
		return fresh, err
	}
	return updated, nil
}

func (r *Repository) RefreshAll() {
	ids := func() []string {
		r.mu.Lock()
		defer r.mu.Unlock()
		out := make([]string, 0, len(r.state.Accounts))
		for _, a := range r.state.Accounts {
			out = append(out, a.ID)
		}
		return out
	}()
	for _, id := range ids {
		_, _ = r.RefreshAccount(id)
	}
}

// ensureFreshLocked mirrors ensureFreshLocked. Caller holds mutex.
func (r *Repository) ensureFreshLocked(a codex.Account) (codex.Account, error) {
	if a.ExpiresAt > codex.NowMillis()+codex.RefreshMargin {
		return a, nil
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {codex.ClientID},
		"refresh_token": {a.RefreshToken},
		"scope":         {codex.RefreshScopes},
	}
	req, err := http.NewRequest("POST", codex.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return codex.Account{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.client.Do(req)
	if err != nil {
		return codex.Account{}, fmt.Errorf("refresh %s: %w", a.Email, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if codex.IsRefreshAuthFailure(resp.StatusCode, string(body)) {
			r.replaceLocked(a.ID, func(x *codex.Account) {
				x.TokenStatus = codex.StatusInvalid
			})
			_ = r.store.Write(r.state)
		}
		return codex.Account{}, fmt.Errorf("token refresh failed for %s: HTTP %d", a.Email, resp.StatusCode)
	}
	var token map[string]any
	if err := json.Unmarshal(body, &token); err != nil {
		return codex.Account{}, fmt.Errorf("refresh %s: parse: %w", a.Email, err)
	}
	access, _ := token["access_token"].(string)
	if access == "" {
		return codex.Account{}, fmt.Errorf("refresh %s: missing access token", a.Email)
	}
	updated := a
	updated.AccessToken = access
	if rt, _ := token["refresh_token"].(string); rt != "" {
		updated.RefreshToken = rt
	}
	expiresIn := int64(3600)
	if f, ok := token["expires_in"].(float64); ok && f > 0 {
		expiresIn = int64(f)
	}
	updated.ExpiresAt = time.Now().UnixMilli() + expiresIn*1000
	updated.TokenStatus = codex.StatusAvailable
	r.replaceLocked(a.ID, func(x *codex.Account) { *x = updated })
	_ = r.store.Write(r.state)
	return updated, nil
}

// fetchUsageLocked mirrors fetchUsageLocked: GET wham/usage.
func (r *Repository) fetchUsageLocked(a codex.Account) (codex.Account, error) {
	req, err := http.NewRequest("GET", codex.CodexBaseURL+"/wham/usage", nil)
	if err != nil {
		return a, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("ChatGPT-Account-Id", a.ChatGPTAccount)
	req.Header.Set("originator", codex.Originator)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return a, fmt.Errorf("usage %s: %w", a.Email, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 401 {
		r.replaceLocked(a.ID, func(x *codex.Account) {
			x.TokenStatus = codex.StatusInvalid
		})
		_ = r.store.Write(r.state)
		return a, fmt.Errorf("usage %s: HTTP 401, marked INVALID", a.Email)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return a, fmt.Errorf("usage %s: HTTP %d", a.Email, resp.StatusCode)
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return a, fmt.Errorf("usage %s: parse: %w", a.Email, err)
	}
	usage := codex.ParseUsageJSON(obj)
	updated := a
	updated.TokenStatus = codex.StatusAvailable
	updated.Usage = usage
	r.replaceLocked(a.ID, func(x *codex.Account) { *x = updated })
	_ = r.store.Write(r.state)
	return updated, nil
}

func (r *Repository) replaceLocked(id string, fn func(*codex.Account)) {
	for i := range r.state.Accounts {
		if r.state.Accounts[i].ID == id {
			fn(&r.state.Accounts[i])
			return
		}
	}
}
