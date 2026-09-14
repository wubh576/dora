package menubar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/approval"
)

func TestExpiredApprovalsAreRemovedFromView(t *testing.T) {
	now := time.Now()
	state := &State{Runtime: RuntimeState{Sessions: []RuntimeSession{{ID: 1, State: "waiting"}}, Approvals: []approval.Pending{
		{ID: 5, SessionID: 1, ExpiresAt: now.Add(-time.Second)},
		{ID: 6, SessionID: 1, ExpiresAt: now.Add(time.Minute)},
	}}}
	view := BuildView(state, MachineState{}, ScreenMetrics{}, now, false, "")
	if len(view.Approvals) != 1 || view.Approvals[0].ID != 6 || view.Sessions[0].ApprovalCount != 1 {
		t.Fatalf("过期审批仍展示: %+v", view)
	}
}

type unavailableApprovalLoader struct{}

func (unavailableApprovalLoader) Load(context.Context) (State, error) {
	return State{}, errors.New("offline")
}
func (unavailableApprovalLoader) LoadRuntime(context.Context) (RuntimeState, error) {
	return RuntimeState{}, errors.New("offline")
}

func TestDisconnectedUIReleasesApprovalDetails(t *testing.T) {
	for _, mode := range []string{"runtime", "full", "refresh"} {
		views := make(chan View, 10)
		controller := NewController(unavailableApprovalLoader{}, fakeRefresher{}, "", func(view View) { views <- view })
		controller.last = &State{Runtime: RuntimeState{Approvals: []approval.Pending{{ID: 5, Detail: "private", ExpiresAt: time.Now().Add(time.Minute)}}}}
		if mode == "full" {
			controller.LoadAsync(context.Background())
		} else if mode == "refresh" {
			controller.RefreshAsync(context.Background())
			<-views // 刷新开始时的即时渲染。
		} else {
			controller.LoadRuntimeAsync(context.Background())
		}
		select {
		case view := <-views:
			if len(view.Approvals) != 0 {
				t.Fatal("断连仍展示审批详情")
			}
		case <-time.After(time.Second):
			t.Fatal("未更新界面")
		}
		controller.Stop()
	}
}
