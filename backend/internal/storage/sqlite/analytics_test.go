package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestUsageEventsInWindowUsesHalfOpenBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()
	if _, err := store.InitializedAt(ctx); err != nil {
		t.Fatalf("预读初始化状态失败: %v", err)
	}

	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	events := []domain.UsageEvent{
		analyticsEvent("before", start.Add(-time.Millisecond)),
		analyticsEvent("start", start),
		analyticsEvent("inside", end.Add(-time.Millisecond)),
		analyticsEvent("end", end),
	}
	if err := store.BeginUsageScan(ctx, "window-run", "full", start); err != nil {
		t.Fatalf("BeginUsageScan() 失败: %v", err)
	}
	if err := store.CompleteUsageScan(ctx, "window-run", end, events, nil, 1, len(events), ""); err != nil {
		t.Fatalf("CompleteUsageScan() 失败: %v", err)
	}

	result, err := store.UsageEventsInWindow(ctx, domain.CodexSource, start, end)
	if err != nil {
		t.Fatalf("UsageEventsInWindow() 失败: %v", err)
	}
	if len(result) != 2 || result[0].DedupKey != "start" || result[1].DedupKey != "inside" {
		t.Fatalf("[start, end) 查询错误: %+v", result)
	}
}

func TestReadConnectionSeesLatestCommittedGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := store.BeginUsageScan(ctx, "first-run", "full", start); err != nil {
		t.Fatalf("BeginUsageScan(first) 失败: %v", err)
	}
	first := []domain.UsageEvent{analyticsEvent("first", start)}
	if err := store.CompleteUsageScan(ctx, "first-run", start.Add(time.Second), first, nil, 1, len(first), ""); err != nil {
		t.Fatalf("CompleteUsageScan(first) 失败: %v", err)
	}
	if events, err := store.LoadUsageEvents(ctx, domain.CodexSource); err != nil || len(events) != 1 {
		t.Fatalf("首次读取 = %d, %v，期望 1", len(events), err)
	}

	if err := store.BeginUsageScan(ctx, "second-run", "incremental", start.Add(2*time.Second)); err != nil {
		t.Fatalf("BeginUsageScan(second) 失败: %v", err)
	}
	second := append(first, analyticsEvent("second", start.Add(time.Second)))
	if err := store.CompleteUsageScan(ctx, "second-run", start.Add(3*time.Second), second, nil, 1, 1, ""); err != nil {
		t.Fatalf("CompleteUsageScan(second) 失败: %v", err)
	}
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("第二次读取失败: %v", err)
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 provider 状态失败: %v", err)
	}
	if len(events) != 2 || state.StoredEvents != 2 || state.LastRunID != "second-run" {
		t.Fatalf("读取连接仍是旧 generation: events=%d state=%+v", len(events), state)
	}
}

func analyticsEvent(key string, occurredAt time.Time) domain.UsageEvent {
	return domain.UsageEvent{
		Source:      domain.CodexSource,
		DedupKey:    key,
		OccurredAt:  occurredAt,
		Model:       "gpt",
		Project:     "dora",
		InputTokens: 1,
		TotalTokens: 1,
	}
}
