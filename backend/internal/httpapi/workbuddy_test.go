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

	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestWorkBuddyWaitingIsIsolatedFromCodexAndNotifiedOnce(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewHandler(store)
	now := time.Now()
	toolKey := "sha256:" + strings.Repeat("a", 64)
	post := func(provider, name, tool string, child bool) {
		t.Helper()
		surface := domain.WorkBuddySurfaceApp
		if provider == "codex" {
			surface = domain.CodexSurfaceApp
		}
		event := attention.Event{SessionID: "shared-id", HookEvent: name, Surface: surface, ToolName: tool, CWDBasename: "probe"}
		if tool != "" {
			event.ToolUseKey = toolKey
		}
		if child {
			event.SubagentEvent = true
			event.SubagentScope = "sha256:" + strings.Repeat("b", 64)
		}
		body, _ := json.Marshal(event)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/"+provider, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s %s: %d %s", provider, name, w.Code, w.Body.String())
		}
	}
	post("codex", "UserPromptSubmit", "", false)
	post("workbuddy", "UserPromptSubmit", "", false)
	post("codex", "PermissionRequest", "Bash", false)
	post("workbuddy", "PermissionRequest", "Bash", false)
	post("workbuddy", "PermissionRequest", "Bash", false)
	requests, err := store.ClaimUnnotifiedAttention(ctx, now)
	if err != nil || len(requests) != 2 {
		t.Fatalf("跨来源去重出错: %+v %v", requests, err)
	}
	if pending, err := store.ClaimUnnotifiedAttention(ctx, now); err != nil || len(pending) != 0 {
		t.Fatalf("重复通知: %+v %v", pending, err)
	}
	post("workbuddy", "PostToolUse", "Bash", false)
	waiting, err := store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 1 || waiting[0].Session.Provider != domain.CodexSource {
		t.Fatalf("WorkBuddy 完成影响了 Codex: %+v %v", waiting, err)
	}
	// 同一提问同时产生 PreToolUse 和归一后的权限事件时只能有一条提醒。
	post("workbuddy", "PreToolUse", "request_user_input", true)
	post("workbuddy", "PreToolUse", "request_user_input", true)
	post("workbuddy", "Stop", "", true)
	waiting, err = store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("子任务没有解除自己的等待: %+v %v", waiting, err)
	}
	post("workbuddy", "PreToolUse", "request_user_input", false)
	post("workbuddy", "Stop", "", true)
	waiting, err = store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 2 {
		t.Fatalf("子任务结束清除了父等待: %+v %v", waiting, err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runtime", nil))
	if strings.Contains(w.Body.String(), "shared-id") || strings.Contains(w.Body.String(), toolKey) {
		t.Fatal("Runtime API 泄露定位字段")
	}
	if !strings.Contains(w.Body.String(), "WorkBuddy 等待回答") || !strings.Contains(w.Body.String(), `"jumpable":true`) {
		t.Fatalf("缺少等待文案或精确跳转: %s", w.Body.String())
	}
	post("workbuddy", "SessionEnd", "", false)
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.Provider != domain.CodexSource {
		t.Fatalf("结束清理未隔离: %+v %v", active, err)
	}
}
func TestWorkBuddyEndpointRejectsRawDataAndWrongProvider(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewHandler(store)
	for _, body := range []string{
		`{"sessionId":"s","hookEvent":"Stop","surface":"codex_app"}`,
		`{"sessionId":"s","hookEvent":"Stop","surface":"workbuddy_app","prompt":"private"}`,
		`{"sessionId":"s","hookEvent":"Stop","surface":"workbuddy_app","tty":"/dev/ttys1"}`,
		`{"sessionId":"s","hookEvent":"Other","surface":"workbuddy_app"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/workbuddy", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应拒绝: %d %s", w.Code, body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", strings.NewReader(`{"sessionId":"s","hookEvent":"Stop","surface":"workbuddy_app"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatal("Codex 接受了 WorkBuddy surface")
	}
}

func TestWorkBuddyNotificationLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewHandler(store)
	post := func(name, tool, key, scope string) {
		t.Helper()
		event := attention.Event{SessionID: "notification-task", HookEvent: name, Surface: domain.WorkBuddySurfaceApp, ToolName: tool, ToolUseKey: key, SubagentScope: scope}
		body, _ := json.Marshal(event)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/workbuddy", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	check := func(state string, requests int) {
		t.Helper()
		sessions, err := store.RuntimeSessions(ctx)
		if err != nil || len(sessions) != 1 || sessions[0].Session.State != state || sessions[0].RequestCount != requests {
			t.Fatalf("state=%s requests=%d: %+v %v", state, requests, sessions, err)
		}
	}
	claim := func(want int) int64 {
		t.Helper()
		requests, err := store.ClaimUnnotifiedAttention(ctx, time.Now())
		if err != nil || len(requests) != want {
			t.Fatalf("notifications want %d got %+v %v", want, requests, err)
		}
		if len(requests) > 0 {
			return requests[0].ID
		}
		return 0
	}
	key := "sha256:" + strings.Repeat("a", 64)
	post("UserPromptSubmit", "", "", "")
	post("SessionStart", "", "", "")
	check(domain.RuntimeStateRunning, 0)
	post("PermissionNotification", "request_user_input", "", "")
	first := claim(1)
	post("PermissionNotification", "request_user_input", "", "")
	post("SessionStart", "", "", "")
	check(domain.RuntimeStateWaiting, 1)
	claim(0)
	post("PreToolUse", "request_user_input", key, "")
	check(domain.RuntimeStateWaiting, 1)
	claim(0)
	waiting, _ := store.WaitingSessions(ctx)
	if waiting[0].Latest.ID != first {
		t.Fatal("升级精确标识更换了请求")
	}
	post("PermissionNotification", "request_user_input", "", "")
	claim(0)
	post("PostToolUse", "Bash", key, "")
	check(domain.RuntimeStateWaiting, 1)
	post("PostToolUse", "request_user_input", key, "")
	check(domain.RuntimeStateRunning, 0)
	// 同工具下一次没有 call_id 的等待是新周期，结束事件可唯一关联。
	post("PermissionNotification", "request_user_input", "", "")
	if claim(1) == first {
		t.Fatal("新等待复用了旧请求")
	}
	post("PostToolUse", "request_user_input", "sha256:"+strings.Repeat("b", 64), "")
	check(domain.RuntimeStateRunning, 0)
	// 精确事件先到，随后 Notification 不重复；另一个 child 互不影响。
	post("PermissionRequest", "Bash", key, "")
	claim(1)
	post("PermissionNotification", "Bash", "", "")
	claim(0)
	child := "sha256:" + strings.Repeat("c", 64)
	post("PermissionNotification", "Bash", "", child)
	check(domain.RuntimeStateWaiting, 2)
	claim(1)
	post("PostToolUse", "Bash", key, child)
	check(domain.RuntimeStateWaiting, 1)
	post("PostToolUse", "Bash", key, "")
	check(domain.RuntimeStateRunning, 0)
	post("SessionEnd", "", "", "")
	sessions, _ := store.RuntimeSessions(ctx)
	if len(sessions) != 0 {
		t.Fatal("SessionEnd 未清理定位")
	}
}
