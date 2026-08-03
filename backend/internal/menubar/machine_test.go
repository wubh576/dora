package menubar

import (
	"testing"
	"time"
)

type manualTimer struct {
	callback func()
	delay    time.Duration
	stopped  bool
}

func (timer *manualTimer) Stop() bool {
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

func (timer *manualTimer) fire() {
	if !timer.stopped {
		timer.stopped = true
		timer.callback()
	}
}

func (timer *manualTimer) forceFire() { timer.callback() }

type manualScheduler struct{ timers []*manualTimer }

func (scheduler *manualScheduler) AfterFunc(delay time.Duration, callback func()) timer {
	timer := &manualTimer{callback: callback, delay: delay}
	scheduler.timers = append(scheduler.timers, timer)
	return timer
}

func TestMachineHoverIntentLeaveDelayAndReentry(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := true
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	machine.Hover(true)
	if state := machine.State(); state.Mode != ModeCompact || len(scheduler.timers) != 1 || scheduler.timers[0].delay != hoverIntentDelay {
		t.Fatalf("hover intent 前状态错误: state=%+v timers=%+v", state, scheduler.timers)
	}
	if hoverIntentDelay < 100*time.Millisecond || hoverIntentDelay > 150*time.Millisecond {
		t.Fatalf("hover intent 延迟超出 100–150ms: %s", hoverIntentDelay)
	}
	scheduler.timers[0].fire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("hover intent 到期未展开: %+v", state)
	}

	pointerInside = false
	machine.Hover(false)
	oldCollapse := scheduler.timers[1]
	if state := machine.State(); state.Mode != ModeHover || oldCollapse.delay != hoverCollapseDelay {
		t.Fatalf("离开未延迟收起: %+v", state)
	}
	pointerInside = true
	machine.Hover(true)
	oldCollapse.forceFire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("重新进入后被旧 timer 收起: %+v", state)
	}

	pointerInside = false
	machine.Hover(false)
	scheduler.timers[2].fire()
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("真正离开后未收起: %+v", state)
	}
}

func TestMachineCollapseRechecksCurrentPanelPointer(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := true
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	machine.Hover(true)
	scheduler.timers[0].fire()
	pointerInside = false
	machine.Hover(false)
	collapse := scheduler.timers[1]

	// tracking area 因窗口动画发出假 mouseExited，但 timer 到期时鼠标仍在新 panel frame 内。
	pointerInside = true
	collapse.fire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("鼠标仍在 panel 内却被收起: %+v", state)
	}
}

func TestMachineAttentionExpiryKeepsHoverAndDeduplicatesRequest(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := false
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	if !machine.Attention(11, 7) || machine.Attention(11, 7) {
		t.Fatal("attention request 去重错误")
	}
	attention := scheduler.timers[0]
	if state := machine.State(); state.Mode != ModeAttention || state.HighlightSessionID != 7 {
		t.Fatalf("attention state = %+v", state)
	}
	pointerInside = true
	attention.fire()
	if state := machine.State(); state.Mode != ModeHover || state.HighlightRequestID != 0 {
		t.Fatalf("attention 到期时鼠标仍在却未保持展开: %+v", state)
	}
	if !machine.Attention(12, 7) {
		t.Fatal("同一 session 的新 request 未重新提醒")
	}
}

func TestMachineInteractionAndFailureAreIndependentExpansionReasons(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := false
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	machine.UIInteraction(true)
	if state := machine.State(); state.Mode != ModeInteraction {
		t.Fatalf("UI interaction 未展开: %+v", state)
	}
	machine.UIInteraction(false)
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("UI interaction 结束后状态错误: %+v", state)
	}
	machine.OperationStart()
	machine.OperationEnd(false)
	if state := machine.State(); state.Mode != ModeInteraction {
		t.Fatalf("失败后未保持展开: %+v", state)
	}
	pointerInside = true
	machine.Hover(true)
	if state := machine.State(); state.Mode != ModeInteraction {
		t.Fatalf("失败后重新进入错误清除了 failure hold: %+v", state)
	}
	pointerInside = false
	machine.Hover(false)
	scheduler.timers[len(scheduler.timers)-1].fire()
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("失败提示在真正离开后未收起: %+v", state)
	}
}

func TestMachineInteractionEndRechecksPointerInsidePanel(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := false
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	machine.UIInteraction(true)
	pointerInside = true
	machine.UIInteraction(false)
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("交互结束时鼠标仍在 panel 内却被收起: %+v", state)
	}

	pointerInside = false
	machine.Hover(false)
	scheduler.timers[len(scheduler.timers)-1].fire()
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("鼠标真正离开后未收起: %+v", state)
	}
}

func TestMachineDismissCollapsesAllReasonsUntilPointerLeaves(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := true
	machine := newMachine(scheduler, func() bool { return pointerInside }, nil)
	machine.Attention(9, 7)
	machine.UIInteraction(true)
	machine.OperationStart()
	machine.Dismiss()
	if state := machine.State(); state.Mode != ModeCompact || state.HighlightRequestID != 0 || state.HighlightSessionID != 0 {
		t.Fatalf("成功跳转后未立即收起: %+v", state)
	}

	// 跳转返回可能早于同一次点击的 mouse-up，旧 hover 也可能在窗口动画期间迟到。
	machine.UIInteraction(false)
	machine.Hover(true)
	for _, timer := range scheduler.timers {
		timer.forceFire()
	}
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("迟到交互重新展开面板: %+v", state)
	}

	pointerInside = false
	machine.Hover(false)
	pointerInside = true
	machine.Hover(true)
	scheduler.timers[len(scheduler.timers)-1].fire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("真正离开后无法再次 hover 展开: %+v", state)
	}
}

func TestMachineIgnoresLateTimersAndStop(t *testing.T) {
	scheduler := &manualScheduler{}
	pointerInside := false
	changes := 0
	machine := newMachine(scheduler, func() bool { return pointerInside }, func(MachineState) { changes++ })
	machine.Attention(1, 7)
	oldAttention := scheduler.timers[0]
	machine.Attention(2, 8)
	oldAttention.forceFire()
	if state := machine.State(); state.Mode != ModeAttention || state.HighlightSessionID != 8 {
		t.Fatalf("旧 attention timer 改变新状态: %+v", state)
	}
	machine.Stop()
	before := changes
	for _, timer := range scheduler.timers {
		timer.forceFire()
	}
	machine.Hover(true)
	if changes != before || machine.State().HighlightSessionID != 8 {
		t.Fatalf("Stop 后迟到 callback 改变状态: state=%+v changes=%d->%d", machine.State(), before, changes)
	}
}
