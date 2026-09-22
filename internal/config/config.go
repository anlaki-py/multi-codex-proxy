package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds proxy runtime settings. Secrets never live here, except the
// local API key that gates client access when set. Empty Key means open.
type Config struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Key  string `json:"key,omitempty"`
}

func Default() Config {
	return Config{Host: "127.0.0.1", Port: 18789}
}

// Dir returns $XDG_CONFIG_HOME/multi-codex-proxy or ~/.config/multi-codex-proxy.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "multi-codex-proxy"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config dir: home dir unavailable: %w", err)
	}
	return filepath.Join(home, ".config", "multi-codex-proxy"), nil
}

func filePath(dir string) string { return filepath.Join(dir, "config.json") }

// Load reads config, creates defaults on first run. Fails fast on corrupt data.
func Load() (Config, string, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Config{}, "", fmt.Errorf("config dir: create %s: %w", dir, err)
	}
	cfg := Default()
	raw, err := os.ReadFile(filePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			if err := Save(dir, cfg); err != nil {
				return Config{}, dir, err
			}
			return cfg, dir, nil
		}
		return Config{}, dir, fmt.Errorf("config: read: %w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, dir, fmt.Errorf("config: parse %s (delete it to reset): %w", filePath(dir), err)
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return Config{}, dir, fmt.Errorf("config: port %d out of range 1-65535", cfg.Port)
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	cfg.Key = strings.TrimSpace(cfg.Key)
	return cfg, dir, nil
}

// Save writes config with 0600 perms via atomic rename.
func Save(dir string, cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Rename(tmp, filePath(dir)); err != nil {
		return fmt.Errorf("config: replace: %w", err)
	}
	return nil
}
