package quota

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type fakeProvider struct {
	mu        sync.Mutex
	calls     int
	snapshots []domain.QuotaSnapshot
	err       error
	started   chan struct{}
	release   chan struct{}
	now       func() time.Time
}

type signalingContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *signalingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func (f *fakeProvider) Fetch(context.Context) ([]domain.QuotaSnapshot, error) {
	f.mu.Lock()
	f.calls++
	started := f.started
	release := f.release
	snapshots := append([]domain.QuotaSnapshot(nil), f.snapshots...)
	err := f.err
	now := f.now
	f.mu.Unlock()
	if now != nil {
		for index := range snapshots {
			snapshots[index].FetchedAt = now().UTC()
		}
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return snapshots, err
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRefreshRequiresConsentAndCachesAutomaticCalls(t *testing.T) {
	store, settingsStore, service, provider, now := newQuotaService(t)
	defer store.Close()

	view, err := service.Refresh(context.Background(), false)
	if err != nil {
		t.Fatalf("未授权 Refresh() 失败: %v", err)
	}
	if view.Enabled || provider.callCount() != 0 {
		t.Fatalf("未授权时访问了 provider: view=%+v calls=%d", view, provider.callCount())
	}
	if err := settingsStore.Save(settings.Values{CodexQuotaConsent: true}); err != nil {
		t.Fatalf("保存 consent 失败: %v", err)
	}

	if _, err := service.Refresh(context.Background(), false); err != nil {
		t.Fatalf("首次 Refresh() 失败: %v", err)
	}
	if _, err := service.Refresh(context.Background(), false); err != nil {
		t.Fatalf("缓存 Refresh() 失败: %v", err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("5 分钟缓存内 provider 调用 = %d，期望 1", provider.callCount())
	}
	if _, err := service.Refresh(context.Background(), true); err != nil {
		t.Fatalf("手动 Refresh() 失败: %v", err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("手动刷新未绕过时间缓存: %d", provider.callCount())
	}

	*now = now.Add(6 * time.Minute)
	if _, err := service.Refresh(context.Background(), false); err != nil {
		t.Fatalf("过期 Refresh() 失败: %v", err)
	}
	if provider.callCount() != 3 {
		t.Fatalf("缓存过期后 provider 调用 = %d，期望 3", provider.callCount())
	}
}

func TestRefreshSingleflightAndStaleFallback(t *testing.T) {
	store, settingsStore, service, provider, now := newQuotaService(t)
	defer store.Close()
	if err := settingsStore.Save(settings.Values{CodexQuotaConsent: true}); err != nil {
		t.Fatalf("保存 consent 失败: %v", err)
	}
	provider.started = make(chan struct{}, 1)
	provider.release = make(chan struct{})

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Refresh(context.Background(), true)
		firstDone <- err
	}()
	<-provider.started
	secondDone := make(chan error, 1)
	secondContext := &signalingContext{
		Context: context.Background(),
		entered: make(chan struct{}),
	}
	go func() {
		_, err := service.Refresh(secondContext, true)
		secondDone <- err
	}()
	<-secondContext.entered
	close(provider.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("首个并发 Refresh() 失败: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("第二个并发 Refresh() 失败: %v", err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("singleflight provider 调用 = %d，期望 1", provider.callCount())
	}

	provider.mu.Lock()
	provider.err = &codex.QuotaError{
		State:   "error",
		Message: "无法连接 Codex 配额服务",
		Advice:  "请检查网络后重试",
	}
	provider.started = nil
	provider.release = nil
	provider.mu.Unlock()
	*now = now.Add(11 * time.Minute)
	view, err := service.Refresh(context.Background(), true)
	if err == nil {
		t.Fatal("网络失败未返回错误")
	}
	if len(view.Items) != 2 || view.Status != "error" {
		t.Fatalf("网络失败未保留最后成功 quota: %+v", view)
	}
	for _, item := range view.Items {
		if item.SourceState != "stale" {
			t.Fatalf("超过 10 分钟未标记 stale: %+v", item)
		}
	}
}

func TestDisablePreservesHistoryButHidesValues(t *testing.T) {
	store, settingsStore, service, _, _ := newQuotaService(t)
	defer store.Close()
	if err := settingsStore.Save(settings.Values{CodexQuotaConsent: true}); err != nil {
		t.Fatalf("保存 consent 失败: %v", err)
	}
	if _, err := service.Refresh(context.Background(), true); err != nil {
		t.Fatalf("Refresh() 失败: %v", err)
	}
	if err := settingsStore.Save(settings.Values{}); err != nil {
		t.Fatalf("关闭 consent 失败: %v", err)
	}
	if err := service.Disable(context.Background()); err != nil {
		t.Fatalf("Disable() 失败: %v", err)
	}
	view, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() 失败: %v", err)
	}
	if view.Enabled || len(view.Items) != 0 {
		t.Fatalf("关闭 consent 后仍暴露 quota: %+v", view)
	}
	history, err := store.LatestQuotaSnapshots(context.Background(), domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 quota 历史失败: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("关闭 consent 删除了 quota 历史: %+v", history)
	}
}

func newQuotaService(
	t *testing.T,
) (*dorasqlite.Store, *settings.Store, *Service, *fakeProvider, *time.Time) {
	t.Helper()
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	settingsStore := settings.New(filepath.Join(t.TempDir(), "settings.json"))
	now := time.Date(2026, 7, 31, 4, 0, 0, 0, time.UTC)
	reset5h := now.Add(5 * time.Hour)
	reset7d := now.Add(7 * 24 * time.Hour)
	provider := &fakeProvider{snapshots: []domain.QuotaSnapshot{
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowFiveHour,
			Label:            "5 hours",
			UsedPercent:      20,
			RemainingPercent: 80,
			ResetsAt:         &reset5h,
			FetchedAt:        now,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
		},
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowSevenDay,
			Label:            "7 days",
			UsedPercent:      30,
			RemainingPercent: 70,
			ResetsAt:         &reset7d,
			FetchedAt:        now,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
		},
	}, now: func() time.Time { return now }}
	service := NewService(provider, store, settingsStore)
	service.now = func() time.Time { return now }
	return store, settingsStore, service, provider, &now
}
