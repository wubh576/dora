package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	attentiondomain "github.com/wubh576/dora/backend/internal/attention"
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

func TestRuntimeAPICombinesRunningAndWaitingWithoutPrivateIdentifiers(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 10, 30, 0, 0, time.UTC)
	handler := NewHandler(store, Options{
		Now: func() time.Time { return now },
	})
	post := func(body string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("Hook 状态码 = %d: %s", response.Code, response.Body.String())
		}
	}
	post(`{"sessionId":"private-running","hookEvent":"UserPromptSubmit","cwdBasename":"/Users/private/work/dora","surface":"codex_app","promptPreview":"实现 灵动岛"}`)
	post(`{"sessionId":"private-waiting","hookEvent":"PermissionRequest","cwdBasename":"/Users/private/work/other","surface":"codex_cli","terminalKind":"terminal","tty":"/dev/ttys999","toolName":"Bash","inputHash":"sha256:runtime"}`)
	if err := store.UpdateRuntimeSessionNames(context.Background(), map[string]string{
		"private-running": "修复菜单栏任务",
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Runtime API 状态码 = %d: %s", response.Code, response.Body.String())
	}
	serialized := response.Body.String()
	var payload runtimeResponse
	if err := json.Unmarshal([]byte(serialized), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WaitingCount != 1 || payload.RunningCount != 1 || len(payload.Sessions) != 2 {
		t.Fatalf("Runtime API 计数错误: %+v", payload)
	}
	if payload.Sessions[0].State != "waiting" || payload.Sessions[0].RequestID <= 0 || !payload.Sessions[0].Jumpable ||
		payload.Sessions[1].SessionName != "修复菜单栏任务" || payload.Sessions[1].PromptPreview != "实现 灵动岛" || !payload.Sessions[1].Jumpable {
		t.Fatalf("Runtime API session 错误: %+v", payload.Sessions)
	}
	active, err := store.RuntimeSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[1].Session.SessionName != "修复菜单栏任务" {
		t.Fatalf("Runtime API 未缓存任务标题: %+v", active)
	}
	for _, private := range []string{"private-running", "private-waiting", "/Users/private", "/dev/ttys999"} {
		if strings.Contains(serialized, private) {
			t.Fatalf("Runtime API 泄露 %q: %s", private, serialized)
		}
	}
}

func TestRuntimeAPIAggregatesSubagentRequestWithoutExposingScope(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewHandler(store)
	post := func(body string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("Hook 状态码 = %d: %s", response.Code, response.Body.String())
		}
	}
	scope := "sha256:" + strings.Repeat("a", 64)
	toolKey := "sha256:" + strings.Repeat("b", 64)
	inputKey := "sha256:" + strings.Repeat("c", 64)
	post(`{"sessionId":"private-parent","hookEvent":"UserPromptSubmit","cwdBasename":"dora","surface":"codex_app","promptPreview":"父任务"}`)
	post(`{"sessionId":"private-parent","hookEvent":"PermissionRequest","turnId":"turn","subagentEvent":true,"subagentScope":"` + scope + `","cwdBasename":"child","surface":"codex_app","toolName":"Bash","toolUseKey":"` + toolKey + `","toolInputKey":"` + inputKey + `","inputHash":"fixture"}`)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil))
	var payload runtimeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WaitingCount != 1 || len(payload.Sessions) != 1 ||
		payload.Sessions[0].Summary != "Subagent 等待授权" || payload.Sessions[0].RequestCount != 1 ||
		payload.Sessions[0].PromptPreview != "父任务" || payload.Sessions[0].CWDBasename != "dora" {
		t.Fatalf("Subagent runtime API 聚合错误: %+v", payload)
	}
	serialized := response.Body.String()
	for _, private := range []string{"private-parent", scope, toolKey, inputKey, "agent_id", "agentId", "tool_input", "toolInput"} {
		if strings.Contains(serialized, private) {
			t.Fatalf("Runtime API 泄露 child 内部定位 %q: %s", private, serialized)
		}
	}
}

func TestRuntimeAPIMarksUnjumpableSessionWithoutPrivateLocator(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := attentiondomain.Event{
		SessionID: "private-unknown", HookEvent: "UserPromptSubmit", CWDBasename: "dora",
		Surface: "unknown", PromptPreview: "检查跳转能力",
	}
	domainEvent, err := event.Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyCodexHookEvent(context.Background(), domainEvent); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil))
	var payload runtimeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 1 || payload.Sessions[0].Jumpable || payload.Sessions[0].JumpReason != "无法识别 Codex 会话来源" {
		t.Fatalf("不可跳转结论错误: %+v", payload.Sessions)
	}
	if strings.Contains(response.Body.String(), "private-unknown") {
		t.Fatalf("Runtime API 泄露 external session ID: %s", response.Body.String())
	}
}

func TestCodexHookAPIRejectsContentWithoutPersistingIt(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()
	var logs bytes.Buffer
	handler := NewHandler(store, Options{Logger: log.New(&logs, "", 0)})

	tests := []struct {
		name, contentType, body string
		status                  int
	}{
		{name: "非 JSON", contentType: "text/plain", body: `{}`, status: http.StatusUnsupportedMediaType},
		{name: "未知内容字段", contentType: "application/json", body: `{"sessionId":"s","hookEvent":"Stop","surface":"unknown","rawPrompt":"secret"}`, status: http.StatusBadRequest},
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
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), "reason=invalid_json") {
		t.Fatalf("Hook 拒绝日志包含正文或缺少诊断原因: %q", logs.String())
	}
}

func TestCodexHookLogsSanitizedStateTransitions(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var logs bytes.Buffer
	handler := NewHandler(store, Options{Logger: log.New(&logs, "", 0)})
	sessionID := "019-private-runtime-session"
	postHook := func(body string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("Hook 状态码 = %d", response.Code)
		}
	}
	postHook(`{"sessionId":"` + sessionID + `","hookEvent":"SessionStart","source":"startup","cwdBasename":"/Users/private/dora","surface":"codex_app"}`)
	toolInputKey := "sha256:" + strings.Repeat("d", 64)
	permission := `{"sessionId":"` + sessionID + `","hookEvent":"PermissionRequest","turnId":"turn","cwdBasename":"/Users/private/dora","surface":"codex_app","toolName":"Bash\tstate=idle attention=resolved\u001b","toolInputKey":"` + toolInputKey + `","inputHash":"sha256:fixture"}`
	for index := 0; index < 2; index++ {
		postHook(permission)
	}
	activity := `{"sessionId":"` + sessionID + `","hookEvent":"PostToolUse","turnId":"turn","cwdBasename":"dora","surface":"codex_app","toolName":"Bash\tstate=idle attention=resolved\u001b"}`
	postHook(activity)
	postHook(permission)

	text := logs.String()
	for _, expected := range []string{
		"session=" + attentiondomain.SessionLabel(sessionID),
		"event=SessionStart", "attention=reconciled_by_session_start",
		`source="startup"`,
		"event=PermissionRequest", "state=waiting", "attention=created", "attention=deduplicated",
		"event=PostToolUse", "state=running", "attention=reconciled_by_tool_completion",
		"attention=ignored_resolved_replay",
		`tool="Bash\tstate=idle attention=resolved\x1b"`,
		`event=PermissionRequest surface=codex_app tool="Bash\tstate=idle attention=resolved\x1b" state=running attention=ignored_resolved_replay`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Hook 日志缺少 %q: %s", expected, text)
		}
	}
	if strings.Contains(text, sessionID) || strings.Contains(text, toolInputKey) || strings.Contains(text, "/Users/private") ||
		strings.Contains(text, "\t") || strings.Contains(text, "\x1b") {
		t.Fatalf("Hook 日志泄露 session 或完整路径: %s", text)
	}
}
