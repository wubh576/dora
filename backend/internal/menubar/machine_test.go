package menubar

import (
	"testing"
	"time"
)

type manualTimer struct {
	callback func()
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

func (scheduler *manualScheduler) AfterFunc(_ time.Duration, callback func()) timer {
	timer := &manualTimer{callback: callback}
	scheduler.timers = append(scheduler.timers, timer)
	return timer
}

func TestMachineHoverAndAttentionLifecycle(t *testing.T) {
	scheduler := &manualScheduler{}
	var changes []MachineState
	machine := newMachine(scheduler, func(state MachineState) { changes = append(changes, state) })
	machine.Hover(true)
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("hover state = %+v", state)
	}
	machine.Hover(false)
	if state := machine.State(); state.Mode != ModeHover || len(scheduler.timers) != 1 {
		t.Fatalf("hover exit 未延迟: %+v", state)
	}
	machine.Hover(true)
	scheduler.timers[0].fire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("重新进入后被旧 timer 收起: %+v", state)
	}
	if !machine.Attention(11, 7) || machine.Attention(11, 7) {
		t.Fatal("attention request 去重错误")
	}
	if state := machine.State(); state.Mode != ModeAttention || state.HighlightSessionID != 7 || state.HighlightRequestID != 11 {
		t.Fatalf("attention state = %+v", state)
	}
	machine.Hover(false)
	scheduler.timers[len(scheduler.timers)-1].fire()
	if state := machine.State(); state.Mode != ModeCompact || state.HighlightSessionID != 0 || state.HighlightRequestID != 0 {
		t.Fatalf("attention 自动收起错误: %+v", state)
	}
	if len(changes) == 0 {
		t.Fatal("状态变化未发布")
	}
}

func TestMachineClickAndStopCancelCallbacks(t *testing.T) {
	scheduler := &manualScheduler{}
	changes := 0
	machine := newMachine(scheduler, func(MachineState) { changes++ })
	machine.Attention(1, 2)
	machine.ClickSession()
	before := changes
	scheduler.timers[0].fire()
	if machine.State().Mode != ModeCompact || changes != before {
		t.Fatal("点击后 attention timer 仍发布")
	}
	machine.Hover(true)
	machine.Hover(false)
	machine.Stop()
	before = changes
	scheduler.timers[len(scheduler.timers)-1].fire()
	machine.Hover(true)
	if changes != before || machine.State().Mode != ModeHover {
		t.Fatalf("Stop 后状态或回调改变: state=%+v changes=%d->%d", machine.State(), before, changes)
	}
}

func TestMachineIgnoresLateCallbacksFromReplacedTimers(t *testing.T) {
	scheduler := &manualScheduler{}
	machine := newMachine(scheduler, nil)
	machine.Attention(1, 7)
	oldAttention := scheduler.timers[0]
	machine.Attention(2, 8)
	newAttention := scheduler.timers[1]
	oldAttention.forceFire()
	if state := machine.State(); state.Mode != ModeAttention || state.HighlightSessionID != 8 || state.HighlightRequestID != 2 {
		t.Fatalf("旧 attention callback 提前结束新提示: %+v", state)
	}
	newAttention.fire()
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("新 attention callback 未正常收起: %+v", state)
	}

	machine.Hover(true)
	machine.Hover(false)
	oldCollapse := scheduler.timers[2]
	machine.Hover(true)
	machine.Hover(false)
	newCollapse := scheduler.timers[3]
	oldCollapse.forceFire()
	if state := machine.State(); state.Mode != ModeHover {
		t.Fatalf("旧 hover callback 提前收起新 hover: %+v", state)
	}
	newCollapse.fire()
	if state := machine.State(); state.Mode != ModeCompact {
		t.Fatalf("新 hover callback 未正常收起: %+v", state)
	}
}

func TestMachineAttentionEndsIntoHoverAndNewRequestReopensSameSession(t *testing.T) {
	scheduler := &manualScheduler{}
	machine := newMachine(scheduler, nil)
	if !machine.Attention(1, 7) {
		t.Fatal("首次 attention 未展开")
	}
	machine.Hover(true)
	scheduler.timers[0].fire()
	if state := machine.State(); state.Mode != ModeHover || state.HighlightRequestID != 0 {
		t.Fatalf("attention 到期时鼠标仍在却未保留 hover: %+v", state)
	}
	if !machine.Attention(2, 7) {
		t.Fatal("同一 session 的新 request 未再次展开")
	}
	if state := machine.State(); state.Mode != ModeAttention || state.HighlightSessionID != 7 || state.HighlightRequestID != 2 {
		t.Fatalf("同 session 新 request 状态错误: %+v", state)
	}
}
