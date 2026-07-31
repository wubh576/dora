//go:build darwin && cgo

package menubar

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
static void doraSetAccessoryPolicy(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}
*/
import "C"

import (
	"context"
	_ "embed"
	"sync"
	"time"

	"fyne.io/systray"
)

const menuReloadInterval = time.Minute

//go:embed icon-template.png
var templateIcon []byte

type Config struct {
	Loader       Loader
	Refresher    Refresher
	DashboardURL string
	Quit         func()
}

// Run 必须由已经锁定到 macOS 主线程的 main goroutine 调用。
func Run(ctx context.Context, config Config) error {
	C.doraSetAccessoryPolicy()
	ready := make(chan struct{})
	var controller *Controller
	var readyOnce sync.Once
	go func() {
		<-ready
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(func() {
		systray.SetTemplateIcon(templateIcon, templateIcon)
		systray.SetTitle("Dora")
		systray.SetTooltip("Dora AI 编程用量")
		systray.SetRemovalAllowed(false)
		items := newMenuItems()
		controller = NewController(config.Loader, config.Refresher, config.DashboardURL, items.present)
		go handleMenuEvents(ctx, controller, items, config.Quit)
		controller.LoadAsync(ctx)
		readyOnce.Do(func() { close(ready) })
	}, func() {
		if controller != nil {
			controller.Stop()
		}
		if config.Quit != nil {
			config.Quit()
		}
		readyOnce.Do(func() { close(ready) })
	})
	return nil
}

type menuItems struct {
	header, today, sevenDays, allTime, topModel *systray.MenuItem
	fiveHour, sevenDay, status                  *systray.MenuItem
	refresh, open, quit                         *systray.MenuItem
}

func newMenuItems() *menuItems {
	items := &menuItems{
		header:    systray.AddMenuItem("Dora", ""),
		today:     systray.AddMenuItem("今日：—", ""),
		sevenDays: systray.AddMenuItem("7 日：—", ""),
		allTime:   systray.AddMenuItem("全部：—", ""),
		topModel:  systray.AddMenuItem("模型：暂无数据", ""),
	}
	systray.AddSeparator()
	items.fiveHour = systray.AddMenuItem("5 小时配额：暂无数据", "")
	items.sevenDay = systray.AddMenuItem("7 日配额：暂无数据", "")
	systray.AddSeparator()
	items.status = systray.AddMenuItem("状态：正在连接本地服务", "")
	items.refresh = systray.AddMenuItem("刷新数据", "重新扫描 token 并刷新配额")
	items.open = systray.AddMenuItem("打开仪表盘", "使用默认浏览器打开 Dora")
	items.quit = systray.AddMenuItem("退出 Dora", "停止 Dora")
	for _, item := range []*systray.MenuItem{items.header, items.today, items.sevenDays, items.allTime, items.topModel, items.fiveHour, items.sevenDay, items.status} {
		item.Disable()
	}
	return items
}

func (items *menuItems) present(view View) {
	systray.SetTitle(view.Title)
	items.header.SetTitle(view.Header)
	items.today.SetTitle(view.Today)
	items.sevenDays.SetTitle(view.SevenDays)
	items.allTime.SetTitle(view.AllTime)
	items.topModel.SetTitle(view.TopModel)
	items.fiveHour.SetTitle(view.FiveHour)
	items.sevenDay.SetTitle(view.SevenDay)
	items.status.SetTitle(view.Status)
	if view.Refreshing {
		items.refresh.SetTitle("正在刷新…")
		items.refresh.Disable()
	} else {
		items.refresh.SetTitle("刷新数据")
		items.refresh.Enable()
	}
}

func handleMenuEvents(ctx context.Context, controller *Controller, items *menuItems, quit func()) {
	ticker := time.NewTicker(menuReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			controller.Stop()
			return
		case <-systray.TrayOpenedCh:
			controller.LoadAsync(ctx)
		case <-ticker.C:
			controller.LoadAsync(ctx)
		case <-items.refresh.ClickedCh:
			controller.RefreshAsync(ctx)
		case <-items.open.ClickedCh:
			if err := controller.OpenDashboard(); err != nil {
				controller.PresentStatus("无法打开仪表盘")
			}
		case <-items.quit.ClickedCh:
			controller.Stop()
			if quit != nil {
				quit()
			}
			return
		}
	}
}
