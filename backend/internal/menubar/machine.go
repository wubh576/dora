package menubar

import (
	"sync"
	"time"
)

type Mode string

const (
	ModeCompact     Mode = "compact"
	ModeHover       Mode = "expanded_by_hover"
	ModeAttention   Mode = "expanded_by_attention"
	ModeInteraction Mode = "expanded_by_interaction"
)

const (
	hoverIntentDelay   = 100 * time.Millisecond
	hoverCollapseDelay = 450 * time.Millisecond
	attentionDuration  = 6 * time.Second
)

type MachineState struct {
	Mode               Mode
	HighlightSessionID int64
	HighlightRequestID int64
}

type timer interface{ Stop() bool }

type scheduler interface {
	AfterFunc(time.Duration, func()) timer
}

type realScheduler struct{}

func (realScheduler) AfterFunc(delay time.Duration, callback func()) timer {
	return time.AfterFunc(delay, callback)
}

type Machine struct {
	mu            sync.Mutex
	scheduler     scheduler
	pointerInside func() bool
	onChange      func(MachineState)
	state         MachineState

	pointerReported bool
	hoverReady      bool
	attentionActive bool
	uiInteraction   bool
	operationActive bool
	failureHold     bool

	hoverTimer          timer
	collapseTimer       timer
	attentionTimer      timer
	hoverGeneration     uint64
	collapseGeneration  uint64
	attentionGeneration uint64
	seenRequests        map[int64]struct{}
	stopped             bool
}

func NewMachine(onChange func(MachineState)) *Machine {
	return newMachine(realScheduler{}, nil, onChange)
}

func newMachine(scheduler scheduler, pointerInside func() bool, onChange func(MachineState)) *Machine {
	return &Machine{
		scheduler: scheduler, pointerInside: pointerInside, onChange: onChange,
		state: MachineState{Mode: ModeCompact}, seenRequests: make(map[int64]struct{}),
	}
}

func (machine *Machine) SetPointerChecker(check func() bool) {
	machine.mu.Lock()
	machine.pointerInside = check
	machine.mu.Unlock()
}

func (machine *Machine) State() MachineState {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return machine.state
}

// Hover(false) 只表示可能离开；真正收起前会再次读取当前 panel frame 内的鼠标位置。
func (machine *Machine) Hover(inside bool) {
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	if inside {
		machine.pointerReported = true
		machine.cancelCollapseLocked()
		machine.startHoverIntentLocked()
		state, changed := machine.updateStateLocked()
		machine.mu.Unlock()
		machine.changed(state, changed)
		return
	}

	machine.pointerReported = false
	machine.cancelHoverIntentLocked()
	if machine.hoverReady || machine.failureHold {
		machine.startCollapseLocked()
	}
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) Attention(requestID, sessionID int64) bool {
	machine.mu.Lock()
	if machine.stopped || requestID <= 0 || sessionID <= 0 {
		machine.mu.Unlock()
		return false
	}
	if _, exists := machine.seenRequests[requestID]; exists {
		machine.mu.Unlock()
		return false
	}
	machine.seenRequests[requestID] = struct{}{}
	machine.attentionActive = true
	machine.failureHold = false
	machine.cancelCollapseLocked()
	machine.attentionGeneration++
	if machine.attentionTimer != nil {
		machine.attentionTimer.Stop()
	}
	generation := machine.attentionGeneration
	machine.attentionTimer = machine.scheduler.AfterFunc(attentionDuration, func() { machine.endAttention(generation) })
	machine.state.HighlightSessionID = sessionID
	machine.state.HighlightRequestID = requestID
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
	return true
}

func (machine *Machine) UIInteraction(active bool) {
	inside := false
	if !active {
		inside = machine.currentPointerInside()
	}
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.uiInteraction = active
	if active {
		machine.failureHold = false
		machine.cancelCollapseLocked()
	} else {
		machine.pointerReported = inside
		if inside {
			machine.hoverReady = true
			machine.cancelCollapseLocked()
		} else if machine.hoverReady {
			machine.startCollapseLocked()
		}
	}
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) OperationStart() {
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.operationActive = true
	machine.failureHold = false
	machine.cancelCollapseLocked()
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

// OperationEnd(false) 保持展开以展示失败原因，直到用户真正离开整个面板。
func (machine *Machine) OperationEnd(success bool) {
	inside := machine.currentPointerInside()
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.operationActive = false
	machine.pointerReported = inside
	if inside {
		machine.hoverReady = true
	}
	machine.failureHold = !success
	if success && !inside && machine.hoverReady {
		machine.startCollapseLocked()
	}
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) HoldFailure() {
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.failureHold = true
	machine.cancelCollapseLocked()
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) Stop() {
	machine.mu.Lock()
	machine.stopped = true
	machine.stopTimersLocked()
	machine.mu.Unlock()
}

func (machine *Machine) startHoverIntentLocked() {
	if machine.hoverReady || machine.hoverTimer != nil {
		return
	}
	machine.hoverGeneration++
	generation := machine.hoverGeneration
	machine.hoverTimer = machine.scheduler.AfterFunc(hoverIntentDelay, func() { machine.finishHoverIntent(generation) })
}

func (machine *Machine) finishHoverIntent(generation uint64) {
	inside := machine.currentPointerInside()
	machine.mu.Lock()
	if machine.stopped || generation != machine.hoverGeneration {
		machine.mu.Unlock()
		return
	}
	machine.hoverTimer = nil
	machine.pointerReported = inside
	machine.hoverReady = inside
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) startCollapseLocked() {
	machine.cancelCollapseLocked()
	generation := machine.collapseGeneration
	machine.collapseTimer = machine.scheduler.AfterFunc(hoverCollapseDelay, func() { machine.finishCollapse(generation) })
}

func (machine *Machine) finishCollapse(generation uint64) {
	inside := machine.currentPointerInside()
	machine.mu.Lock()
	if machine.stopped || generation != machine.collapseGeneration {
		machine.mu.Unlock()
		return
	}
	machine.collapseTimer = nil
	if inside {
		machine.pointerReported = true
		machine.hoverReady = true
	} else {
		machine.pointerReported = false
		machine.hoverReady = false
		machine.failureHold = false
	}
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) endAttention(generation uint64) {
	inside := machine.currentPointerInside()
	machine.mu.Lock()
	if machine.stopped || generation != machine.attentionGeneration {
		machine.mu.Unlock()
		return
	}
	machine.attentionTimer = nil
	machine.attentionActive = false
	machine.state.HighlightSessionID = 0
	machine.state.HighlightRequestID = 0
	machine.pointerReported = inside
	machine.hoverReady = inside
	state, changed := machine.updateStateLocked()
	machine.mu.Unlock()
	machine.changed(state, changed)
}

func (machine *Machine) updateStateLocked() (MachineState, bool) {
	previous := machine.state
	switch {
	case machine.attentionActive:
		machine.state.Mode = ModeAttention
	case machine.uiInteraction || machine.operationActive || machine.failureHold:
		machine.state.Mode = ModeInteraction
	case machine.hoverReady:
		machine.state.Mode = ModeHover
	default:
		machine.state.Mode = ModeCompact
	}
	return machine.state, machine.state != previous
}

func (machine *Machine) currentPointerInside() bool {
	machine.mu.Lock()
	check, fallback := machine.pointerInside, machine.pointerReported
	machine.mu.Unlock()
	if check == nil {
		return fallback
	}
	return check()
}

func (machine *Machine) cancelHoverIntentLocked() {
	machine.hoverGeneration++
	if machine.hoverTimer != nil {
		machine.hoverTimer.Stop()
		machine.hoverTimer = nil
	}
}

func (machine *Machine) cancelCollapseLocked() {
	machine.collapseGeneration++
	if machine.collapseTimer != nil {
		machine.collapseTimer.Stop()
		machine.collapseTimer = nil
	}
}

func (machine *Machine) stopTimersLocked() {
	machine.cancelHoverIntentLocked()
	machine.cancelCollapseLocked()
	machine.attentionGeneration++
	if machine.attentionTimer != nil {
		machine.attentionTimer.Stop()
		machine.attentionTimer = nil
	}
}

func (machine *Machine) changed(state MachineState, changed bool) {
	if changed && machine.onChange != nil {
		machine.onChange(state)
	}
}
