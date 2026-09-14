// Package approval 只保留仍有 Hook 连接的审批，不持久化命令或决定。
package approval

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	WaitTimeout        = 2 * time.Minute
	HookTimeoutSeconds = 125
	MaxDetailBytes     = 24 << 10
	UILease            = 4 * time.Second
	maxPendingRequests = 64
)

type Pending struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"sessionId"`
	Title     string    `json:"title"`
	Tool      string    `json:"tool"`
	Detail    string    `json:"detail"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Request struct {
	Pending
	EventKey string
	Context  context.Context
	Result   chan string
}

type Broker struct {
	mu       sync.Mutex
	requests map[int64]*Request
	lastUI   time.Time
}

func New() *Broker { return &Broker{requests: make(map[int64]*Request)} }

func (b *Broker) Available() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Since(b.lastUI) <= UILease
}

func (b *Broker) List() []Pending {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastUI = time.Now()
	result := []Pending{}
	for _, r := range b.requests {
		if r.Context.Err() == nil {
			result = append(result, r.Pending)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExpiresAt.Before(result[j].ExpiresAt) })
	return result
}

func (b *Broker) Register(ctx context.Context, value Pending, eventKey string) (*Request, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.lastUI) > UILease || len(b.requests) >= maxPendingRequests {
		return nil, errors.New("审批界面不可用")
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	// 保持 JSON 与 AppKit 整数往返精确，重启后旧按钮也不能命中新请求。
	value.ID = int64(binary.BigEndian.Uint64(raw[:]) & ((1 << 53) - 1))
	if value.ID == 0 || b.requests[value.ID] != nil {
		return nil, errors.New("生成审批标识失败")
	}
	value.ExpiresAt, _ = ctx.Deadline()
	r := &Request{Pending: value, EventKey: eventKey, Context: ctx, Result: make(chan string, 1)}
	b.requests[value.ID] = r
	return r, nil
}

func (b *Broker) Get(id int64) *Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests[id]
}

func (b *Broker) Resolve(id int64, decision string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.requests[id]
	if r == nil {
		return false
	}
	delete(b.requests, id)
	if r.Context.Err() != nil {
		return false
	}
	r.Result <- decision
	return true
}

func (b *Broker) Remove(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.requests, id)
}

func (b *Broker) Requests() []*Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]*Request, 0, len(b.requests))
	for _, r := range b.requests {
		result = append(result, r)
	}
	return result
}
