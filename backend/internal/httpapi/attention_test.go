package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestCodexHookAndAttentionAPIUsePersistedState(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(store, Options{Now: func() time.Time { return now }})

	body := `{
		"sessionId":"session-api",
		"hookEvent":"PermissionRequest",
		"turnId":"turn-api",
		"cwdBasename":"/Users/private/work/dora",
		"model":"gpt-test",
		"surface":"codex_app",
		"toolName":"Bash",
		"inputHash":"sha256:fixture"
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("Hook API 状态码 = %d；响应: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("Attention API 状态码 = %d；响应: %s", recorder.Code, recorder.Body.String())
	}
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("解析 Attention API 失败: %v", err)
	}
	if response.WaitingCount != 1 || len(response.Sessions) != 1 {
		t.Fatalf("等待 session 数错误: %+v", response)
	}
	session := response.Sessions[0]
	if session.Surface != "codex_app" || session.CWDBasename != "dora" || session.Kind != "dangerous_command" || session.WaitSeconds != 0 {
		t.Fatalf("等待 session 内容错误: %+v", session)
	}

	// 相同事件只保留一条 request。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("重复 Hook API 状态码 = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("解析重复事件结果失败: %v", err)
	}
	if response.Sessions[0].RequestCount != 1 {
		t.Fatalf("重复事件产生多条 request: %+v", response.Sessions[0])
	}
}

func TestCodexHookAPIRejectsContentWithoutPersistingIt(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()
	handler := NewHandler(store)

	tests := []struct {
		name, contentType, body string
		status                  int
	}{
		{name: "非 JSON", contentType: "text/plain", body: `{}`, status: http.StatusUnsupportedMediaType},
		{name: "未知内容字段", contentType: "application/json", body: `{"sessionId":"s","hookEvent":"Stop","surface":"unknown","prompt":"secret"}`, status: http.StatusBadRequest},
		{name: "等待缺少稳定标识", contentType: "application/json", body: `{"sessionId":"s","hookEvent":"PermissionRequest","surface":"unknown","toolName":"Bash"}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("状态码 = %d，期望 %d；响应: %s", recorder.Code, test.status, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "secret") {
				t.Fatal("错误响应回显了 Hook 内容")
			}
		})
	}
}
