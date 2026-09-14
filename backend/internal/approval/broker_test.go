package approval

import (
	"context"
	"testing"
	"time"
)

func TestExpiredRequestCannotBeApprovedOrListed(t *testing.T) {
	b := New()
	b.List()
	ctx, cancel := context.WithCancel(context.Background())
	r, err := b.Register(ctx, Pending{}, "event")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if b.Resolve(r.ID, "allow") || len(b.List()) != 0 {
		t.Fatal("已取消请求仍可批准")
	}
	select {
	case <-r.Result:
		t.Fatal("取消请求收到决定")
	default:
	}
}

func TestBrokerRequiresLiveUIAndUsesFreshIDs(t *testing.T) {
	b := New()
	if _, err := b.Register(context.Background(), Pending{}, "event"); err == nil {
		t.Fatal("没有界面仍拦截审批")
	}
	b.List()
	first, err := b.Register(context.Background(), Pending{}, "same event")
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Register(context.Background(), Pending{}, "same event")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("相同输入共用了审批 ID")
	}
	if !b.Resolve(second.ID, "deny") || b.Resolve(second.ID, "allow") {
		t.Fatal("决定不是一次性的")
	}
	select {
	case <-first.Result:
		t.Fatal("决定串到另一个请求")
	default:
	}
	b.lastUI = time.Now().Add(-UILease - time.Second)
	if b.Available() {
		t.Fatal("过期界面仍可用")
	}
}
