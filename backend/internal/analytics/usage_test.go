package analytics

import (
	"math"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestTimeWindowsUseLocalCalendarBoundaries(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	now := time.Date(2026, 7, 31, 15, 30, 0, 0, location)

	tests := []struct {
		value     string
		startDate string
		label     string
	}{
		{value: "1D", startDate: "2026-07-31", label: "1D"},
		{value: "Today", startDate: "2026-07-31", label: "1D"},
		{value: "7D", startDate: "2026-07-25", label: "7D"},
		{value: "30D", startDate: "2026-07-02", label: "30D"},
		{value: "ALL", startDate: TrackingStartDate, label: "ALL"},
	}
	for _, test := range tests {
		window, err := NewTimeWindow(now, location, test.value)
		if err != nil {
			t.Fatalf("NewTimeWindow(%s) 失败: %v", test.value, err)
		}
		expectedTime := test.startDate + " 00:00"
		if test.startDate < TrackingStartDate {
			expectedTime = TrackingStartDate + " 00:00"
		}
		if got := window.StartUTC.In(location).Format("2006-01-02 15:04"); got != expectedTime {
			t.Fatalf("%s start = %s", test.value, got)
		}
		if window.Range != test.label || !window.EndUTC.Equal(now.UTC()) {
			t.Fatalf("%s window 不正确: %+v", test.value, window)
		}
	}
}

func TestTimeWindowStartsAtConfiguredTrackingDate(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	window, err := NewTimeWindow(now, time.UTC, "ALL")
	if err != nil {
		t.Fatalf("NewTimeWindow() 失败: %v", err)
	}
	if got := window.StartUTC.Format(time.DateOnly); got != TrackingStartDate {
		t.Fatalf("ALL 起始日 = %s，期望 %s", got, TrackingStartDate)
	}
}

func TestTimeWindowHandlesDSTByCalendarDay(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载 DST 时区失败: %v", err)
	}
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, location)
	window, err := NewTimeWindow(now, location, "7D")
	if err != nil {
		t.Fatalf("NewTimeWindow() 失败: %v", err)
	}
	if got := window.StartUTC.In(location).Format("2006-01-02 15:04 MST"); got != "2027-03-09 00:00 EST" {
		t.Fatalf("DST 窗口起点错误: %s", got)
	}
	if duration := window.EndUTC.Sub(window.StartUTC); duration != 155*time.Hour {
		t.Fatalf("DST 窗口错误地按固定 24 小时计算: %s", duration)
	}
}

func TestDailyTimelineDoesNotLoseOrDuplicateDSTEvents(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载 DST 时区失败: %v", err)
	}
	tests := []struct {
		name   string
		now    time.Time
		events []domain.UsageEvent
		dates  []string
		totals []int64
	}{
		{
			name: "spring forward",
			now:  time.Date(2027, 3, 15, 12, 0, 0, 0, location),
			events: []domain.UsageEvent{
				{OccurredAt: time.Date(2027, 3, 14, 6, 30, 0, 0, time.UTC), TotalTokens: 10},
				{OccurredAt: time.Date(2027, 3, 14, 7, 30, 0, 0, time.UTC), TotalTokens: 20},
				{OccurredAt: time.Date(2027, 3, 15, 4, 15, 0, 0, time.UTC), TotalTokens: 30},
			},
			dates:  []string{"2027-03-14", "2027-03-15"},
			totals: []int64{30, 30},
		},
		{
			name: "fall back",
			now:  time.Date(2026, 11, 2, 12, 0, 0, 0, location),
			events: []domain.UsageEvent{
				{OccurredAt: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), TotalTokens: 11},
				{OccurredAt: time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), TotalTokens: 22},
				{OccurredAt: time.Date(2026, 11, 2, 5, 15, 0, 0, time.UTC), TotalTokens: 33},
			},
			dates:  []string{"2026-11-01", "2026-11-02"},
			totals: []int64{33, 33},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window, err := NewTimeWindow(test.now, location, "7D")
			if err != nil {
				t.Fatalf("NewTimeWindow() 失败: %v", err)
			}
			points, err := DailyTimeline(test.events, window)
			if err != nil {
				t.Fatalf("DailyTimeline() 失败: %v", err)
			}
			if len(points) != len(test.dates) {
				t.Fatalf("timeline 点数 = %d，期望 %d: %+v", len(points), len(test.dates), points)
			}
			for index, point := range points {
				if point.Date != test.dates[index] || point.TotalTokens != test.totals[index] {
					t.Fatalf("timeline[%d] = %+v，期望 %s/%d", index, point, test.dates[index], test.totals[index])
				}
			}
		})
	}
}

func TestSummaryEqualsTimelineAndPreservesReportedTotal(t *testing.T) {
	location := time.UTC
	window, err := NewTimeWindow(time.Date(2026, 7, 31, 12, 0, 0, 0, location), location, "7D")
	if err != nil {
		t.Fatalf("NewTimeWindow() 失败: %v", err)
	}
	events := []domain.UsageEvent{
		{
			OccurredAt:               time.Date(2026, 7, 30, 3, 0, 0, 0, location),
			InputTokens:              60,
			CachedInputTokens:        30,
			CacheCreationInputTokens: 10,
			OutputTokens:             15,
			ReasoningOutputTokens:    5,
			ReportedTotalTokens:      120,
			TotalTokens:              120,
		},
		{
			OccurredAt:          time.Date(2026, 7, 31, 3, 0, 0, 0, location),
			ReportedTotalTokens: 42,
			TotalTokens:         42,
		},
	}
	summary, err := Summarize(events)
	if err != nil {
		t.Fatalf("Summarize() 失败: %v", err)
	}
	if summary.TotalTokens != 162 || summary.ReportedTotalTokens != 162 {
		t.Fatalf("reported total 未保真: %+v", summary)
	}
	if math.Abs(summary.CacheHitRate-0.3) > 0.000001 {
		t.Fatalf("cache hit rate = %f，期望 0.3", summary.CacheHitRate)
	}

	timeline, err := DailyTimeline(events, window)
	if err != nil {
		t.Fatalf("DailyTimeline() 失败: %v", err)
	}
	var timelineTotal int64
	for _, point := range timeline {
		timelineTotal += point.TotalTokens
	}
	if timelineTotal != summary.TotalTokens {
		t.Fatalf("timeline total = %d，summary total = %d", timelineTotal, summary.TotalTokens)
	}
}

func TestBreakdownSortsByTotalThenName(t *testing.T) {
	events := []domain.UsageEvent{
		{Model: "gpt-b", Project: "beta", TotalTokens: 10},
		{Model: "gpt-a", Project: "alpha", TotalTokens: 10},
		{Model: "gpt-b", Project: "alpha", TotalTokens: 5},
	}
	models, err := Breakdown(events, "model")
	if err != nil {
		t.Fatalf("Breakdown(model) 失败: %v", err)
	}
	if len(models) != 2 || models[0].Name != "gpt-b" || models[0].TotalTokens != 15 {
		t.Fatalf("模型分布错误: %+v", models)
	}
	projects, err := Breakdown(events, "project")
	if err != nil {
		t.Fatalf("Breakdown(project) 失败: %v", err)
	}
	if len(projects) != 2 || projects[0].Name != "alpha" || projects[0].TotalTokens != 15 {
		t.Fatalf("项目分布错误: %+v", projects)
	}
}

func TestRejectsUnsupportedRangeAndDimension(t *testing.T) {
	if _, err := NewTimeWindow(time.Now(), time.UTC, "1Y"); err == nil {
		t.Fatal("不支持的 range 未报错")
	}
	if _, err := Breakdown(nil, "source"); err == nil {
		t.Fatal("不支持的 dimension 未报错")
	}
}
