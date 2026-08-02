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
static void doraPlayAttentionSound(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[[NSSound soundNamed:@"Glass"] play];
	});
}
*/
import "C"

import (
	"context"
	_ "embed"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	menuReloadInterval      = time.Minute
	attentionReloadInterval = time.Second
)

//go:embed icon-template.png
var templateIcon []byte

type Config struct {
	Loader       Loader
	Refresher    Refresher
	DashboardURL string
	Jumper       SessionJumper
	Quit         func()
}

type SoundNotifier struct{}

func (SoundNotifier) Notify(context.Context, domain.AttentionRequest) error {
	C.doraPlayAttentionSound()
	return nil
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
		controller.SetSessionJumper(config.Jumper)
		go handleMenuEvents(ctx, controller, items, config.Quit)
		controller.LoadAsync(ctx)
		controller.LoadAttentionAsync(ctx)
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
	attentionHeader                   *systray.MenuItem
	waiting                           map[int64]*waitingMenuItem
	waitingClicks                     chan int64
	header, today, sevenDays, allTime *systray.MenuItem
	fiveHour, sevenDay, status        *systray.MenuItem
	refresh, open, quit               *systray.MenuItem
}

type waitingMenuItem struct {
	item *systray.MenuItem
}

func newMenuItems() *menuItems {
	items := &menuItems{waiting: make(map[int64]*waitingMenuItem), waitingClicks: make(chan int64, 64)}
	items.attentionHeader = systray.AddMenuItem("需要关注", "Codex 正在等待你的操作")
	items.attentionHeader.Hide()
	items.header = systray.AddMenuItem("Dora", "")
	items.today = systray.AddMenuItem("今日：—", "")
	items.sevenDays = systray.AddMenuItem("7 日：—", "")
	items.allTime = systray.AddMenuItem("全部：—", "")
	systray.AddSeparator()
	items.fiveHour = systray.AddMenuItem("Codex 5 小时配额：暂无数据", "")
	items.sevenDay = systray.AddMenuItem("Codex 7 日配额：暂无数据", "")
	systray.AddSeparator()
	items.status = systray.AddMenuItem("状态：正在连接本地服务", "")
	items.refresh = systray.AddMenuItem("刷新数据", "重新扫描 token 并刷新配额")
	items.open = systray.AddMenuItem("打开仪表盘", "使用默认浏览器打开 Dora")
	items.quit = systray.AddMenuItem("退出 Dora", "停止 Dora")
	for _, item := range []*systray.MenuItem{items.header, items.today, items.sevenDays, items.allTime, items.fiveHour, items.sevenDay, items.status} {
		item.Disable()
	}
	return items
}

func (items *menuItems) present(view View) {
	systray.SetTitle(view.Title)
	items.header.SetTitle(view.Header)
	if len(view.Waiting) > 0 {
		items.attentionHeader.SetTitle(view.AttentionHeader)
		items.attentionHeader.Show()
	} else {
		items.attentionHeader.Hide()
	}
	active := make(map[int64]struct{}, len(view.Waiting))
	for _, row := range view.Waiting {
		active[row.SessionID] = struct{}{}
		slot := items.waiting[row.SessionID]
		if slot == nil {
			slot = &waitingMenuItem{item: items.attentionHeader.AddSubMenuItem(row.Title, "跳转到对应 Codex 会话")}
			items.waiting[row.SessionID] = slot
			go func(sessionID int64, item *systray.MenuItem) {
				for range item.ClickedCh {
					items.waitingClicks <- sessionID
				}
			}(row.SessionID, slot.item)
		} else {
			slot.item.SetTitle(row.Title)
			slot.item.Show()
		}
	}
	for sessionID, slot := range items.waiting {
		if _, ok := active[sessionID]; !ok {
			slot.item.Remove()
			delete(items.waiting, sessionID)
		}
	}
	items.today.SetTitle(view.Today)
	items.sevenDays.SetTitle(view.SevenDays)
	items.allTime.SetTitle(view.AllTime)
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
	attentionTicker := time.NewTicker(attentionReloadInterval)
	defer attentionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			controller.Stop()
			return
		case <-systray.TrayOpenedCh:
			controller.LoadAsync(ctx)
		case <-ticker.C:
			controller.LoadAsync(ctx)
		case <-attentionTicker.C:
			controller.LoadAttentionAsync(ctx)
		case sessionID := <-items.waitingClicks:
			controller.JumpAttentionSessionAsync(ctx, sessionID)
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
