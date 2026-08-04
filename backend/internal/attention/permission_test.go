package attention

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPermissionBrokerRoutesOneActionToOneWaiter(t *testing.T) {
	broker := NewPermissionBroker(time.Second)
	defer broker.Close()
	result := make(chan PermissionAction, 1)
	go func() {
		action, _ := broker.Wait(context.Background(), PermissionRequest{
			InteractionID: "exact-interaction-1", ExternalSessionID: "session-1",
			ToolName: "Bash", Summary: "Bash · make verify",
		})
		result <- action
	}()
	request := waitPermission(t, broker, "session-1", 1)
	if request.InteractionID != "exact-interaction-1" || request.Summary != "Bash · make verify" {
		t.Fatalf("活动授权请求错误: %+v", request)
	}
	if err := broker.Submit(context.Background(), request.InteractionID, PermissionAllow); err != nil {
		t.Fatal(err)
	}
	if action := <-result; action != PermissionAllow {
		t.Fatalf("waiter 收到 %q，期望 allow", action)
	}
	if _, ok := broker.First("session-1"); ok {
		t.Fatal("已处理请求仍留在活动队列")
	}
	if err := broker.Submit(context.Background(), request.InteractionID, PermissionDeny); !errors.Is(err, ErrPermissionResolved) {
		t.Fatalf("重复处理错误 = %v，期望 ErrPermissionResolved", err)
	}
}

func TestPermissionBrokerQueuesIdenticalRequestsWithoutBroadcast(t *testing.T) {
	broker := NewPermissionBroker(time.Second)
	defer broker.Close()
	results := make(chan PermissionAction, 2)
	for index := 0; index < 2; index++ {
		go func() {
			action, _ := broker.Wait(context.Background(), PermissionRequest{
				ExternalSessionID: "same-session", ToolName: "Bash", Summary: "Bash · same command",
			})
			results <- action
		}()
	}
	first := waitPermission(t, broker, "same-session", 2)
	if err := broker.Submit(context.Background(), first.InteractionID, PermissionDeny); err != nil {
		t.Fatal(err)
	}
	second := waitPermission(t, broker, "same-session", 1)
	if second.InteractionID == first.InteractionID {
		t.Fatal("队列中的相同请求复用了 interaction ID")
	}
	if err := broker.Submit(context.Background(), second.InteractionID, PermissionAllow); err != nil {
		t.Fatal(err)
	}
	seen := make(map[PermissionAction]int)
	seen[<-results]++
	seen[<-results]++
	if seen[PermissionAllow] != 1 || seen[PermissionDeny] != 1 {
		t.Fatalf("相同请求被广播或丢失: %+v", seen)
	}
}

func TestPermissionBrokerIsolatesSessionsAndCleansFallbacks(t *testing.T) {
	broker := NewPermissionBroker(25 * time.Millisecond)
	defer broker.Close()
	timedOut := make(chan PermissionAction, 1)
	go func() {
		action, _ := broker.Wait(context.Background(), PermissionRequest{ExternalSessionID: "timeout"})
		timedOut <- action
	}()
	waitPermission(t, broker, "timeout", 1)
	if action := <-timedOut; action != PermissionHandoff {
		t.Fatalf("超时结果 = %q，期望 handoff", action)
	}
	if _, ok := broker.First("timeout"); ok {
		t.Fatal("超时请求未清理")
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan PermissionAction, 1)
	go func() {
		action, _ := broker.Wait(ctx, PermissionRequest{ExternalSessionID: "cancel"})
		canceled <- action
	}()
	waitPermission(t, broker, "cancel", 1)
	cancel()
	if action := <-canceled; action != PermissionHandoff {
		t.Fatalf("取消结果 = %q，期望 handoff", action)
	}
}

func TestPermissionBrokerCloseReleasesAllWaiters(t *testing.T) {
	broker := NewPermissionBroker(time.Second)
	results := make(chan PermissionAction, 2)
	for _, sessionID := range []string{"first", "second"} {
		go func(sessionID string) {
			action, _ := broker.Wait(context.Background(), PermissionRequest{ExternalSessionID: sessionID})
			results <- action
		}(sessionID)
		waitPermission(t, broker, sessionID, 1)
	}
	broker.Close()
	for index := 0; index < 2; index++ {
		if action := <-results; action != PermissionHandoff {
			t.Fatalf("关闭结果 = %q，期望 handoff", action)
		}
	}
	if err := broker.Submit(context.Background(), "missing", PermissionAllow); !errors.Is(err, ErrPermissionClosed) {
		t.Fatalf("关闭后的提交错误 = %v", err)
	}
}

func waitPermission(t *testing.T, broker *PermissionBroker, sessionID string, queueCount int) PermissionRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if request, ok := broker.First(sessionID); ok && request.QueueCount == queueCount {
			return request
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 session %q 的授权请求超时", sessionID)
	return PermissionRequest{}
}
