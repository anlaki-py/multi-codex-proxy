package codex

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseUsageJSON mirrors parseCodexUsage(JsonObject) in CodexJson.kt:39.
// Reads rate_limit.primary_window + secondary_window.
func ParseUsageJSON(obj map[string]any) *UsageSnapshot {
	rl, _ := obj["rate_limit"].(map[string]any)
	if rl == nil {
		return &UsageSnapshot{UpdatedAt: time.Now().UnixMilli()}
	}
	return &UsageSnapshot{
		Primary:   jsonWindow(rl["primary_window"]),
		Secondary: jsonWindow(rl["secondary_window"]),
		UpdatedAt: time.Now().UnixMilli(),
	}
}

func jsonWindow(v any) *UsageWindow {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	used, ok := toFloat(m["used_percent"])
	if !ok {
		return nil
	}
	var minutes *int64
	if secs, ok := toInt(m["limit_window_seconds"]); ok {
		if secs <= 0 {
			return nil
		}
		mm := secs / 60
		minutes = &mm
	}
	var resets *int64
	if r, ok := toInt(m["reset_at"]); ok {
		resets = &r
	}
	return &UsageWindow{UsedPercent: used, WindowMinutes: minutes, ResetsAt: resets}
}

// ParseUsageHeaders mirrors parseCodexUsage(Headers) in CodexJson.kt:47.
// Every model reply carries x-codex-{primary,secondary}-used-percent etc.
func ParseUsageHeaders(h http.Header) *UsageSnapshot {
	primary := headerWindow(h, "primary")
	secondary := headerWindow(h, "secondary")
	if primary == nil && secondary == nil {
		return nil
	}
	return &UsageSnapshot{Primary: primary, Secondary: secondary, UpdatedAt: time.Now().UnixMilli()}
}

func headerWindow(h http.Header, prefix string) *UsageWindow {
	usedRaw := h.Get("x-codex-" + prefix + "-used-percent")
	if usedRaw == "" {
		return nil
	}
	used, err := strconv.ParseFloat(strings.TrimSpace(usedRaw), 64)
	if err != nil {
		return nil
	}
	var minutes *int64
	if raw := strings.TrimSpace(h.Get("x-codex-" + prefix + "-window-minutes")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if v <= 0 {
				return nil
			}
			minutes = &v
		}
	}
	var resets *int64
	if raw := strings.TrimSpace(h.Get("x-codex-" + prefix + "-reset-at")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			resets = &v
		}
	} else if raw := strings.TrimSpace(h.Get("x-codex-" + prefix + "-reset-after-seconds")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			r := time.Now().Unix() + v
			resets = &r
		}
	}
	return &UsageWindow{UsedPercent: used, WindowMinutes: minutes, ResetsAt: resets}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}
