package menubar

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
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

type Controller struct {
	loader       Loader
	refresher    Refresher
	dashboardURL string
	runner       CommandRunner
	present      Presenter
	now          func() time.Time
	jumper       SessionJumper
	jumpTimeout  time.Duration

	presentMu        sync.Mutex
	mu               sync.Mutex
	last             *State
	version          uint64
	presentation     uint64
	loading          bool
	attentionLoading bool
	attentionVersion uint64
	refreshing       bool
	jumping          bool
	operationStatus  string
	operationUntil   time.Time
	stopped          bool
}

func (c *Controller) SetSessionJumper(jumper SessionJumper) {
	c.mu.Lock()
	c.jumper = jumper
	c.mu.Unlock()
}

func NewController(loader Loader, refresher Refresher, dashboardURL string, present Presenter) *Controller {
	return &Controller{
		loader:       loader,
		refresher:    refresher,
		dashboardURL: dashboardURL,
		runner:       execRunner{},
		present:      present,
		now:          time.Now,
		jumpTimeout:  jumpTimeout,
	}
}

func (c *Controller) LoadAsync(ctx context.Context) bool {
	c.mu.Lock()
	if c.loading || c.refreshing || c.stopped {
		c.mu.Unlock()
		return false
	}
	c.loading = true
	c.version++
	version := c.version
	c.mu.Unlock()
	go c.load(ctx, version)
	return true
}

func (c *Controller) RefreshAsync(ctx context.Context) bool {
	c.mu.Lock()
	if c.refreshing || c.stopped {
		c.mu.Unlock()
		return false
	}
	c.refreshing = true
	c.loading = false
	c.operationStatus = ""
	c.operationUntil = time.Time{}
	c.version++
	version := c.version
	last := cloneState(c.last)
	c.presentation++
	presentation := c.presentation
	c.mu.Unlock()
	c.publish(presentation, BuildView(last, c.now(), true, ""))
	go func() {
		usageErr, quotaErr := c.refresher.Refresh(ctx)
		state, loadErr := c.loader.Load(ctx)
		c.mu.Lock()
		if version != c.version || c.stopped {
			c.mu.Unlock()
			return
		}
		if loadErr == nil {
			state.Attention = attentionFrom(c.last)
			c.last = &state
		}
		last := cloneState(c.last)
		c.refreshing = false
		c.presentation++
		presentation := c.presentation
		c.mu.Unlock()
		status := refreshStatus(usageErr, quotaErr)
		if loadErr != nil {
			status = "连接本地服务失败"
		}
		c.publish(presentation, BuildView(last, c.now(), false, status))
	}()
	return true
}

func (c *Controller) LoadAttentionAsync(ctx context.Context) bool {
	loader, ok := c.loader.(AttentionLoader)
	if !ok {
		return false
	}
	c.mu.Lock()
	if c.attentionLoading || c.stopped {
		c.mu.Unlock()
		return false
	}
	c.attentionLoading = true
	c.attentionVersion++
	attentionVersion := c.attentionVersion
	c.mu.Unlock()
	go func() {
		attention, err := loader.LoadAttention(ctx)
		now := c.now()
		c.mu.Lock()
		if attentionVersion != c.attentionVersion || c.stopped {
			c.mu.Unlock()
			return
		}
		c.attentionLoading = false
		if err != nil {
			c.mu.Unlock()
			return
		}
		if c.last == nil {
			c.last = &State{}
		}
		c.last.Attention = attention
		last, refreshing := cloneState(c.last), c.refreshing
		status := c.operationStatusLocked(now)
		c.presentation++
		presentation := c.presentation
		c.mu.Unlock()
		c.publish(presentation, BuildView(last, now, refreshing, status))
	}()
	return true
}

func (c *Controller) JumpAttentionSessionAsync(ctx context.Context, sessionID int64) bool {
	c.mu.Lock()
	jumper := c.jumper
	if jumper == nil || c.jumping || c.stopped {
		c.mu.Unlock()
		if jumper == nil {
			c.PresentStatus("Codex 跳转服务未配置")
		}
		return false
	}
	c.jumping = true
	c.operationStatus = ""
	c.operationUntil = time.Time{}
	timeout := c.jumpTimeout
	c.mu.Unlock()
	go func() {
		jumpCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := jumper.JumpAttentionSession(jumpCtx, sessionID)
		if err != nil {
			c.PresentStatus(fmt.Sprintf("跳转 Codex 会话失败：%v", err))
		}
		c.mu.Lock()
		c.jumping = false
		c.mu.Unlock()
		c.LoadAttentionAsync(ctx)
	}()
	return true
}

func (c *Controller) OpenDashboard() error {
	if err := c.runner.Run("open", c.dashboardURL); err != nil {
		return fmt.Errorf("打开仪表盘: %w", err)
	}
	return nil
}

func (c *Controller) PresentStatus(message string) {
	c.mu.Lock()
	now := c.now()
	c.operationStatus = message
	c.operationUntil = now.Add(operationStatusTTL)
	last := cloneState(c.last)
	refreshing, stopped := c.refreshing, c.stopped
	c.presentation++
	presentation := c.presentation
	c.mu.Unlock()
	if !stopped {
		c.publish(presentation, BuildView(last, now, refreshing, message))
	}
}

func (c *Controller) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.version++
	c.presentation++
	c.mu.Unlock()
}

func (c *Controller) load(ctx context.Context, version uint64) {
	state, err := c.loader.Load(ctx)
	c.mu.Lock()
	if version != c.version || c.stopped {
		c.mu.Unlock()
		return
	}
	if err == nil {
		state.Attention = attentionFrom(c.last)
		c.last = &state
	}
	last := cloneState(c.last)
	c.loading = false
	status := c.operationStatusLocked(c.now())
	c.presentation++
	presentation := c.presentation
	c.mu.Unlock()
	if err != nil {
		status = "连接本地服务失败"
	}
	c.publish(presentation, BuildView(last, c.now(), false, status))
}

func (c *Controller) operationStatusLocked(now time.Time) string {
	if c.operationStatus != "" && now.Before(c.operationUntil) {
		return c.operationStatus
	}
	c.operationStatus = ""
	c.operationUntil = time.Time{}
	return ""
}

func attentionFrom(state *State) AttentionState {
	if state == nil {
		return AttentionState{}
	}
	return state.Attention
}

// publish 串行提交菜单视图，并在真正写 UI 前再次丢弃已经过期的异步结果。
func (c *Controller) publish(presentation uint64, view View) {
	c.presentMu.Lock()
	defer c.presentMu.Unlock()
	c.mu.Lock()
	current := presentation == c.presentation && !c.stopped
	c.mu.Unlock()
	if current {
		c.present(view)
	}
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
	return &copy
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) error { return exec.Command(name, args...).Run() }
