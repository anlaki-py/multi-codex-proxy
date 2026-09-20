package accounts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"multi-codex-proxy/internal/codex"
)

// Store persists codex.State to the home .config dir.
// Mirror of CodexCredentialStore.kt, minus Android Keystore.
// File gets 0600 perms, dir 0700, atomic replace via .tmp.
type Store struct{ path string }

func NewStore(configDir string) *Store {
	return &Store{path: filepath.Join(configDir, "accounts.json")}
}

// Read returns stored state or empty state on missing file.
// Corrupt JSON returns an error so startup fails loud instead of
// silently dropping accounts.
func (s *Store) Read() (codex.State, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return codex.State{}, nil
		}
		return codex.State{}, fmt.Errorf("accounts: read: %w", err)
	}
	var st codex.State
	if err := json.Unmarshal(raw, &st); err != nil {
		return codex.State{}, fmt.Errorf("accounts: parse %s: %w", s.path, err)
	}
	if st.Accounts == nil {
		st.Accounts = []codex.Account{}
	}
	return st, nil
}

func (s *Store) Write(st codex.State) error {
	if st.Accounts == nil {
		st.Accounts = []codex.Account{}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: encode: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("accounts: write: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("accounts: chmod: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("accounts: replace: %w", err)
	}
	return nil
}
