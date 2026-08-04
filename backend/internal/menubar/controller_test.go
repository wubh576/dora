package menubar

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/attention"
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

type permissionRaceLoader struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	oldGate      chan struct{}
}

func (loader *permissionRaceLoader) Load(context.Context) (State, error) {
	return State{}, nil
}

func (loader *permissionRaceLoader) LoadRuntime(context.Context) (RuntimeState, error) {
	loader.mu.Lock()
	loader.calls++
	call := loader.calls
	loader.mu.Unlock()
	if call == 1 {
		close(loader.firstStarted)
		<-loader.oldGate
		return permissionRuntime("interaction-current", "Bash · current"), nil
	}
	return permissionRuntime("interaction-next", "Bash · next"), nil
}

func permissionRuntime(interactionID, summary string) RuntimeState {
	return RuntimeState{WaitingCount: 1, Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", SessionName: "dora", Respondable: true,
		InteractionID: interactionID, PermissionSummary: summary, PermissionQueueCount: 1,
	}}}
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

type fakePermissionResponder struct {
	mu            sync.Mutex
	interactionID string
	action        attention.PermissionAction
	calls         int
	err           error
	started       chan struct{}
	release       <-chan struct{}
}

func (responder *fakePermissionResponder) Submit(_ context.Context, interactionID string, action attention.PermissionAction) error {
	responder.mu.Lock()
	responder.interactionID = interactionID
	responder.action = action
	responder.calls++
	started := responder.started
	release := responder.release
	err := responder.err
	responder.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return err
}

type orderedPermissionResponder struct{ events chan string }

func (responder orderedPermissionResponder) Submit(_ context.Context, _ string, action attention.PermissionAction) error {
	responder.events <- string(action)
	return nil
}

type orderedJumper struct{ events chan string }

func (jumper orderedJumper) JumpAttentionSession(context.Context, int64) error {
	jumper.events <- "jump"
	return nil
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

func TestControllerRefreshIsSingleFlightAndKeepsPartialSuccess(t *testing.T) {
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
	controller.UIInteraction(true)
	if controller.RefreshAsync(context.Background()) {
		t.Fatal("并发 refresh 被错误接受")
	}
	controller.UIInteraction(false)
	if state := controller.machine.State(); state.Mode != ModeHover {
		t.Fatalf("被拒绝的重复刷新点击未保持展开: %+v", state)
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

func TestControllerAllowsCurrentPermissionWithoutJumpAndLoadsNextQueueItem(t *testing.T) {
	loader := &fakeLoader{runtime: RuntimeState{WaitingCount: 1, Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", SessionName: "dora", Respondable: true,
		InteractionID: "interaction-next", PermissionSummary: "Bash · next", PermissionQueueCount: 1,
	}}}}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.last = &State{Runtime: RuntimeState{WaitingCount: 1, Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", SessionName: "dora", Respondable: true,
		InteractionID: "interaction-current", PermissionSummary: "Bash · current", PermissionQueueCount: 2,
	}}}}
	responder := &fakePermissionResponder{}
	jumper := &fakeJumper{}
	controller.SetPermissionResponder(responder)
	controller.SetSessionJumper(jumper)
	if !controller.RespondPermissionAsync(context.Background(), 7, "interaction-current", attention.PermissionAllow) {
		t.Fatal("本次允许未启动")
	}
	waitForView(t, presented, func(view View) bool {
		return len(view.Sessions) == 1 && !view.Sessions[0].Respondable
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		controller.mu.Lock()
		current := controller.permissionInteractionLocked(7)
		controller.mu.Unlock()
		if current == "interaction-next" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	controller.mu.Lock()
	current := controller.permissionInteractionLocked(7)
	controller.mu.Unlock()
	if current != "interaction-next" {
		t.Fatalf("下一条授权请求未出现: %q", current)
	}
	responder.mu.Lock()
	interactionID, action, calls := responder.interactionID, responder.action, responder.calls
	responder.mu.Unlock()
	jumper.mu.Lock()
	jumpCalls := jumper.calls
	jumper.mu.Unlock()
	if interactionID != "interaction-current" || action != attention.PermissionAllow || calls != 1 || jumpCalls != 0 {
		t.Fatalf("直接允许行为错误: interaction=%q action=%q calls=%d jumps=%d", interactionID, action, calls, jumpCalls)
	}
}

func TestControllerDeniesCurrentPermissionWithoutJump(t *testing.T) {
	loader := &fakeLoader{runtime: RuntimeState{WaitingCount: 1, Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", SessionName: "dora", Respondable: false,
	}}}}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.last = &State{Runtime: permissionRuntime("interaction-deny", "Bash · deny")}
	responder := &fakePermissionResponder{}
	jumper := &fakeJumper{}
	controller.SetPermissionResponder(responder)
	controller.SetSessionJumper(jumper)

	if !controller.RespondPermissionAsync(context.Background(), 7, "interaction-deny", attention.PermissionDeny) {
		t.Fatal("拒绝当前授权未启动")
	}
	waitForView(t, presented, func(view View) bool {
		return len(view.Sessions) == 1 && !view.Sessions[0].Respondable
	})
	responder.mu.Lock()
	interactionID, action, calls := responder.interactionID, responder.action, responder.calls
	responder.mu.Unlock()
	jumper.mu.Lock()
	jumpCalls := jumper.calls
	jumper.mu.Unlock()
	if interactionID != "interaction-deny" || action != attention.PermissionDeny || calls != 1 || jumpCalls != 0 {
		t.Fatalf("直接拒绝行为错误: interaction=%q action=%q calls=%d jumps=%d", interactionID, action, calls, jumpCalls)
	}
}

func TestControllerStalePermissionActionClearsButtonWithoutError(t *testing.T) {
	loader := &fakeLoader{runtime: RuntimeState{RunningCount: 1, Sessions: []RuntimeSession{{
		ID: 7, State: "running", SessionName: "dora",
	}}}}
	presented := make(chan View, 16)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.last = &State{Runtime: permissionRuntime("interaction-ended", "Bash · ended")}
	controller.SetPermissionResponder(&fakePermissionResponder{err: attention.ErrPermissionResolved})

	if !controller.RespondPermissionAsync(context.Background(), 7, "interaction-ended", attention.PermissionAllow) {
		t.Fatal("过期授权操作未启动")
	}
	view := waitForView(t, presented, func(view View) bool {
		return len(view.Sessions) == 1 && !view.Sessions[0].Respondable
	})
	if view.OperationStatus != "" || view.OperationError {
		t.Fatalf("已结束请求显示了持久错误: %+v", view)
	}
}

func TestControllerOldRuntimeLoadCannotRestoreResolvedPermission(t *testing.T) {
	loader := &permissionRaceLoader{firstStarted: make(chan struct{}), oldGate: make(chan struct{})}
	presented := make(chan View, 32)
	controller := NewController(loader, fakeRefresher{}, "", func(view View) { presented <- view })
	controller.last = &State{Runtime: permissionRuntime("interaction-current", "Bash · current")}
	controller.SetPermissionResponder(&fakePermissionResponder{})

	if !controller.LoadRuntimeAsync(context.Background()) {
		t.Fatal("旧 runtime 加载未启动")
	}
	<-loader.firstStarted
	if !controller.RespondPermissionAsync(context.Background(), 7, "interaction-current", attention.PermissionAllow) {
		t.Fatal("本次允许未启动")
	}
	waitForView(t, presented, func(view View) bool {
		return len(view.Sessions) == 1 && view.Sessions[0].Respondable && view.Sessions[0].InteractionID == "interaction-next"
	})
	close(loader.oldGate)
	time.Sleep(20 * time.Millisecond)

	controller.mu.Lock()
	interactionID := controller.permissionInteractionLocked(7)
	controller.mu.Unlock()
	if interactionID != "interaction-next" {
		t.Fatalf("迟到的 runtime 恢复了已处理请求: %q", interactionID)
	}
}

func TestControllerHandoffsBeforeExactJumpAndOrdinaryJumpSkipsHandoff(t *testing.T) {
	events := make(chan string, 4)
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(View) {})
	controller.last = &State{Runtime: RuntimeState{Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", Respondable: true, InteractionID: "interaction-7",
	}}}}
	controller.SetPermissionResponder(orderedPermissionResponder{events: events})
	controller.SetSessionJumper(orderedJumper{events: events})
	if !controller.JumpPermissionSessionAsync(context.Background(), 7, "interaction-7") {
		t.Fatal("handoff jump 未启动")
	}
	if first, second := <-events, <-events; first != string(attention.PermissionHandoff) || second != "jump" {
		t.Fatalf("handoff/jump 顺序错误: %q -> %q", first, second)
	}
	waitForJumpIdle(t, controller)

	controller.mu.Lock()
	controller.last = &State{Runtime: RuntimeState{Sessions: []RuntimeSession{{ID: 8, State: "running"}}}}
	controller.mu.Unlock()
	if !controller.JumpSessionAsync(context.Background(), 8) {
		t.Fatal("普通 jump 未启动")
	}
	if event := <-events; event != "jump" {
		t.Fatalf("普通 session 错误触发 handoff: %q", event)
	}
	waitForJumpIdle(t, controller)
}

func TestControllerStalePermissionHandoffStillJumps(t *testing.T) {
	for _, submitErr := range []error{attention.ErrPermissionResolved, attention.ErrPermissionNotFound} {
		t.Run(submitErr.Error(), func(t *testing.T) {
			controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(View) {})
			controller.last = &State{Runtime: permissionRuntime("interaction-ended", "Bash · ended")}
			responder := &fakePermissionResponder{err: submitErr}
			jumper := &fakeJumper{}
			controller.SetPermissionResponder(responder)
			controller.SetSessionJumper(jumper)

			if !controller.JumpPermissionSessionAsync(context.Background(), 7, "interaction-ended") {
				t.Fatal("过期 handoff 未启动")
			}
			waitForJumpIdle(t, controller)
			jumper.mu.Lock()
			jumpCalls, sessionID := jumper.calls, jumper.session
			jumper.mu.Unlock()
			if jumpCalls != 1 || sessionID != 7 {
				t.Fatalf("过期 handoff 没有继续跳转: calls=%d session=%d", jumpCalls, sessionID)
			}
		})
	}
}

func TestControllerRejectsDuplicatePermissionClickWhileFirstIsPending(t *testing.T) {
	release := make(chan struct{})
	responder := &fakePermissionResponder{started: make(chan struct{}, 1), release: release}
	controller := NewController(&fakeLoader{}, fakeRefresher{}, "", func(View) {})
	controller.last = &State{Runtime: RuntimeState{Sessions: []RuntimeSession{{
		ID: 7, State: "waiting", Respondable: true, InteractionID: "interaction-7",
	}}}}
	controller.SetPermissionResponder(responder)
	if !controller.RespondPermissionAsync(context.Background(), 7, "interaction-7", attention.PermissionDeny) {
		t.Fatal("首次拒绝未启动")
	}
	<-responder.started
	if controller.RespondPermissionAsync(context.Background(), 7, "interaction-7", attention.PermissionDeny) {
		t.Fatal("重复点击启动了第二次授权操作")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		responder.mu.Lock()
		calls := responder.calls
		responder.mu.Unlock()
		controller.mu.Lock()
		pending := controller.responding[7]
		controller.mu.Unlock()
		if calls == 1 && !pending {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("授权操作未完成")
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
