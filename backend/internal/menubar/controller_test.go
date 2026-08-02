package menubar

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeLoader struct {
	state       State
	runtime     RuntimeState
	loadErr     error
	runtimeErr  error
	loadGate    <-chan struct{}
	runtimeGate <-chan struct{}
}

func (loader *fakeLoader) Load(context.Context) (State, error) {
	if loader.loadGate != nil {
		<-loader.loadGate
	}
	return loader.state, loader.loadErr
}

func (loader *fakeLoader) LoadRuntime(context.Context) (RuntimeState, error) {
	if loader.runtimeGate != nil {
		<-loader.runtimeGate
	}
	return loader.runtime, loader.runtimeErr
}

type fakeRefresher struct{ usageErr, quotaErr error }

func (refresher fakeRefresher) Refresh(context.Context) (error, error) {
	return refresher.usageErr, refresher.quotaErr
}

type sequencedLoader struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	oldGate      chan struct{}
}

func (loader *sequencedLoader) Load(context.Context) (State, error) {
	return State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 1000}}}, nil
}

func (loader *sequencedLoader) LoadRuntime(context.Context) (RuntimeState, error) {
	loader.mu.Lock()
	loader.calls++
	call := loader.calls
	loader.mu.Unlock()
	if call == 1 {
		close(loader.firstStarted)
		<-loader.oldGate
		return RuntimeState{RunningCount: 1}, nil
	}
	return RuntimeState{RunningCount: 2}, nil
}

type fakeJumper struct {
	mu      sync.Mutex
	session int64
	calls   int
	err     error
}

func (jumper *fakeJumper) JumpAttentionSession(_ context.Context, session int64) error {
	jumper.mu.Lock()
	defer jumper.mu.Unlock()
	jumper.session = session
	jumper.calls++
	return jumper.err
}

func (jumper *fakeJumper) setError(err error) {
	jumper.mu.Lock()
	jumper.err = err
	jumper.mu.Unlock()
}

type blockingRefresher struct {
	started chan struct{}
	release chan struct{}
}

func (refresher *blockingRefresher) Refresh(context.Context) (error, error) {
	close(refresher.started)
	<-refresher.release
	return nil, errors.New("quota offline")
}

type recordingRunner struct {
	name string
	args []string
}

func (runner *recordingRunner) Run(name string, args ...string) error {
	runner.name = name
	runner.args = append([]string(nil), args...)
	return nil
}

func TestControllerLoadsUsageAndRuntimeIntoOneView(t *testing.T) {
	loader := &fakeLoader{
		state:   State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 1000}}},
		runtime: RuntimeState{RunningCount: 1, Sessions: []RuntimeSession{{ID: 3, State: "running", SessionName: "dora"}}},
	}
	presented := make(chan View, 8)
	controller := NewController(loader, fakeRefresher{}, "http://127.0.0.1:8080", func(view View) { presented <- view })
	if !controller.LoadAsync(context.Background()) {
		t.Fatal("首次 LoadAsync 未启动")
	}
	select {
	case view := <-presented:
		if view.CompactTokens != "今日 token 1K" || view.RunningCount != 1 || len(view.Sessions) != 1 {
			t.Fatalf("组合视图错误: %+v", view)
		}
	case <-time.After(time.Second):
		t.Fatal("LoadAsync 未发布")
	}
}

func TestControllerStopDropsLateLoad(t *testing.T) {
	gate := make(chan struct{})
	loader := &fakeLoader{loadGate: gate, runtime: RuntimeState{}}
	presented := make(chan View, 1)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.LoadAsync(context.Background())
	controller.Stop()
	close(gate)
	select {
	case view := <-presented:
		t.Fatalf("Stop 后发布了迟到视图: %+v", view)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestControllerDoesNotOverwriteNewRuntimeWithOlderFullLoad(t *testing.T) {
	loader := &sequencedLoader{firstStarted: make(chan struct{}), oldGate: make(chan struct{})}
	presented := make(chan View, 8)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.LoadAsync(context.Background())
	<-loader.firstStarted
	if !controller.LoadRuntimeAsync(context.Background()) {
		t.Fatal("并发实时加载未启动")
	}
	close(loader.oldGate)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.CompactTokens == "今日 token 1K" && view.RunningCount == 2 {
				return
			}
		case <-deadline:
			t.Fatal("旧 full load 覆盖了更新的 runtime")
		}
	}
}

func TestControllerRuntimeFailureKeepsLastStateAndRecovers(t *testing.T) {
	loader := &fakeLoader{runtime: RuntimeState{RunningCount: 1}}
	presented := make(chan View, 8)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.LoadAsync(context.Background())
	<-presented
	loader.runtimeErr = errors.New("offline")
	controller.LoadRuntimeAsync(context.Background())
	failed := <-presented
	if failed.RunningCount != 1 || failed.OperationStatus != "实时状态连接失败" {
		t.Fatalf("runtime 失败未保留状态或提示错误: %+v", failed)
	}
	loader.runtimeErr = nil
	loader.runtime = RuntimeState{RunningCount: 2}
	controller.LoadRuntimeAsync(context.Background())
	recovered := <-presented
	if recovered.RunningCount != 2 || recovered.OperationStatus != "" {
		t.Fatalf("runtime 恢复后状态错误: %+v", recovered)
	}
}

func TestControllerRefreshIsSingleFlightAndKeepsPartialSuccess(t *testing.T) {
	refresher := &blockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	loader := &fakeLoader{state: State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 2000}}}}
	presented := make(chan View, 8)
	controller := NewController(loader, refresher, "", func(view View) { presented <- view })
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("首次 refresh 未启动")
	}
	<-refresher.started
	if controller.RefreshAsync(context.Background()) {
		t.Fatal("并发 refresh 被错误接受")
	}
	close(refresher.release)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.CompactTokens == "今日 token 2K" && view.OperationStatus == "token 已更新，配额刷新失败" {
				return
			}
		case <-deadline:
			t.Fatal("部分刷新成功状态未发布")
		}
	}
}

func TestControllerOpenDashboardUsesConfiguredLoopbackURL(t *testing.T) {
	runner := &recordingRunner{}
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "http://127.0.0.1:18083", func(View) {})
	controller.runner = runner
	if err := controller.OpenDashboard(); err != nil {
		t.Fatal(err)
	}
	if runner.name != "open" || len(runner.args) != 1 || runner.args[0] != "http://127.0.0.1:18083" {
		t.Fatalf("open 参数错误: %s %+v", runner.name, runner.args)
	}
}

func TestControllerJumpFailureKeepsExpandedUntilUserCanReadReason(t *testing.T) {
	jumper := &fakeJumper{err: errors.New("target gone")}
	presented := make(chan View, 8)
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.SetSessionJumper(jumper)
	controller.NotifyAttention(9, 7)
	if !controller.JumpSessionAsync(context.Background(), 7) {
		t.Fatal("JumpSessionAsync 未启动")
	}
	deadline := time.After(time.Second)
	for {
		jumper.mu.Lock()
		got := jumper.session
		jumper.mu.Unlock()
		if got == 7 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("未调用精确 session jumper")
		case <-time.After(time.Millisecond):
		}
	}
	if controller.machine.State().Mode == ModeCompact {
		t.Fatalf("跳转返回前错误收起: %+v", controller.machine.State())
	}
	statusDeadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.OperationStatus == "跳转 Codex 会话失败：target gone" && view.Expanded {
				return
			}
		case <-statusDeadline:
			t.Fatal("展开状态没有显示跳转失败")
		}
	}
}

func TestControllerSuccessfulRetryClearsPreviousJumpError(t *testing.T) {
	jumper := &fakeJumper{err: errors.New("denied")}
	presented := make(chan View, 16)
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.SetSessionJumper(jumper)
	controller.JumpSessionAsync(context.Background(), 1)
	waitForView(t, presented, func(view View) bool { return view.OperationStatus == "跳转 Codex 会话失败：denied" })
	waitForJumpIdle(t, controller)
	jumper.setError(nil)
	controller.JumpSessionAsync(context.Background(), 1)
	waitForView(t, presented, func(view View) bool { return !view.Expanded && view.OperationStatus == "" })
	waitForJumpIdle(t, controller)
	jumper.mu.Lock()
	calls := jumper.calls
	jumper.mu.Unlock()
	if calls != 2 {
		t.Fatalf("jump 调用次数 = %d", calls)
	}
}

func TestControllerRuntimeRefreshWithSameLayoutDoesNotAnimateFrame(t *testing.T) {
	loader := &fakeLoader{
		state:   State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 1000}}},
		runtime: RuntimeState{WaitingCount: 1, Sessions: []RuntimeSession{{ID: 7, State: "waiting", SessionName: "dora", RequestID: 9}}},
	}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.LoadAsync(context.Background())
	<-presented
	controller.NotifyAttention(9, 7)
	expanded := <-presented
	if !expanded.Expanded || !expanded.AnimateFrame {
		t.Fatalf("首次展开没有尺寸动画: %+v", expanded)
	}
	loader.runtime.Sessions[0].WaitSeconds = 1
	controller.LoadRuntimeAsync(context.Background())
	updated := <-presented
	if updated.Layout != expanded.Layout || updated.AnimateFrame {
		t.Fatalf("相同 layout 的每秒刷新重复动画: expanded=%+v updated=%+v", expanded.Layout, updated)
	}
}

func TestControllerExplainsUnjumpableSessionInExpandedFooter(t *testing.T) {
	loader := &fakeLoader{
		runtime: RuntimeState{RunningCount: 1, Sessions: []RuntimeSession{{
			ID: 12, State: "running", SessionName: "probe", JumpReason: "无法识别 Codex 会话来源",
		}}},
	}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.LoadAsync(context.Background())
	<-presented
	controller.ExplainSession(12)
	view := waitForView(t, presented, func(view View) bool {
		return view.Expanded && view.OperationStatus == "无法识别 Codex 会话来源"
	})
	if !view.OperationError {
		t.Fatalf("不可跳转原因未作为错误反馈: %+v", view)
	}
}

func waitForView(t *testing.T, views <-chan View, match func(View) bool) View {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-views:
			if match(view) {
				return view
			}
		case <-deadline:
			t.Fatal("等待目标视图超时")
		}
	}
}

func waitForJumpIdle(t *testing.T, controller *Controller) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		controller.mu.Lock()
		jumping := controller.jumping
		controller.mu.Unlock()
		if !jumping {
			return
		}
		select {
		case <-deadline:
			t.Fatal("等待 jump 完成超时")
		case <-time.After(time.Millisecond):
		}
	}
}
