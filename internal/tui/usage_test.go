package tui

import (
	"strings"
	"testing"
	"time"

	"multi-codex-proxy/internal/codex"
)

func window(used float64, resets *int64) *codex.UsageWindow {
	return &codex.UsageWindow{UsedPercent: used, ResetsAt: resets}
}

func TestQuotaTiers(t *testing.T) {
	if quotaLevel(80) != 0 || quotaLevel(20) != 1 || quotaLevel(5) != 2 {
		t.Fatal("quota tiers wrong")
	}
	full := window(20, nil)
	low := window(85, nil)
	out := window(96, nil)
	for _, w := range []*codex.UsageWindow{full, low, out} {
		bar := quotaBar(w, "5h", 10)
		if !strings.Contains(bar, "% left") {
			t.Fatalf("bar missing remaining: %s", bar)
		}
	}
}

func TestQuotaBarClampsCells(t *testing.T) {
	w := window(40, nil)
	small := quotaBar(w, "5h", 2)
	wide := quotaBar(w, "5h", 100)
	if !strings.Contains(small, "[") || !strings.Contains(wide, "[") {
		t.Fatal("bar brackets missing")
	}
	if quotaBar(nil, "5h", 10) == "" {
		t.Fatal("nil window bar empty")
	}
}

func TestResetTextPastIsEmpty(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	if resetText(window(10, &past)) != "" {
		t.Fatal("past reset should be empty")
	}
	future := time.Now().Add(time.Hour).Unix()
	if resetText(window(10, &future)) == "" {
		t.Fatal("future reset missing")
	}
	if resetText(nil) != "" {
		t.Fatal("nil reset should be empty")
	}
}

func TestQuotaLabelNoData(t *testing.T) {
	a := codex.Account{}
	if !strings.Contains(quotaLabel(a), "quota --") {
		t.Fatalf("no data label: %s", quotaLabel(a))
	}
}
