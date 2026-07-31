package menubar

import (
	"strings"
	"testing"
	"time"
)

func TestCompactTokens(t *testing.T) {
	for _, test := range []struct {
		value int64
		want  string
	}{
		{999, "999"},
		{1_200, "1.2K"},
		{999_949, "999.9K"},
		{999_999, "1M"},
		{999_999_999, "1B"},
		{999_999_999_999, "1T"},
		{1_250_000, "1.3M"},
		{2_300_000_000, "2.3B"},
		{4_500_000_000_000, "4.5T"},
	} {
		if got := compactTokens(test.value); got != test.want {
			t.Fatalf("compactTokens(%d) = %q，期望 %q", test.value, got, test.want)
		}
	}
}

func TestBuildViewFormatsUsageAndEmptyModel(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.Local)
	lastScan := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	state := State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 12_345, SevenDayTokens: 84_500, AllTimeTokens: 1_200_000, LastScanAt: &lastScan}}}
	view := BuildView(&state, now, false, "")
	if view.Title != "12.3K" || view.Today != "今日：12.3K tokens" || view.SevenDays != "7 日：84.5K tokens" || view.AllTime != "全部：1.2M tokens" {
		t.Fatalf("用量菜单格式错误: %+v", view)
	}
	if view.TopModel != "模型：暂无数据" {
		t.Fatalf("空 top model 文案 = %q", view.TopModel)
	}
}

func TestQuotaRowsCoverNormalStaleDisabledUnauthorizedAndError(t *testing.T) {
	now := time.Date(2026, 7, 31, 16, 0, 0, 0, time.Local)
	reset := now.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	tests := []struct {
		name  string
		quota QuotaState
		want  string
	}{
		{"normal", QuotaState{Enabled: true, Status: "ready", Items: []QuotaItem{{WindowKey: "five_hour", RemainingPercent: 72, ResetsAt: &reset, SourceState: "confirmed"}}}, "剩余 72% · 18:00 重置"},
		{"stale", QuotaState{Enabled: true, Status: "error", Items: []QuotaItem{{WindowKey: "five_hour", RemainingPercent: 45, SourceState: "stale"}}}, "剩余 45%（数据过期）"},
		{"disabled", QuotaState{}, "已关闭"}, {"unauthorized", QuotaState{Enabled: true, Status: "not_configured"}, "未登录"}, {"error", QuotaState{Enabled: true, Status: "error"}, "读取失败"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := quotaRow("5 小时配额", "five_hour", test.quota, now)
			if !strings.Contains(got, test.want) {
				t.Fatalf("quotaRow() = %q，期望包含 %q", got, test.want)
			}
		})
	}
}

func TestBuildViewMarksRefreshInProgress(t *testing.T) {
	view := BuildView(nil, time.Now(), true, "")
	if !view.Refreshing || view.Status != "状态：正在刷新数据…" {
		t.Fatalf("刷新状态错误: %+v", view)
	}
}
