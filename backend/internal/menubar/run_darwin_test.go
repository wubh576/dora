//go:build darwin && cgo

package menubar

import (
	"context"
	"sync"
	"testing"
	"time"
)

type startupLoader struct {
	mu           sync.Mutex
	loadCalls    int
	runtimeCalls int
}

func (loader *startupLoader) Load(context.Context) (State, error) {
	loader.mu.Lock()
	loader.loadCalls++
	loader.mu.Unlock()
	return State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 1000}}}, nil
}

func (loader *startupLoader) LoadRuntime(context.Context) (RuntimeState, error) {
	loader.mu.Lock()
	loader.runtimeCalls++
	loader.mu.Unlock()
	return RuntimeState{}, nil
}

func (loader *startupLoader) calls() (int, int) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return loader.loadCalls, loader.runtimeCalls
}

func TestInitializeIslandUsesFirstScreenBeforeLoading(t *testing.T) {
	tests := []struct {
		name      string
		screen    ScreenMetrics
		wantWidth float64
		wantGap   float64
	}{
		{
			name: "刘海屏",
			screen: ScreenMetrics{
				Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 944},
				NotchWidth: 185,
			},
			wantWidth: 329,
			wantGap:   185,
		},
		{
			name: "普通屏",
			screen: ScreenMetrics{
				Frame: Rect{Width: 1920, Height: 1080}, Visible: Rect{Width: 1920, Height: 1055},
			},
			wantWidth: 280,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loader := &startupLoader{}
			presented := make(chan View, 8)
			controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
			defer controller.Stop()
			screens := make(chan ScreenMetrics, 1)
			screens <- test.screen

			if !initializeIsland(ctx, controller, screens) {
				t.Fatal("收到首个 screen 后未启动")
			}
			first := receiveIslandView(t, presented, func(View) bool { return true })
			if first.Today != "今日 —" || first.Layout.Frame.Width != test.wantWidth || first.Layout.CompactCenterGap != test.wantGap {
				t.Fatalf("第一份 View 未使用真实 screen: %+v", first)
			}
			loaded := receiveIslandView(t, presented, func(view View) bool { return view.Today == "今日 1K tokens" })
			if loaded.Layout.Frame.Width != test.wantWidth || loaded.Layout.CompactCenterGap != test.wantGap {
				t.Fatalf("首份数据 View 布局错误: %+v", loaded)
			}
			if loadCalls, runtimeCalls := loader.calls(); loadCalls != 1 || runtimeCalls != 1 {
				t.Fatalf("首次加载次数错误: full=%d runtime=%d", loadCalls, runtimeCalls)
			}
		})
	}
}

func TestInitializeIslandStopsBeforeScreenWithoutLoading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	loader := &startupLoader{}
	presented := make(chan View, 1)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	defer controller.Stop()
	done := make(chan bool, 1)
	go func() {
		done <- initializeIsland(ctx, controller, make(chan ScreenMetrics))
	}()
	cancel()

	select {
	case initialized := <-done:
		if initialized {
			t.Fatal("screen 到达前取消仍完成了初始化")
		}
	case <-time.After(time.Second):
		t.Fatal("screen 到达前取消未退出")
	}
	if loadCalls, runtimeCalls := loader.calls(); loadCalls != 0 || runtimeCalls != 0 {
		t.Fatalf("screen 到达前发生加载: full=%d runtime=%d", loadCalls, runtimeCalls)
	}
	select {
	case view := <-presented:
		t.Fatalf("screen 到达前发布 View: %+v", view)
	default:
	}
}

func TestRunIslandEventsKeepsLaterScreensWithoutReloading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	loader := &startupLoader{}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	events := &bridgeEvents{interaction: make(chan bridgeEvent, 4), screen: make(chan ScreenMetrics, 4)}
	events.screen <- ScreenMetrics{
		Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 944}, NotchWidth: 185,
	}
	done := make(chan struct{})
	go func() {
		runIslandEvents(ctx, controller, Config{}, events)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
		controller.Stop()
	}()

	receiveIslandView(t, presented, func(view View) bool {
		return view.Today == "今日 1K tokens" && view.Layout.Frame.Width == 329
	})
	events.screen <- ScreenMetrics{
		Frame: Rect{Width: 1920, Height: 1080}, Visible: Rect{Width: 1920, Height: 1055},
	}
	updated := receiveIslandView(t, presented, func(view View) bool { return view.Layout.Frame.Width == 280 })
	if updated.Layout.CompactCenterGap != 0 {
		t.Fatalf("普通屏后续布局保留了刘海间隙: %+v", updated.Layout)
	}
	if loadCalls, _ := loader.calls(); loadCalls != 1 {
		t.Fatalf("后续 screen 重复触发首次加载: %d", loadCalls)
	}
}

func TestRunIslandEventsPreservesQueuedEventsUntilFirstScreen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	loader := &startupLoader{}
	presented := make(chan View, 24)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	attention := make(chan AttentionSignal, 1)
	attention <- AttentionSignal{RequestID: 9, SessionID: 7}
	events := &bridgeEvents{interaction: make(chan bridgeEvent, 4), screen: make(chan ScreenMetrics, 1)}
	events.interaction <- bridgeEvent{kind: 9, value: 42}
	events.screen <- ScreenMetrics{
		Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 944}, NotchWidth: 185,
	}
	done := make(chan struct{})
	go func() {
		runIslandEvents(ctx, controller, Config{AttentionEvents: attention}, events)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
		controller.Stop()
	}()

	view := receiveIslandView(t, presented, func(view View) bool {
		return view.Mode == string(ModeAttention) && view.HighlightRequestID == 9 && view.HighlightSessionID == 7 &&
			view.OperationStatus == "当前 Codex 会话无法精确跳转"
	})
	if view.Layout.Frame.Width != 760 {
		t.Fatalf("排队事件处理后的展开布局错误: %+v", view.Layout)
	}
}

func receiveIslandView(t *testing.T, views <-chan View, match func(View) bool) View {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-views:
			if match(view) {
				return view
			}
		case <-deadline:
			t.Fatal("等待菜单栏 View 超时")
		}
	}
}
