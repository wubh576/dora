package analytics

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

const TrackingStartDate = "2026-07-01"

type TimeWindow struct {
	StartUTC time.Time
	EndUTC   time.Time
	Location *time.Location
	Range    string
}

type TokenTotals struct {
	TotalTokens              int64   `json:"totalTokens"`
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CachedInputTokens        int64   `json:"cachedInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	ReasoningOutputTokens    int64   `json:"reasoningOutputTokens"`
	ReportedTotalTokens      int64   `json:"reportedTotalTokens"`
	CacheHitRate             float64 `json:"cacheHitRate"`
	EventCount               int     `json:"eventCount"`
}

type TimelinePoint struct {
	Date                     string `json:"date"`
	TotalTokens              int64  `json:"totalTokens"`
	InputTokens              int64  `json:"inputTokens"`
	OutputTokens             int64  `json:"outputTokens"`
	CachedInputTokens        int64  `json:"cachedInputTokens"`
	CacheCreationInputTokens int64  `json:"cacheCreationInputTokens"`
	ReasoningOutputTokens    int64  `json:"reasoningOutputTokens"`
}

type BreakdownItem struct {
	Name        string `json:"name"`
	TotalTokens int64  `json:"totalTokens"`
	EventCount  int    `json:"eventCount"`
}

func NewTimeWindow(now time.Time, location *time.Location, value string) (TimeWindow, error) {
	if location == nil {
		location = time.Local
	}
	now = now.In(location)
	canonical := strings.ToUpper(strings.TrimSpace(value))
	if canonical == "" {
		canonical = "7D"
	}
	trackingStart, err := time.ParseInLocation(time.DateOnly, TrackingStartDate, location)
	if err != nil {
		return TimeWindow{}, fmt.Errorf("解析 Dora 统计起始日: %w", err)
	}

	var start time.Time
	var label string
	switch canonical {
	case "TODAY", "1D":
		start = localMidnight(now)
		label = "1D"
	case "7D":
		start = localMidnight(now).AddDate(0, 0, -6)
		label = "7D"
	case "30D":
		start = localMidnight(now).AddDate(0, 0, -29)
		label = "30D"
	case "ALL":
		start = trackingStart
		label = "ALL"
	default:
		return TimeWindow{}, fmt.Errorf("不支持的时间范围 %q", value)
	}
	if start.Before(trackingStart) {
		start = trackingStart
	}
	if start.After(now) {
		start = now
	}
	return TimeWindow{
		StartUTC: start.UTC(),
		EndUTC:   now.UTC(),
		Location: location,
		Range:    label,
	}, nil
}

func Summarize(events []domain.UsageEvent) (TokenTotals, error) {
	var totals TokenTotals
	totals.EventCount = len(events)
	for _, event := range events {
		var err error
		if totals.TotalTokens, err = checkedAdd(totals.TotalTokens, event.TotalTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.InputTokens, err = checkedAdd(totals.InputTokens, event.InputTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.OutputTokens, err = checkedAdd(totals.OutputTokens, event.OutputTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.CachedInputTokens, err = checkedAdd(totals.CachedInputTokens, event.CachedInputTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.CacheCreationInputTokens, err = checkedAdd(totals.CacheCreationInputTokens, event.CacheCreationInputTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.ReasoningOutputTokens, err = checkedAdd(totals.ReasoningOutputTokens, event.ReasoningOutputTokens); err != nil {
			return TokenTotals{}, err
		}
		if totals.ReportedTotalTokens, err = checkedAdd(totals.ReportedTotalTokens, event.ReportedTotalTokens); err != nil {
			return TokenTotals{}, err
		}
	}

	inputTotal, err := checkedAdd(totals.InputTokens, totals.CachedInputTokens)
	if err != nil {
		return TokenTotals{}, err
	}
	inputTotal, err = checkedAdd(inputTotal, totals.CacheCreationInputTokens)
	if err != nil {
		return TokenTotals{}, err
	}
	if inputTotal > 0 {
		totals.CacheHitRate = float64(totals.CachedInputTokens) / float64(inputTotal)
	}
	return totals, nil
}

func DailyTimeline(events []domain.UsageEvent, window TimeWindow) ([]TimelinePoint, error) {
	points := make(map[string]*TimelinePoint)
	for _, event := range events {
		date := event.OccurredAt.In(window.Location).Format("2006-01-02")
		point := points[date]
		if point == nil {
			point = &TimelinePoint{Date: date}
			points[date] = point
		}
		var err error
		if point.TotalTokens, err = checkedAdd(point.TotalTokens, event.TotalTokens); err != nil {
			return nil, err
		}
		if point.InputTokens, err = checkedAdd(point.InputTokens, event.InputTokens); err != nil {
			return nil, err
		}
		if point.OutputTokens, err = checkedAdd(point.OutputTokens, event.OutputTokens); err != nil {
			return nil, err
		}
		if point.CachedInputTokens, err = checkedAdd(point.CachedInputTokens, event.CachedInputTokens); err != nil {
			return nil, err
		}
		if point.CacheCreationInputTokens, err = checkedAdd(point.CacheCreationInputTokens, event.CacheCreationInputTokens); err != nil {
			return nil, err
		}
		if point.ReasoningOutputTokens, err = checkedAdd(point.ReasoningOutputTokens, event.ReasoningOutputTokens); err != nil {
			return nil, err
		}
	}

	result := make([]TimelinePoint, 0, len(points))
	for _, point := range points {
		result = append(result, *point)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date < result[j].Date
	})
	return result, nil
}

func Breakdown(events []domain.UsageEvent, dimension string) ([]BreakdownItem, error) {
	if dimension != "model" && dimension != "project" && dimension != "provider" && dimension != "provider_model" {
		return nil, errors.New("dimension 只支持 model、project、provider 或 provider_model")
	}
	items := make(map[string]*BreakdownItem)
	for _, event := range events {
		name := event.Model
		if dimension == "project" {
			name = event.Project
		} else if dimension == "provider" {
			name = event.Source
		} else if dimension == "provider_model" {
			name = event.Source + ":" + event.Model
		}
		if name == "" {
			name = "unknown"
		}
		item := items[name]
		if item == nil {
			item = &BreakdownItem{Name: name}
			items[name] = item
		}
		total, err := checkedAdd(item.TotalTokens, event.TotalTokens)
		if err != nil {
			return nil, err
		}
		item.TotalTokens = total
		item.EventCount++
	}

	result := make([]BreakdownItem, 0, len(items))
	for _, item := range items {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalTokens == result[j].TotalTokens {
			return result[i].Name < result[j].Name
		}
		return result[i].TotalTokens > result[j].TotalTokens
	})
	return result, nil
}

func localMidnight(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func checkedAdd(left, right int64) (int64, error) {
	if right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("token 汇总超出 int64")
	}
	return left + right, nil
}
