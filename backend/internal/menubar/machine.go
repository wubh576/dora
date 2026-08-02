package menubar

import (
	"sync"
	"time"
)

type Mode string

const (
	ModeCompact   Mode = "compact"
	ModeHover     Mode = "expanded_by_hover"
	ModeAttention Mode = "expanded_by_attention"
)

const (
	hoverCollapseDelay = 320 * time.Millisecond
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
	mu                  sync.Mutex
	scheduler           scheduler
	onChange            func(MachineState)
	state               MachineState
	hovering            bool
	collapseTimer       timer
	attentionTimer      timer
	collapseGeneration  uint64
	attentionGeneration uint64
	seenRequests        map[int64]struct{}
	stopped             bool
}

func NewMachine(onChange func(MachineState)) *Machine {
	return newMachine(realScheduler{}, onChange)
}

func newMachine(scheduler scheduler, onChange func(MachineState)) *Machine {
	return &Machine{
		scheduler: scheduler, onChange: onChange,
		state: MachineState{Mode: ModeCompact}, seenRequests: make(map[int64]struct{}),
	}
}

func (machine *Machine) State() MachineState {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return machine.state
}

func (machine *Machine) Hover(inside bool) {
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.hovering = inside
	machine.collapseGeneration++
	if machine.collapseTimer != nil {
		machine.collapseTimer.Stop()
		machine.collapseTimer = nil
	}
	if inside {
		if machine.state.Mode != ModeAttention {
			machine.state.Mode = ModeHover
		}
		state := machine.state
		machine.mu.Unlock()
		machine.changed(state)
		return
	}
	if machine.state.Mode != ModeHover {
		machine.mu.Unlock()
		return
	}
	generation := machine.collapseGeneration
	machine.collapseTimer = machine.scheduler.AfterFunc(hoverCollapseDelay, func() { machine.collapse(generation) })
	machine.mu.Unlock()
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
	machine.attentionGeneration++
	if machine.attentionTimer != nil {
		machine.attentionTimer.Stop()
	}
	if machine.collapseTimer != nil {
		machine.collapseTimer.Stop()
		machine.collapseTimer = nil
	}
	machine.collapseGeneration++
	machine.state = MachineState{Mode: ModeAttention, HighlightSessionID: sessionID, HighlightRequestID: requestID}
	generation := machine.attentionGeneration
	machine.attentionTimer = machine.scheduler.AfterFunc(attentionDuration, func() { machine.endAttention(generation) })
	state := machine.state
	machine.mu.Unlock()
	machine.changed(state)
	return true
}

func (machine *Machine) ClickSession() {
	machine.mu.Lock()
	if machine.stopped {
		machine.mu.Unlock()
		return
	}
	machine.hovering = false
	machine.stopTimersLocked()
	machine.state = MachineState{Mode: ModeCompact}
	state := machine.state
	machine.mu.Unlock()
	machine.changed(state)
}

func (machine *Machine) Stop() {
	machine.mu.Lock()
	machine.stopped = true
	machine.stopTimersLocked()
	machine.mu.Unlock()
}

func (machine *Machine) collapse(generation uint64) {
	machine.mu.Lock()
	if machine.stopped || generation != machine.collapseGeneration || machine.hovering || machine.state.Mode != ModeHover {
		machine.mu.Unlock()
		return
	}
	machine.collapseTimer = nil
	machine.state = MachineState{Mode: ModeCompact}
	state := machine.state
	machine.mu.Unlock()
	machine.changed(state)
}

func (machine *Machine) endAttention(generation uint64) {
	machine.mu.Lock()
	if machine.stopped || generation != machine.attentionGeneration || machine.state.Mode != ModeAttention {
		machine.mu.Unlock()
		return
	}
	machine.attentionTimer = nil
	machine.state.HighlightSessionID = 0
	machine.state.HighlightRequestID = 0
	if machine.hovering {
		machine.state.Mode = ModeHover
	} else {
		machine.state.Mode = ModeCompact
	}
	state := machine.state
	machine.mu.Unlock()
	machine.changed(state)
}

func (machine *Machine) stopTimersLocked() {
	machine.collapseGeneration++
	machine.attentionGeneration++
	if machine.collapseTimer != nil {
		machine.collapseTimer.Stop()
		machine.collapseTimer = nil
	}
	if machine.attentionTimer != nil {
		machine.attentionTimer.Stop()
		machine.attentionTimer = nil
	}
}

func (machine *Machine) changed(state MachineState) {
	if machine.onChange != nil {
		machine.onChange(state)
	}
}
