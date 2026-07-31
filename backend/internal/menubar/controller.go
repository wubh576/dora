package menubar

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type Refresher interface {
	Refresh(context.Context) (usageErr, quotaErr error)
}

type CommandRunner interface {
	Run(string, ...string) error
}

type Presenter func(View)

type Controller struct {
	loader       Loader
	refresher    Refresher
	dashboardURL string
	runner       CommandRunner
	present      Presenter
	now          func() time.Time

	presentMu  sync.Mutex
	mu         sync.Mutex
	last       *State
	version    uint64
	loading    bool
	refreshing bool
	stopped    bool
}

func NewController(loader Loader, refresher Refresher, dashboardURL string, present Presenter) *Controller {
	return &Controller{
		loader:       loader,
		refresher:    refresher,
		dashboardURL: dashboardURL,
		runner:       execRunner{},
		present:      present,
		now:          time.Now,
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
	c.version++
	version := c.version
	last := cloneState(c.last)
	c.mu.Unlock()
	c.publish(version, BuildView(last, c.now(), true, ""))
	go func() {
		usageErr, quotaErr := c.refresher.Refresh(ctx)
		state, loadErr := c.loader.Load(ctx)
		c.mu.Lock()
		if version != c.version || c.stopped {
			c.mu.Unlock()
			return
		}
		if loadErr == nil {
			c.last = &state
		}
		last := cloneState(c.last)
		c.refreshing = false
		c.mu.Unlock()
		status := refreshStatus(usageErr, quotaErr)
		if loadErr != nil {
			status = "连接本地服务失败"
		}
		c.publish(version, BuildView(last, c.now(), false, status))
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
	last, version := cloneState(c.last), c.version
	refreshing, stopped := c.refreshing, c.stopped
	c.mu.Unlock()
	if !stopped {
		c.publish(version, BuildView(last, c.now(), refreshing, message))
	}
}

func (c *Controller) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.version++
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
		c.last = &state
	}
	last := cloneState(c.last)
	c.loading = false
	c.mu.Unlock()
	status := ""
	if err != nil {
		status = "连接本地服务失败"
	}
	c.publish(version, BuildView(last, c.now(), false, status))
}

// publish 串行提交菜单视图，并在真正写 UI 前再次丢弃已经过期的异步结果。
func (c *Controller) publish(version uint64, view View) {
	c.presentMu.Lock()
	defer c.presentMu.Unlock()
	c.mu.Lock()
	current := version == c.version && !c.stopped
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
