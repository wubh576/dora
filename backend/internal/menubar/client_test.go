package menubar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientLoadsSnapshotAndQuotaFromLoopbackAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/snapshot":
			_, _ = w.Write([]byte(`{"generatedAt":"2026-07-31T08:00:00Z","usage":{"todayTokens":123,"sevenDayTokens":456,"allTimeTokens":789,"topModel":"gpt-5.6-sol","stale":false,"providers":[{"source":"provider.codex","tokens":500},{"source":"provider.claude-code","tokens":289}]},"quotas":[],"errors":[]}`))
		case "/api/v1/quotas":
			_, _ = w.Write([]byte(`{"enabled":true,"status":"ready","items":[{"windowKey":"five_hour","remainingPercent":72,"sourceState":"confirmed"}]}`))
		case "/api/v1/attention":
			_, _ = w.Write([]byte(`{"waitingCount":1,"sessions":[{"id":7,"provider":"provider.codex","surface":"codex_app","cwdBasename":"dora","summary":"Codex 等待授权","waitSeconds":12,"requestCount":1}]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	state, err := NewClient(server.URL).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() 失败: %v", err)
	}
	if state.Snapshot.Usage.TodayTokens != 123 || state.Snapshot.Usage.TopModel != "gpt-5.6-sol" ||
		len(state.Snapshot.Usage.Providers) != 2 || state.Snapshot.Usage.Providers[1].Tokens != 289 ||
		!state.Quota.Enabled || len(state.Quota.Items) != 1 {
		t.Fatalf("本地 API 状态错误: %+v", state)
	}
}

func TestClientLoadsAttentionIndependently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/attention" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"waitingCount":2,"sessions":[{"id":1},{"id":2}]}`))
	}))
	defer server.Close()
	state, err := NewClient(server.URL).LoadAttention(context.Background())
	if err != nil || state.WaitingCount != 2 || len(state.Sessions) != 2 {
		t.Fatalf("LoadAttention() = %+v, %v", state, err)
	}
}

func TestClientKeepsSnapshotWhenQuotaEndpointFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/snapshot" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage":{"todayTokens":250},"quotas":[],"errors":[]}`))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	state, err := NewClient(server.URL).Load(context.Background())
	if err != nil || state.Snapshot.Usage.TodayTokens != 250 || state.Quota.Status != "error" {
		t.Fatalf("配额失败后快照错误: state=%+v err=%v", state, err)
	}
}

func TestClientRejectsUnavailableSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := NewClient(server.URL).Load(context.Background()); err == nil {
		t.Fatal("Load() 未返回 HTTP 错误")
	}
}
