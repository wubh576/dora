package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestQuotaSuccessAndFailurePreserveLastSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	fetchedAt := time.Date(2026, 7, 31, 4, 0, 0, 0, time.UTC)
	reset := fetchedAt.Add(5 * time.Hour)
	snapshots := []domain.QuotaSnapshot{
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowFiveHour,
			Label:            "5 hours",
			UsedPercent:      42,
			RemainingPercent: 58,
			ResetsAt:         &reset,
			FetchedAt:        fetchedAt,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
			Plan:             "pro",
			AccountLabel:     "Account 12ab34cd",
		},
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowSevenDay,
			Label:            "7 days",
			UsedPercent:      10,
			RemainingPercent: 90,
			FetchedAt:        fetchedAt,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
		},
	}
	if err := store.SaveQuotaSuccess(ctx, snapshots); err != nil {
		t.Fatalf("SaveQuotaSuccess() 失败: %v", err)
	}
	if err := store.SetQuotaStatus(ctx, domain.CodexSource, "error", "network unavailable"); err != nil {
		t.Fatalf("SetQuotaStatus() 失败: %v", err)
	}

	latest, err := store.LatestQuotaSnapshots(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("LatestQuotaSnapshots() 失败: %v", err)
	}
	state, err := store.QuotaProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("QuotaProviderState() 失败: %v", err)
	}
	if len(latest) != 2 || latest[0].FetchedAt != fetchedAt {
		t.Fatalf("失败后 quota 快照被覆盖: %+v", latest)
	}
	if state.Status != "error" || state.LastQuotaAt == nil || !state.LastQuotaAt.Equal(fetchedAt) {
		t.Fatalf("quota provider 状态错误: %+v", state)
	}
}

func TestQuotaRepositoryRejectsInvalidPercent(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	err = store.SaveQuotaSuccess(context.Background(), []domain.QuotaSnapshot{{
		Provider:         domain.CodexSource,
		WindowKey:        domain.QuotaWindowFiveHour,
		Label:            "5 hours",
		UsedPercent:      101,
		RemainingPercent: 0,
		FetchedAt:        time.Now(),
	}})
	if err == nil {
		t.Fatal("repository 接受了无效 quota 百分比")
	}
}
