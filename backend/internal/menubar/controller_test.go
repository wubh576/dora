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

type successfulBlockingRefresher struct {
	started chan struct{}
	release chan struct{}
}

type queuedRefresher struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	started   chan int
	releases  []chan struct{}
}

func (refresher *queuedRefresher) Refresh(context.Context) (error, error) {
	refresher.mu.Lock()
	call := refresher.calls
	refresher.calls++
	refresher.active++
	refresher.maxActive = max(refresher.maxActive, refresher.active)
	release := refresher.releases[call]
	refresher.mu.Unlock()
	refresher.started <- call + 1
	<-release
	refresher.mu.Lock()
	refresher.active--
	refresher.mu.Unlock()
	return nil, nil
}

func (refresher *queuedRefresher) callCount() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return refresher.calls
}

func (refresher *queuedRefresher) maximumActive() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return refresher.maxActive
}

func (refresher *successfulBlockingRefresher) Refresh(context.Context) (error, error) {
	close(refresher.started)
	<-refresher.release
	return nil, nil
}

type recordingRunner struct {
	name string
	args []string
	err  error
}

func (runner *recordingRunner) Run(name string, args ...string) error {
	runner.name = name
	runner.args = append([]string(nil), args...)
	return runner.err
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
		if view.CompactStatus != "1" || view.RunningCount != 1 || len(view.Sessions) != 1 {
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
			if view.Today == "今日 1K tokens" && view.CompactStatus == "2" && view.RunningCount == 2 {
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

func TestControllerRefreshKeepsPartialSuccessAndCollapses(t *testing.T) {
	refresher := &blockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	loader := &fakeLoader{state: State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 2000}}}}
	presented := make(chan View, 8)
	controller := NewController(loader, refresher, "", func(view View) { presented <- view })
	pointerInside := true
	controller.SetPointerChecker(func() bool { return pointerInside })
	controller.UIInteraction(true)
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("首次 refresh 未启动")
	}
	<-refresher.started
	controller.UIInteraction(false)
	if state := controller.machine.State(); state.Mode != ModeHover {
		t.Fatalf("刷新进行中未按真实指针状态保持展开: %+v", state)
	}
	pointerInside = false
	controller.Hover(false)
	collapseDeadline := time.After(time.Second)
	for controller.machine.State().Mode != ModeCompact {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-collapseDeadline:
			t.Fatalf("刷新进行中鼠标离开后未收起: %+v", controller.machine.State())
		}
	}
	close(refresher.release)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.Today == "今日 2K tokens" && view.OperationStatus == "token 已更新，配额刷新失败" {
				if view.Mode != string(ModeCompact) {
					t.Fatalf("鼠标已离开时刷新失败重新展开: %+v", view)
				}
				return
			}
		case <-deadline:
			t.Fatal("部分刷新成功状态未发布")
		}
	}
}

func TestControllerRefreshQueuesOneFollowUpWithoutConcurrency(t *testing.T) {
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	refresher := &queuedRefresher{
		started:  make(chan int, 2),
		releases: []chan struct{}{firstRelease, secondRelease},
	}
	presented := make(chan View, 16)
	controller := NewController(&fakeLoader{}, refresher, "", func(view View) { presented <- view })
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("首次 refresh 未启动")
	}
	if call := <-refresher.started; call != 1 {
		t.Fatalf("首次 refresh 调用序号 = %d", call)
	}
	if !controller.RefreshAsync(context.Background()) || !controller.RefreshAsync(context.Background()) {
		t.Fatal("刷新中的重复点击未被接受")
	}
	close(firstRelease)
	select {
	case call := <-refresher.started:
		if call != 2 {
			t.Fatalf("待执行 refresh 调用序号 = %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("当前刷新完成后未执行合并的下一轮刷新")
	}
	if calls := refresher.callCount(); calls != 2 {
		t.Fatalf("重复点击未合并为一轮刷新: calls=%d", calls)
	}
	close(secondRelease)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if !view.Refreshing && view.OperationStatus == "刷新完成" {
				if calls := refresher.callCount(); calls != 2 {
					t.Fatalf("刷新结束后出现额外调用: calls=%d", calls)
				}
				if maximum := refresher.maximumActive(); maximum != 1 {
					t.Fatalf("刷新发生并发调用: max_active=%d", maximum)
				}
				return
			}
		case <-deadline:
			t.Fatal("合并刷新完成状态未发布")
		}
	}
}

func TestControllerRefreshStaysExpandedUntilPointerLeavesAfterSuccess(t *testing.T) {
	refresher := &successfulBlockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	presented := make(chan View, 16)
	controller := NewController(&fakeLoader{}, refresher, "", func(view View) { presented <- view })
	pointerInside := true
	controller.SetPointerChecker(func() bool { return pointerInside })
	controller.UIInteraction(true)
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("refresh 未启动")
	}
	<-refresher.started
	controller.UIInteraction(false)
	if state := controller.machine.State(); state.Mode != ModeHover {
		t.Fatalf("刷新进行中未保持展开: %+v", state)
	}
	close(refresher.release)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.OperationStatus == "刷新完成" {
				if view.Mode != string(ModeHover) {
					t.Fatalf("刷新成功后未保留展开结果: %+v", view)
				}
				pointerInside = false
				controller.Hover(false)
				collapseDeadline := time.After(time.Second)
				for controller.machine.State().Mode != ModeCompact {
					select {
					case <-time.After(10 * time.Millisecond):
					case <-collapseDeadline:
						t.Fatalf("刷新结果在鼠标离开后未收起: %+v", controller.machine.State())
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("刷新成功状态未发布")
		}
	}
}

func TestControllerSuccessfulRefreshPreservesNewAttention(t *testing.T) {
	refresher := &successfulBlockingRefresher{started: make(chan struct{}), release: make(chan struct{})}
	presented := make(chan View, 16)
	controller := NewController(&fakeLoader{}, refresher, "", func(view View) { presented <- view })
	if !controller.RefreshAsync(context.Background()) {
		t.Fatal("refresh 未启动")
	}
	<-refresher.started
	if !controller.NotifyAttention(91, 7) {
		t.Fatal("刷新期间的新 attention 未被接受")
	}
	close(refresher.release)
	deadline := time.After(time.Second)
	for {
		select {
		case view := <-presented:
			if view.OperationStatus == "刷新完成" {
				if view.Mode != string(ModeAttention) || view.HighlightRequestID != 91 || view.HighlightSessionID != 7 {
					t.Fatalf("刷新成功错误清除了新 attention: %+v", view)
				}
				return
			}
		case <-deadline:
			t.Fatal("刷新成功状态未发布")
		}
	}
}

func TestControllerOperationStatusExpiresBackToSnapshotStatus(t *testing.T) {
	now := time.Date(2026, 8, 5, 13, 45, 0, 0, time.Local)
	lastScan := now.Format(time.RFC3339Nano)
	presented := make(chan View, 4)
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.now = func() time.Time { return now }
	controller.mu.Lock()
	controller.last = &State{Snapshot: Snapshot{Usage: SnapshotUsage{LastScanAt: &lastScan}}}
	controller.mu.Unlock()

	controller.PresentStatus("刷新完成")
	temporary := <-presented
	if temporary.Status != "刷新完成" || temporary.OperationStatus != "刷新完成" {
		t.Fatalf("临时刷新状态错误: %+v", temporary)
	}
	now = now.Add(operationStatusTTL)
	controller.publish()
	restored := <-presented
	if restored.Status != "已更新 · 13:45" || restored.OperationStatus != "" || restored.OperationError {
		t.Fatalf("临时状态到期后未恢复快照状态: %+v", restored)
	}
}

func TestRefreshStatusPreservesExistingResultCopy(t *testing.T) {
	failure := errors.New("offline")
	tests := []struct {
		name               string
		usageErr, quotaErr error
		want               string
	}{
		{name: "success", want: "刷新完成"},
		{name: "quota failure", quotaErr: failure, want: "token 已更新，配额刷新失败"},
		{name: "token failure", usageErr: failure, want: "token 刷新失败"},
		{name: "complete failure", usageErr: failure, quotaErr: failure, want: "token 与配额刷新失败"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := refreshStatus(test.usageErr, test.quotaErr); got != test.want {
				t.Fatalf("refreshStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestControllerOpenDashboardUsesConfiguredLoopbackURLAndDismissesClick(t *testing.T) {
	runner := &recordingRunner{}
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "http://127.0.0.1:18083", func(View) {})
	controller.runner = runner
	controller.SetPointerChecker(func() bool { return true })
	controller.UIInteraction(true)
	if err := controller.OpenDashboard(); err != nil {
		t.Fatal(err)
	}
	controller.UIInteraction(false)
	if runner.name != "open" || len(runner.args) != 1 || runner.args[0] != "http://127.0.0.1:18083" {
		t.Fatalf("open 参数错误: %s %+v", runner.name, runner.args)
	}
	if state := controller.machine.State(); state.Mode != ModeCompact {
		t.Fatalf("打开仪表盘后未收起: %+v", state)
	}
}

func TestControllerOpenDashboardFailureKeepsInteractionVisible(t *testing.T) {
	runner := &recordingRunner{err: errors.New("open failed")}
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "http://127.0.0.1:18083", func(View) {})
	controller.runner = runner
	controller.UIInteraction(true)
	if err := controller.OpenDashboard(); err == nil {
		t.Fatal("打开仪表盘失败未返回错误")
	}
	if state := controller.machine.State(); state.Mode != ModeInteraction {
		t.Fatalf("打开仪表盘失败时错误收起: %+v", state)
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

func TestControllerSuccessfulJumpDismissesWhilePointerRemainsInside(t *testing.T) {
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(View) {})
	controller.SetSessionJumper(&fakeJumper{})
	controller.machine.SetPointerChecker(func() bool { return true })
	controller.NotifyAttention(9, 7)
	controller.UIInteraction(true)
	if !controller.JumpSessionAsync(context.Background(), 7) {
		t.Fatal("JumpSessionAsync 未启动")
	}
	waitForJumpIdle(t, controller)
	controller.UIInteraction(false)
	controller.Hover(true)
	if state := controller.machine.State(); state.Mode != ModeCompact {
		t.Fatalf("成功跳转后被迟到事件重新展开: %+v", state)
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
