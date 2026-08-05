package menubar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientLoadsSnapshotQuotaAndUnifiedRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/snapshot":
			_, _ = response.Write([]byte(`{"usage":{"todayTokens":1200,"thirtyDayTokens":3400},"quotas":[],"errors":[]}`))
		case "/api/v1/quotas":
			_, _ = response.Write([]byte(`{"enabled":true,"status":"ready","items":[]}`))
		case "/api/v1/runtime":
			_, _ = response.Write([]byte(`{"waitingCount":1,"runningCount":2,"sessions":[{"id":7,"state":"waiting","sessionName":"dora","requestId":9,"agentId":"private-agent","toolUseId":"private-tool","toolInput":{"command":"private-command"}}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL)
	state, err := client.Load(context.Background())
	if err != nil || state.Snapshot.Usage.TodayTokens != 1200 || state.Snapshot.Usage.ThirtyDayTokens != 3400 || !state.Quota.Enabled {
		t.Fatalf("Load() = %+v, %v", state, err)
	}
	runtimeState, err := client.LoadRuntime(context.Background())
	if err != nil || runtimeState.WaitingCount != 1 || runtimeState.RunningCount != 2 || runtimeState.Sessions[0].RequestID != 9 {
		t.Fatalf("LoadRuntime() = %+v, %v", runtimeState, err)
	}
	view := BuildView(
		&State{Runtime: runtimeState}, MachineState{Mode: ModeAttention},
		ScreenMetrics{Frame: Rect{Width: 1440, Height: 900}, Visible: Rect{Width: 1440, Height: 876}},
		time.Now(), false, "",
	)
	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-agent", "private-tool", "private-command", "toolInput"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("菜单栏 View 保留了 Runtime API 内部字段 %q: %s", secret, payload)
		}
	}
}

func TestClientKeepsTokenSnapshotWhenQuotaFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/snapshot" {
			_, _ = response.Write([]byte(`{"usage":{"todayTokens":88}}`))
			return
		}
		http.Error(response, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	state, err := NewClient(server.URL).Load(context.Background())
	if err != nil || state.Snapshot.Usage.TodayTokens != 88 || state.Quota.Status != "error" {
		t.Fatalf("Load() = %+v, %v", state, err)
	}
}
