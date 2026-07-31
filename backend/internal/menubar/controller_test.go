package menubar

import (
	"context"
	"errors"
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
