//go:build darwin && cgo

package menubar

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void doraIslandStart(double compactMinimumWidth, double compactWingWidth);
void doraIslandStop(void);
void doraIslandPresent(const char *payload);
void doraIslandPlayAttentionSound(void);
int doraIslandPointerInside(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"sync"
	"time"
	"unsafe"

	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	viewReloadInterval    = time.Minute
	runtimeReloadInterval = time.Second
)

type Config struct {
	Loader          Loader
	Refresher       Refresher
	DashboardURL    string
	Jumper          SessionJumper
	AttentionEvents <-chan AttentionSignal
	Quit            func()
}

type AttentionSignal struct {
	RequestID int64
	SessionID int64
}

type SoundNotifier struct {
	events chan AttentionSignal
}

func NewSoundNotifier() *SoundNotifier {
	return &SoundNotifier{events: make(chan AttentionSignal, 64)}
}

func (notifier *SoundNotifier) Events() <-chan AttentionSignal { return notifier.events }

func (notifier *SoundNotifier) Notify(_ context.Context, request domain.AttentionRequest) error {
	C.doraIslandPlayAttentionSound()
	select {
	case notifier.events <- AttentionSignal{RequestID: request.ID, SessionID: request.RuntimeSessionID}:
	default:
	}
	return nil
}

type bridgeEvents struct {
	interaction chan bridgeEvent
	screen      chan ScreenMetrics
}

type bridgeEvent struct {
	kind  int
	value int64
}

var activeBridge struct {
	sync.RWMutex
	events *bridgeEvents
}

// Run 必须由已经锁定到 macOS 主线程的 main goroutine 调用。
func Run(ctx context.Context, config Config) error {
	events := &bridgeEvents{
		interaction: make(chan bridgeEvent, 64),
		screen:      make(chan ScreenMetrics, 8),
	}
	activeBridge.Lock()
	activeBridge.events = events
	activeBridge.Unlock()

	present := func(view View) {
		payload, err := json.Marshal(view)
		if err != nil {
			return
		}
		value := C.CString(string(payload))
		C.doraIslandPresent(value)
		C.free(unsafe.Pointer(value))
	}
	controller := NewController(config.Loader, config.Refresher, config.DashboardURL, present)
	controller.SetSessionJumper(config.Jumper)
	controller.SetPointerChecker(func() bool { return C.doraIslandPointerInside() != 0 })
	done := make(chan struct{})
	go func() {
		runIslandEvents(ctx, controller, config, events)
		close(done)
		C.doraIslandStop()
	}()
	C.doraIslandStart(C.double(compactMinimumWidth), C.double(compactWingWidth))
	controller.Stop()
	activeBridge.Lock()
	activeBridge.events = nil
	activeBridge.Unlock()
	<-done
	return nil
}

func runIslandEvents(ctx context.Context, controller *Controller, config Config, events *bridgeEvents) {
	viewTicker := time.NewTicker(viewReloadInterval)
	defer viewTicker.Stop()
	runtimeTicker := time.NewTicker(runtimeReloadInterval)
	defer runtimeTicker.Stop()
	controller.LoadAsync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-viewTicker.C:
			controller.LoadAsync(ctx)
		case <-runtimeTicker.C:
			controller.LoadRuntimeAsync(ctx)
		case signal := <-config.AttentionEvents:
			if controller.NotifyAttention(signal.RequestID, signal.SessionID) {
				controller.LoadRuntimeAsync(ctx)
			}
		case screen := <-events.screen:
			controller.SetScreen(screen)
		case event := <-events.interaction:
			switch event.kind {
			case 1:
				controller.Hover(true)
			case 2:
				controller.Hover(false)
			case 3:
				controller.RefreshAsync(ctx)
			case 4:
				if err := controller.OpenDashboard(); err != nil {
					controller.PresentStatus("无法打开仪表盘")
				}
			case 5:
				if config.Quit != nil {
					config.Quit()
				}
				return
			case 6:
				controller.JumpSessionAsync(ctx, event.value)
			case 7:
				controller.UIInteraction(true)
			case 8:
				controller.UIInteraction(false)
			case 9:
				controller.ExplainSession(event.value)
			}
		}
	}
}

//export doraIslandOnEvent
func doraIslandOnEvent(kind C.int, value C.longlong) {
	activeBridge.RLock()
	events := activeBridge.events
	activeBridge.RUnlock()
	if events == nil {
		return
	}
	select {
	case events.interaction <- bridgeEvent{kind: int(kind), value: int64(value)}:
	default:
	}
}

//export doraIslandOnScreen
func doraIslandOnScreen(x, y, width, height, visibleX, visibleY, visibleWidth, visibleHeight, safeTop, menuBarThickness, notchWidth C.double) {
	activeBridge.RLock()
	events := activeBridge.events
	activeBridge.RUnlock()
	if events == nil {
		return
	}
	metrics := ScreenMetrics{
		Frame:            Rect{X: float64(x), Y: float64(y), Width: float64(width), Height: float64(height)},
		Visible:          Rect{X: float64(visibleX), Y: float64(visibleY), Width: float64(visibleWidth), Height: float64(visibleHeight)},
		SafeTop:          float64(safeTop),
		MenuBarThickness: float64(menuBarThickness),
		NotchWidth:       float64(notchWidth),
	}
	select {
	case events.screen <- metrics:
	default:
	}
}
