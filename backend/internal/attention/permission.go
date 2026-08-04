package attention

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const PermissionWaitTimeout = 570 * time.Second

type PermissionAction string

const (
	PermissionAllow   PermissionAction = "allow"
	PermissionDeny    PermissionAction = "deny"
	PermissionHandoff PermissionAction = "handoff"
)

var (
	ErrPermissionNotFound = errors.New("Codex 授权请求不存在")
	ErrPermissionResolved = errors.New("Codex 授权请求已经处理")
	ErrPermissionClosed   = errors.New("Codex 授权服务已经关闭")
)

type PermissionRequest struct {
	InteractionID     string
	ExternalSessionID string
	ToolName          string
	Summary           string
	RequestedAt       time.Time
	QueuePosition     int
	QueueCount        int
}

type permissionWaiter struct {
	request PermissionRequest
	result  chan PermissionAction
}

// PermissionBroker 只保存当前进程中仍被 Hook 阻塞的授权请求。
type PermissionBroker struct {
	mu          sync.Mutex
	waitTimeout time.Duration
	waiters     map[string]*permissionWaiter
	queues      map[string][]string
	resolved    map[string]struct{}
	resolvedIDs []string
	closed      bool
}

func NewPermissionBroker(waitTimeout time.Duration) *PermissionBroker {
	if waitTimeout <= 0 {
		waitTimeout = PermissionWaitTimeout
	}
	return &PermissionBroker{
		waitTimeout: waitTimeout,
		waiters:     make(map[string]*permissionWaiter),
		queues:      make(map[string][]string),
		resolved:    make(map[string]struct{}),
	}
}

func (broker *PermissionBroker) Wait(ctx context.Context, request PermissionRequest) (PermissionAction, error) {
	interactionID := request.InteractionID
	if interactionID == "" {
		var err error
		interactionID, err = NewPermissionInteractionID()
		if err != nil {
			return PermissionHandoff, err
		}
	}
	request.InteractionID = interactionID
	if request.RequestedAt.IsZero() {
		request.RequestedAt = time.Now().UTC()
	}
	waiter := &permissionWaiter{request: request, result: make(chan PermissionAction, 1)}

	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return PermissionHandoff, ErrPermissionClosed
	}
	if _, exists := broker.waiters[interactionID]; exists {
		broker.mu.Unlock()
		return PermissionHandoff, errors.New("Codex 授权交互 ID 重复")
	}
	if _, resolved := broker.resolved[interactionID]; resolved {
		broker.mu.Unlock()
		return PermissionHandoff, ErrPermissionResolved
	}
	broker.waiters[interactionID] = waiter
	broker.queues[request.ExternalSessionID] = append(broker.queues[request.ExternalSessionID], interactionID)
	broker.mu.Unlock()

	timer := time.NewTimer(broker.waitTimeout)
	defer timer.Stop()
	select {
	case action := <-waiter.result:
		return action, nil
	case <-ctx.Done():
		broker.finish(interactionID)
		return PermissionHandoff, nil
	case <-timer.C:
		broker.finish(interactionID)
		return PermissionHandoff, nil
	}
}

func (broker *PermissionBroker) Submit(_ context.Context, interactionID string, action PermissionAction) error {
	if action != PermissionAllow && action != PermissionDeny && action != PermissionHandoff {
		return errors.New("Codex 授权操作无效")
	}
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return ErrPermissionClosed
	}
	waiter := broker.waiters[interactionID]
	if waiter == nil {
		_, resolved := broker.resolved[interactionID]
		broker.mu.Unlock()
		if resolved {
			return ErrPermissionResolved
		}
		return ErrPermissionNotFound
	}
	broker.removeLocked(interactionID, waiter.request.ExternalSessionID)
	broker.rememberResolvedLocked(interactionID)
	broker.mu.Unlock()
	waiter.result <- action
	return nil
}

func (broker *PermissionBroker) First(externalSessionID string) (PermissionRequest, bool) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	queue := broker.queues[externalSessionID]
	if len(queue) == 0 {
		return PermissionRequest{}, false
	}
	waiter := broker.waiters[queue[0]]
	if waiter == nil {
		return PermissionRequest{}, false
	}
	request := waiter.request
	request.QueuePosition = 1
	request.QueueCount = len(queue)
	return request, true
}

func (broker *PermissionBroker) Close() {
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return
	}
	broker.closed = true
	waiters := make([]*permissionWaiter, 0, len(broker.waiters))
	for interactionID, waiter := range broker.waiters {
		waiters = append(waiters, waiter)
		broker.rememberResolvedLocked(interactionID)
	}
	broker.waiters = make(map[string]*permissionWaiter)
	broker.queues = make(map[string][]string)
	broker.mu.Unlock()
	for _, waiter := range waiters {
		waiter.result <- PermissionHandoff
	}
}

func (broker *PermissionBroker) finish(interactionID string) {
	broker.mu.Lock()
	waiter := broker.waiters[interactionID]
	if waiter != nil {
		broker.removeLocked(interactionID, waiter.request.ExternalSessionID)
		broker.rememberResolvedLocked(interactionID)
	}
	broker.mu.Unlock()
}

func (broker *PermissionBroker) removeLocked(interactionID, externalSessionID string) {
	delete(broker.waiters, interactionID)
	queue := broker.queues[externalSessionID]
	for index, current := range queue {
		if current != interactionID {
			continue
		}
		queue = append(queue[:index], queue[index+1:]...)
		break
	}
	if len(queue) == 0 {
		delete(broker.queues, externalSessionID)
	} else {
		broker.queues[externalSessionID] = queue
	}
}

func (broker *PermissionBroker) rememberResolvedLocked(interactionID string) {
	const resolvedLimit = 512
	broker.resolved[interactionID] = struct{}{}
	broker.resolvedIDs = append(broker.resolvedIDs, interactionID)
	if len(broker.resolvedIDs) <= resolvedLimit {
		return
	}
	oldest := broker.resolvedIDs[0]
	broker.resolvedIDs = broker.resolvedIDs[1:]
	delete(broker.resolved, oldest)
}

func NewPermissionInteractionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("生成 Codex 授权交互 ID 失败")
	}
	return hex.EncodeToString(value[:]), nil
}
