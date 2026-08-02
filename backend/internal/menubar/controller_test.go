package menubar

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRefreshIsAsyncAndNonConcurrent(t *testing.T) {
	loader := &fakeLoader{state: stateWithToday(20)}
	refresher := &blockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	views := make(chan View, 4)
	controller := NewController(loader, refresher, "http://127.0.0.1:9090", func(view View) { views <- view })
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("首次刷新未启动")
	}
	select {
	case <-refresher.started:
	case <-time.After(time.Second):
		t.Fatal("异步刷新未启动")
	}
	if controller.RefreshAsync(context.Background()) {
		t.Fatal("刷新过程中启动了第二次刷新")
	}
	if first := <-views; !first.Refreshing {
		t.Fatalf("首个 view 未禁用刷新: %+v", first)
	}
	close(refresher.release)
	waitForView(t, views, func(view View) bool { return view.Status == "状态：刷新完成" })
	if refresher.calls != 1 {
		t.Fatalf("Refresh() 调用 %d 次，期望 1 次", refresher.calls)
	}
}

func TestRefreshViewCannotBeOverwrittenByOlderLoad(t *testing.T) {
	loader := &fakeLoader{state: stateWithToday(20)}
	refresher := &blockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	presentStarted := make(chan struct{})
	releasePresent := make(chan struct{})
	views := make(chan View, 4)
	var presentOnce sync.Once
	controller := NewController(loader, refresher, "http://127.0.0.1:9090", func(view View) {
		presentOnce.Do(func() {
			close(presentStarted)
			<-releasePresent
		})
		views <- view
	})
	controller.LoadAsync(context.Background())
	select {
	case <-presentStarted:
	case <-time.After(time.Second):
		t.Fatal("旧状态未进入 presenter")
	}
	refreshReturned := make(chan bool, 1)
	go func() { refreshReturned <- controller.RefreshAsync(context.Background()) }()
	close(releasePresent)
	if !<-refreshReturned {
		t.Fatal("刷新未启动")
	}
	select {
	case <-refresher.started:
	case <-time.After(time.Second):
		t.Fatal("异步刷新未进入 refresher")
	}
	first, second := <-views, <-views
	if first.Refreshing || !second.Refreshing {
		t.Fatalf("菜单视图顺序错误: first=%+v second=%+v", first, second)
	}
	close(refresher.release)
	waitForView(t, views, func(view View) bool { return view.Status == "状态：刷新完成" })
}

func TestPresentStatusKeepsRefreshDisabled(t *testing.T) {
	refresher := &blockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	views := make(chan View, 4)
	controller := NewController(&fakeLoader{}, refresher, "http://127.0.0.1:9090", func(view View) { views <- view })
	controller.RefreshAsync(context.Background())
	select {
	case <-refresher.started:
	case <-time.After(time.Second):
		t.Fatal("异步刷新未进入 refresher")
	}
	<-views
	controller.PresentStatus("无法打开仪表盘")
	view := <-views
	if !view.Refreshing || view.Status != "状态：无法打开仪表盘" {
		t.Fatalf("刷新期间的操作状态错误: %+v", view)
	}
	close(refresher.release)
	waitForView(t, views, func(view View) bool { return view.Status == "状态：刷新完成" })
}

func TestQuotaFailureKeepsNewTokenState(t *testing.T) {
	views := make(chan View, 4)
	controller := NewController(&fakeLoader{state: stateWithToday(250)}, staticRefresher{quotaErr: errors.New("quota unavailable")}, "http://127.0.0.1:9090", func(view View) { views <- view })
	controller.RefreshAsync(context.Background())
	view := waitForView(t, views, func(view View) bool { return view.Status == "状态：token 已更新，配额刷新失败" })
	if view.Today != "今日：250 tokens" {
		t.Fatalf("配额失败后 token 未保留: %+v", view)
	}
}

func TestLoadFailureKeepsLastSuccessfulState(t *testing.T) {
	loader := &fakeLoader{state: stateWithToday(400)}
	views := make(chan View, 4)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:9090", func(view View) { views <- view })
	controller.LoadAsync(context.Background())
	if view := waitForView(t, views, func(View) bool { return true }); view.Today != "今日：400 tokens" {
		t.Fatalf("首次状态错误: %+v", view)
	}
	loader.mu.Lock()
	loader.err = errors.New("offline")
	loader.mu.Unlock()
	controller.LoadAsync(context.Background())
	view := waitForView(t, views, func(View) bool { return true })
	if view.Today != "今日：400 tokens" || view.Status != "状态：连接本地服务失败" {
		t.Fatalf("加载失败未保留旧状态: %+v", view)
	}
}

func TestOpenDashboardUsesActualAddressAsArgument(t *testing.T) {
	runner := &recordingRunner{}
	controller := NewController(&fakeLoader{}, staticRefresher{}, "http://127.0.0.1:49152", func(View) {})
	controller.runner = runner
	if err := controller.OpenDashboard(); err != nil {
		t.Fatalf("OpenDashboard() 失败: %v", err)
	}
	if runner.name != "open" || len(runner.args) != 1 || runner.args[0] != "http://127.0.0.1:49152" {
		t.Fatalf("浏览器命令参数错误: %q %+v", runner.name, runner.args)
	}
}

func TestAttentionLoadsIndependentlyAndJumpsBySessionID(t *testing.T) {
	loader := &fakeLoader{state: State{Attention: AttentionState{
		WaitingCount: 1,
		Sessions:     []AttentionSession{{ID: 42, Surface: "codex_app", CWDBasename: "dora", Summary: "Codex 等待授权", RequestCount: 1}},
	}}}
	views := make(chan View, 2)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	jumper := &recordingJumper{ids: make(chan int64, 1)}
	controller.SetSessionJumper(jumper)
	if !controller.LoadAttentionAsync(context.Background()) {
		t.Fatal("独立 attention 加载未启动")
	}
	view := waitForView(t, views, func(view View) bool { return len(view.Waiting) == 1 })
	if view.Title != "🔴 1" || view.Waiting[0].SessionID != 42 {
		t.Fatalf("独立 attention view 错误: %+v", view)
	}
	if !controller.JumpAttentionSessionAsync(context.Background(), 42) {
		t.Fatal("JumpAttentionSessionAsync() 未启动")
	}
	select {
	case id := <-jumper.ids:
		if id != 42 {
			t.Fatalf("跳转 session ID = %d", id)
		}
	case <-time.After(time.Second):
		t.Fatal("等待异步跳转超时")
	}
}

func TestFullLoadNeverOverwritesIndependentAttention(t *testing.T) {
	loader := &splitLoader{
		state: State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 500}}},
		attention: AttentionState{WaitingCount: 1, Sessions: []AttentionSession{
			{ID: 9, Surface: "codex_app", Summary: "Codex 等待授权", RequestCount: 1},
		}},
	}
	views := make(chan View, 4)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	controller.LoadAttentionAsync(context.Background())
	waitForView(t, views, func(view View) bool { return len(view.Waiting) == 1 })
	controller.LoadAsync(context.Background())
	view := waitForView(t, views, func(view View) bool { return view.Today == "今日：500 tokens" })
	if len(view.Waiting) != 1 || view.Waiting[0].SessionID != 9 {
		t.Fatalf("完整 Load 覆盖了独立 attention: %+v", view)
	}
}

func TestStaleFullLoadViewCannotPublishAfterNewAttentionView(t *testing.T) {
	loader := &splitLoader{
		state: State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 500}}},
		attention: AttentionState{WaitingCount: 1, Sessions: []AttentionSession{
			{ID: 2, Surface: "codex_app", Summary: "新请求", RequestCount: 1},
		}},
	}
	views := make(chan View, 4)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	controller.last = &State{Attention: AttentionState{WaitingCount: 1, Sessions: []AttentionSession{
		{ID: 1, Surface: "codex_app", Summary: "旧请求", RequestCount: 1},
	}}}
	controller.presentMu.Lock()
	controller.LoadAsync(context.Background())
	waitForController(t, controller, func() bool {
		return !controller.loading && controller.last.Snapshot.Usage.TodayTokens == 500
	})
	controller.LoadAttentionAsync(context.Background())
	waitForController(t, controller, func() bool {
		return !controller.attentionLoading && controller.last.Attention.Sessions[0].ID == 2
	})
	controller.presentMu.Unlock()
	view := waitForView(t, views, func(view View) bool { return len(view.Waiting) > 0 })
	if view.Waiting[0].SessionID != 2 {
		t.Fatalf("旧完整加载视图覆盖了新 attention: %+v", view)
	}
	select {
	case stale := <-views:
		if len(stale.Waiting) > 0 && stale.Waiting[0].SessionID == 1 {
			t.Fatalf("新 attention 后发布了旧视图: %+v", stale)
		}
	case <-time.After(20 * time.Millisecond):
	}
}

func TestAttentionLoadFailureDoesNotDiscardPendingFullView(t *testing.T) {
	loader := &splitLoader{
		state:        State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 500}}},
		attentionErr: errors.New("attention unavailable"),
	}
	views := make(chan View, 2)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	controller.presentMu.Lock()
	controller.LoadAsync(context.Background())
	waitForController(t, controller, func() bool {
		return !controller.loading && controller.last.Snapshot.Usage.TodayTokens == 500
	})
	controller.LoadAttentionAsync(context.Background())
	waitForController(t, controller, func() bool { return !controller.attentionLoading })
	controller.presentMu.Unlock()
	view := waitForView(t, views, func(View) bool { return true })
	if view.Today != "今日：500 tokens" {
		t.Fatalf("attention 失败吞掉了等待发布的完整视图: %+v", view)
	}
}

func TestJumpErrorIsAsyncAndSurvivesAttentionPoll(t *testing.T) {
	loader := &fakeLoader{state: State{Attention: AttentionState{WaitingCount: 1}}}
	views := make(chan View, 8)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	jumper := &recordingJumper{ids: make(chan int64, 1), err: errors.New("Automation 权限被拒绝")}
	controller.SetSessionJumper(jumper)
	if !controller.JumpAttentionSessionAsync(context.Background(), 7) {
		t.Fatal("异步跳转未启动")
	}
	select {
	case <-jumper.ids:
	case <-time.After(time.Second):
		t.Fatal("同步菜单线程被跳转阻塞")
	}
	errorView := waitForView(t, views, func(view View) bool { return strings.Contains(view.Status, "Automation 权限被拒绝") })
	if errorView.Status == "" {
		t.Fatal("跳转错误未展示")
	}
	controller.LoadAttentionAsync(context.Background())
	retained := waitForView(t, views, func(view View) bool { return strings.Contains(view.Status, "Automation 权限被拒绝") })
	if retained.Status != errorView.Status {
		t.Fatalf("attention 轮询覆盖了跳转错误: before=%q after=%q", errorView.Status, retained.Status)
	}
}

func TestSuccessfulJumpClearsPreviousJumpError(t *testing.T) {
	loader := &fakeLoader{state: State{Attention: AttentionState{WaitingCount: 1}}}
	views := make(chan View, 8)
	controller := NewController(loader, staticRefresher{}, "http://127.0.0.1:8080", func(view View) { views <- view })
	jumper := &sequenceJumper{ids: make(chan int64, 2), errs: []error{errors.New("Automation 权限被拒绝"), nil}}
	controller.SetSessionJumper(jumper)
	controller.JumpAttentionSessionAsync(context.Background(), 7)
	<-jumper.ids
	waitForView(t, views, func(view View) bool { return strings.Contains(view.Status, "Automation 权限被拒绝") })
	waitForController(t, controller, func() bool { return !controller.jumping && !controller.attentionLoading })
	for len(views) > 0 {
		<-views
	}
	if !controller.JumpAttentionSessionAsync(context.Background(), 7) {
		t.Fatal("错误后的成功重试未启动")
	}
	<-jumper.ids
	view := waitForView(t, views, func(view View) bool { return !strings.Contains(view.Status, "Automation 权限被拒绝") })
	if strings.Contains(view.Status, "跳转 Codex 会话失败") {
		t.Fatalf("成功重试仍显示旧错误: %+v", view)
	}
}

type fakeLoader struct {
	mu    sync.Mutex
	state State
	err   error
}

func (f *fakeLoader) Load(context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.err
}

func (f *fakeLoader) LoadAttention(context.Context) (AttentionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state.Attention, f.err
}

type staticRefresher struct{ usageErr, quotaErr error }

func (f staticRefresher) Refresh(context.Context) (error, error) { return f.usageErr, f.quotaErr }

type blockingRefresher struct {
	started, release chan struct{}
	calls            int
}

func (f *blockingRefresher) Refresh(context.Context) (error, error) {
	f.calls++
	close(f.started)
	<-f.release
	return nil, nil
}

type recordingRunner struct {
	name string
	args []string
}

type recordingJumper struct {
	ids chan int64
	err error
}

type sequenceJumper struct {
	mu   sync.Mutex
	ids  chan int64
	errs []error
}

func (jumper *sequenceJumper) JumpAttentionSession(_ context.Context, id int64) error {
	jumper.mu.Lock()
	err := jumper.errs[0]
	jumper.errs = jumper.errs[1:]
	jumper.mu.Unlock()
	jumper.ids <- id
	return err
}

func (jumper *recordingJumper) JumpAttentionSession(_ context.Context, id int64) error {
	jumper.ids <- id
	return jumper.err
}

type splitLoader struct {
	state        State
	attention    AttentionState
	attentionErr error
}

func (loader *splitLoader) Load(context.Context) (State, error) { return loader.state, nil }

func (loader *splitLoader) LoadAttention(context.Context) (AttentionState, error) {
	return loader.attention, loader.attentionErr
}

func (r *recordingRunner) Run(name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil
}
func stateWithToday(tokens int64) State {
	return State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: tokens}}, Quota: QuotaState{Enabled: false}}
}
func waitForView(t *testing.T, views <-chan View, match func(View) bool) View {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case view := <-views:
			if match(view) {
				return view
			}
		case <-timer.C:
			t.Fatal("等待菜单 view 超时")
		}
	}
}

func waitForController(t *testing.T, controller *Controller, match func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		controller.mu.Lock()
		matched := match()
		controller.mu.Unlock()
		if matched {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待 controller 状态超时")
}
