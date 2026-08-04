package menubar

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/wubh576/dora/backend/internal/attention"
)

const (
	jumpTimeout        = 5 * time.Second
	operationStatusTTL = 10 * time.Second
)

type Refresher interface {
	Refresh(context.Context) (usageErr, quotaErr error)
}

type CommandRunner interface {
	Run(string, ...string) error
}

type Presenter func(View)

type SessionJumper interface {
	JumpAttentionSession(context.Context, int64) error
}

type PermissionResponder interface {
	Submit(context.Context, string, attention.PermissionAction) error
}

type Controller struct {
	loader       Loader
	refresher    Refresher
	dashboardURL string
	runner       CommandRunner
	present      Presenter
	now          func() time.Time
	jumper       SessionJumper
	permission   PermissionResponder
	machine      *Machine

	presentMu       sync.Mutex
	mu              sync.Mutex
	last            *State
	screen          ScreenMetrics
	loading         bool
	runtimeLoading  bool
	loadVersion     uint64
	runtimeVersion  uint64
	refreshing      bool
	jumping         bool
	responding      map[int64]bool
	operationStatus string
	operationUntil  time.Time
	runtimeStatus   string
	lastFrame       Rect
	hasFrame        bool
	stopped         bool
}

func NewController(loader Loader, refresher Refresher, dashboardURL string, present Presenter) *Controller {
	controller := &Controller{
		loader: loader, refresher: refresher, dashboardURL: dashboardURL,
		runner: execRunner{}, present: present, now: time.Now,
		responding: make(map[int64]bool),
		screen:     ScreenMetrics{Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 947}},
	}
	controller.machine = NewMachine(func(MachineState) { controller.publish() })
	return controller
}

func (controller *Controller) SetPermissionResponder(responder PermissionResponder) {
	controller.mu.Lock()
	controller.permission = responder
	controller.mu.Unlock()
}

func (controller *Controller) SetSessionJumper(jumper SessionJumper) {
	controller.mu.Lock()
	controller.jumper = jumper
	controller.mu.Unlock()
}

func (controller *Controller) SetPointerChecker(check func() bool) {
	controller.machine.SetPointerChecker(check)
}

func (controller *Controller) SetScreen(screen ScreenMetrics) {
	controller.mu.Lock()
	controller.screen = screen
	controller.mu.Unlock()
	controller.publish()
}

func (controller *Controller) Hover(inside bool) { controller.machine.Hover(inside) }

func (controller *Controller) UIInteraction(active bool) { controller.machine.UIInteraction(active) }

func (controller *Controller) NotifyAttention(requestID, sessionID int64) bool {
	return controller.machine.Attention(requestID, sessionID)
}

func (controller *Controller) LoadAsync(ctx context.Context) bool {
	controller.mu.Lock()
	if controller.loading || controller.refreshing || controller.stopped {
		controller.mu.Unlock()
		return false
	}
	controller.loading = true
	controller.loadVersion++
	loadVersion := controller.loadVersion
	controller.runtimeVersion++
	runtimeVersion := controller.runtimeVersion
	controller.mu.Unlock()
	go func() {
		state, err := controller.loader.Load(ctx)
		runtimeState, runtimeErr := controller.loader.LoadRuntime(ctx)
		controller.mu.Lock()
		if controller.stopped || loadVersion != controller.loadVersion {
			controller.mu.Unlock()
			return
		}
		controller.loading = false
		if err == nil {
			if runtimeVersion == controller.runtimeVersion {
				if runtimeErr == nil {
					state.Runtime = runtimeState
					controller.runtimeStatus = ""
				} else {
					controller.runtimeStatus = "实时状态连接失败"
					if controller.last != nil {
						state.Runtime = controller.last.Runtime
					}
				}
			} else if controller.last != nil {
				state.Runtime = controller.last.Runtime
			}
			controller.last = &state
		}
		if err != nil {
			controller.setStatusLocked("连接本地服务失败")
		}
		controller.mu.Unlock()
		controller.publish()
	}()
	return true
}

func (controller *Controller) LoadRuntimeAsync(ctx context.Context) bool {
	controller.mu.Lock()
	if controller.runtimeLoading || controller.stopped {
		controller.mu.Unlock()
		return false
	}
	controller.runtimeLoading = true
	controller.runtimeVersion++
	version := controller.runtimeVersion
	controller.mu.Unlock()
	go func() {
		runtimeState, err := controller.loader.LoadRuntime(ctx)
		controller.mu.Lock()
		if controller.stopped {
			controller.mu.Unlock()
			return
		}
		controller.runtimeLoading = false
		if version != controller.runtimeVersion {
			controller.mu.Unlock()
			return
		}
		if err == nil {
			if controller.last == nil {
				controller.last = &State{}
			}
			controller.last.Runtime = runtimeState
			controller.runtimeStatus = ""
		} else {
			controller.runtimeStatus = "实时状态连接失败"
		}
		controller.mu.Unlock()
		controller.publish()
	}()
	return true
}

func (controller *Controller) RefreshAsync(ctx context.Context) bool {
	controller.mu.Lock()
	if controller.refreshing || controller.stopped {
		controller.mu.Unlock()
		return false
	}
	controller.refreshing = true
	controller.loading = false
	controller.loadVersion++
	loadVersion := controller.loadVersion
	controller.runtimeVersion++
	runtimeVersion := controller.runtimeVersion
	controller.operationStatus = ""
	controller.mu.Unlock()
	// 刷新只更新数据，面板是否展开继续由鼠标和当前交互决定。
	controller.publish()
	go func() {
		usageErr, quotaErr := controller.refresher.Refresh(ctx)
		state, loadErr := controller.loader.Load(ctx)
		runtimeState, runtimeErr := controller.loader.LoadRuntime(ctx)
		controller.mu.Lock()
		if controller.stopped || loadVersion != controller.loadVersion {
			controller.mu.Unlock()
			return
		}
		controller.refreshing = false
		if loadErr == nil {
			if runtimeVersion == controller.runtimeVersion {
				if runtimeErr == nil {
					state.Runtime = runtimeState
					controller.runtimeStatus = ""
				} else {
					controller.runtimeStatus = "实时状态连接失败"
					if controller.last != nil {
						state.Runtime = controller.last.Runtime
					}
				}
			} else if controller.last != nil {
				state.Runtime = controller.last.Runtime
			}
			controller.last = &state
		}
		status := refreshStatus(usageErr, quotaErr)
		if loadErr != nil {
			status = "连接本地服务失败"
		} else if runtimeErr != nil && runtimeVersion == controller.runtimeVersion {
			status = "实时状态连接失败"
		}
		controller.setStatusLocked(status)
		controller.mu.Unlock()
		controller.publish()
	}()
	return true
}

func (controller *Controller) JumpSessionAsync(ctx context.Context, sessionID int64) bool {
	return controller.jumpSessionAsync(ctx, sessionID, "")
}

func (controller *Controller) JumpPermissionSessionAsync(ctx context.Context, sessionID int64, interactionID string) bool {
	return controller.jumpSessionAsync(ctx, sessionID, interactionID)
}

func (controller *Controller) jumpSessionAsync(ctx context.Context, sessionID int64, interactionID string) bool {
	controller.mu.Lock()
	controller.operationStatus = ""
	controller.operationUntil = time.Time{}
	jumper := controller.jumper
	responder := controller.permission
	if jumper == nil || controller.jumping || controller.stopped || (interactionID != "" && responder == nil) {
		controller.mu.Unlock()
		if jumper == nil {
			controller.PresentStatus("Codex 跳转服务未配置")
		} else if interactionID != "" && responder == nil {
			controller.PresentStatus("Codex 授权服务未配置")
		}
		return false
	}
	controller.jumping = true
	controller.mu.Unlock()
	controller.machine.OperationStart()
	controller.publish()
	go func() {
		if interactionID != "" {
			err := responder.Submit(ctx, interactionID, attention.PermissionHandoff)
			if errors.Is(err, attention.ErrPermissionResolved) {
				err = nil
			}
			if err != nil {
				controller.mu.Lock()
				controller.jumping = false
				controller.setStatusLocked(fmt.Sprintf("交回 Codex 处理失败：%v", err))
				controller.mu.Unlock()
				controller.machine.OperationEnd(false)
				controller.publish()
				controller.LoadRuntimeAsync(ctx)
				return
			}
			controller.mu.Lock()
			controller.clearPermissionLocked(interactionID)
			controller.runtimeVersion++
			controller.mu.Unlock()
			controller.publish()
		}
		jumpContext, cancel := context.WithTimeout(ctx, jumpTimeout)
		defer cancel()
		err := jumper.JumpAttentionSession(jumpContext, sessionID)
		controller.mu.Lock()
		controller.jumping = false
		if err != nil {
			controller.setStatusLocked(fmt.Sprintf("跳转 Codex 会话失败：%v", err))
		}
		controller.mu.Unlock()
		if err == nil {
			controller.machine.Dismiss()
		} else {
			controller.machine.OperationEnd(false)
		}
		controller.publish()
		controller.LoadRuntimeAsync(ctx)
	}()
	return true
}

func (controller *Controller) RespondPermissionAsync(ctx context.Context, sessionID int64, interactionID string, action attention.PermissionAction) bool {
	if action != attention.PermissionAllow && action != attention.PermissionDeny {
		return false
	}
	controller.mu.Lock()
	responder := controller.permission
	if interactionID == "" || responder == nil || controller.responding[sessionID] || controller.stopped {
		controller.mu.Unlock()
		if responder == nil {
			controller.PresentStatus("Codex 授权服务未配置")
		}
		return false
	}
	controller.responding[sessionID] = true
	controller.mu.Unlock()
	go func() {
		err := responder.Submit(ctx, interactionID, action)
		var reloadVersion uint64
		controller.mu.Lock()
		delete(controller.responding, sessionID)
		if err == nil {
			controller.clearPermissionLocked(interactionID)
			controller.runtimeVersion++
			reloadVersion = controller.runtimeVersion
		} else {
			controller.setStatusLocked(fmt.Sprintf("处理 Codex 授权失败：%v", err))
		}
		controller.mu.Unlock()
		controller.publish()
		if err == nil {
			controller.loadRuntimeVersion(ctx, reloadVersion)
		} else {
			controller.LoadRuntimeAsync(ctx)
		}
	}()
	return true
}

func (controller *Controller) loadRuntimeVersion(ctx context.Context, version uint64) {
	runtimeState, err := controller.loader.LoadRuntime(ctx)
	controller.mu.Lock()
	if controller.stopped || version != controller.runtimeVersion {
		controller.mu.Unlock()
		return
	}
	if err == nil {
		if controller.last == nil {
			controller.last = &State{}
		}
		controller.last.Runtime = runtimeState
		controller.runtimeStatus = ""
	} else {
		controller.runtimeStatus = "实时状态连接失败"
	}
	controller.mu.Unlock()
	controller.publish()
}

func (controller *Controller) permissionInteractionLocked(sessionID int64) string {
	if controller.last == nil {
		return ""
	}
	for _, session := range controller.last.Runtime.Sessions {
		if session.ID == sessionID && session.Respondable {
			return session.InteractionID
		}
	}
	return ""
}

func (controller *Controller) clearPermissionLocked(interactionID string) {
	if controller.last == nil {
		return
	}
	for index := range controller.last.Runtime.Sessions {
		session := &controller.last.Runtime.Sessions[index]
		if session.InteractionID != interactionID {
			continue
		}
		session.Respondable = false
		session.InteractionID = ""
		session.PermissionSummary = ""
		session.PermissionQueueCount = 0
	}
}

func (controller *Controller) ExplainSession(sessionID int64) {
	controller.mu.Lock()
	reason := "当前 Codex 会话无法精确跳转"
	if controller.last != nil {
		for _, session := range controller.last.Runtime.Sessions {
			if session.ID == sessionID && session.JumpReason != "" {
				reason = session.JumpReason
				break
			}
		}
	}
	controller.setStatusLocked(reason)
	controller.mu.Unlock()
	controller.machine.HoldFailure()
	controller.publish()
}

func (controller *Controller) OpenDashboard() error {
	if err := controller.runner.Run("open", controller.dashboardURL); err != nil {
		return fmt.Errorf("打开仪表盘: %w", err)
	}
	controller.machine.Dismiss()
	return nil
}

func (controller *Controller) PresentStatus(message string) {
	controller.mu.Lock()
	controller.setStatusLocked(message)
	controller.mu.Unlock()
	controller.publish()
}

func (controller *Controller) Stop() {
	controller.machine.Stop()
	controller.presentMu.Lock()
	defer controller.presentMu.Unlock()
	controller.mu.Lock()
	controller.stopped = true
	controller.mu.Unlock()
}

func (controller *Controller) publish() {
	controller.presentMu.Lock()
	defer controller.presentMu.Unlock()
	controller.mu.Lock()
	if controller.stopped {
		controller.mu.Unlock()
		return
	}
	now := controller.now()
	status := controller.operationStatus
	if status != "" && !now.Before(controller.operationUntil) {
		controller.operationStatus = ""
		status = ""
	}
	if status == "" {
		status = controller.runtimeStatus
	}
	last := cloneState(controller.last)
	screen, refreshing := controller.screen, controller.refreshing
	view := BuildView(last, controller.machine.State(), screen, now, refreshing, status)
	view.AnimateFrame = controller.hasFrame && view.Layout.Frame != controller.lastFrame
	controller.lastFrame = view.Layout.Frame
	controller.hasFrame = true
	controller.mu.Unlock()
	controller.present(view)
}

func (controller *Controller) setStatusLocked(message string) {
	controller.operationStatus = message
	controller.operationUntil = controller.now().Add(operationStatusTTL)
}

func refreshStatus(usageErr, quotaErr error) string {
	switch {
	case usageErr == nil && quotaErr == nil:
		return "刷新完成"
	case usageErr == nil:
		return "token 已更新，配额刷新失败"
	case quotaErr == nil:
		return "token 刷新失败"
	default:
		return "token 与配额刷新失败"
	}
}

func cloneState(value *State) *State {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Runtime.Sessions = append([]RuntimeSession(nil), value.Runtime.Sessions...)
	return &copy
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) error { return exec.Command(name, args...).Run() }
