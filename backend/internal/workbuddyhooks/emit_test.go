package workbuddyhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
)

func TestWorkBuddyProjectionAndQuestionLifecycle(t *testing.T) {
	raw := `{"session_id":"task-1","hook_event_name":"PreToolUse","agent_type":"workbuddy","cwd":"/Users/private/project","tool_name":"AskUserQuestion","call_id":"call-1","tool_input":{"questions":[{"question":"private secret"}]},"transcript_path":"/private/transcript","prompt":"private prompt","last_assistant_message":"private reply"}`
	event, err := parseHookEvent(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if event.SubagentEvent || event.CWDBasename != "project" || event.ToolName != "request_user_input" {
		t.Fatalf("错误归一化: %+v", event)
	}
	body, _ := json.Marshal(event)
	for _, secret := range []string{"private", "call-1", "prompt", "transcript", "questions"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("泄露 %s: %s", secret, body)
		}
	}
	first, err := Domain(event, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != domain.WorkBuddySource || !strings.HasPrefix(first.EventKey, "workbuddy:") {
		t.Fatalf("错误来源或 key: %+v", first)
	}
	post, err := parseHookEvent(strings.NewReader(strings.ReplaceAll(raw, "PreToolUse", "PostToolUseFailure")))
	if err != nil {
		t.Fatal(err)
	}
	if post.HookEvent != "PostToolUse" || post.ToolUseKey != event.ToolUseKey || post.ToolName != event.ToolName {
		t.Fatalf("提问结束无法关联: %+v", post)
	}
	permission, err := parseHookEvent(strings.NewReader(strings.ReplaceAll(raw, "PreToolUse", "PermissionRequest")))
	if err != nil {
		t.Fatal(err)
	}
	other, err := Domain(permission, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if other.EventKey != first.EventKey {
		t.Fatal("同一提问的权限事件重复提醒")
	}
}
func TestWorkBuddyChildLifecycleCannotClearParent(t *testing.T) {
	for _, name := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
		_, err := parseHookEvent(strings.NewReader(`{"session_id":"parent","agent_id":"child","hook_event_name":"` + name + `"}`))
		if !errors.Is(err, errIgnoredEvent) {
			t.Fatalf("child %s: %v", name, err)
		}
	}
	event, err := parseHookEvent(strings.NewReader(`{"session_id":"parent","agent_id":"child","hook_event_name":"Stop"}`))
	if err != nil || !event.SubagentEvent || !strings.HasPrefix(event.SubagentScope, "sha256:") {
		t.Fatalf("child Stop: %+v %v", event, err)
	}
}
func TestWorkBuddyRejectsAmbiguousOrOversizedInput(t *testing.T) {
	for _, raw := range []string{
		`{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
		`{"hook_event_name":"UserPromptSubmit"}`, `{} {}`, strings.Repeat("x", maxInputBytes+1),
	} {
		if _, err := parseHookEvent(strings.NewReader(raw)); err == nil {
			t.Fatal("应拒绝输入")
		}
	}
	for _, name := range []string{"Notification", "TaskCompleted", "PreToolUse"} {
		_, err := parseHookEvent(strings.NewReader(`{"session_id":"s","hook_event_name":"` + name + `","tool_name":"Read"}`))
		if !errors.Is(err, errIgnoredEvent) {
			t.Fatalf("不相关事件 %s: %v", name, err)
		}
	}
}
func TestWorkBuddyEmitterDoesNotRedirectOrBlockWhenOffline(t *testing.T) {
	received := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received++ }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	e := NewEmitter()
	e.endpoint = origin.URL
	err := e.Emit(context.Background(), strings.NewReader(`{"session_id":"s","hook_event_name":"Stop"}`))
	if err == nil || received != 0 {
		t.Fatalf("错误重定向: %v %d", err, received)
	}
	origin.Close()
	if err := e.Emit(context.Background(), strings.NewReader(`{"session_id":"s","hook_event_name":"Stop"}`)); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatal(err)
	}
}
func TestWorkBuddyDomainRejectsUnprojectedFields(t *testing.T) {
	event := attention.Event{SessionID: "s", HookEvent: "Stop", Surface: domain.WorkBuddySurfaceApp, PromptPreview: "private"}
	if _, err := Domain(event, time.Now()); err == nil {
		t.Fatal("应拒绝未经脱敏的字段")
	}
}

func TestPermissionNotificationProjectsOnlyFixedToolName(t *testing.T) {
	event, err := parseHookEvent(strings.NewReader(`{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"s","message":"needs your permission to use AskUserQuestion","cwd":"/private/project","prompt":"secret"}`))
	if err != nil || event.HookEvent != "PermissionNotification" || event.ToolName != "request_user_input" || event.ToolUseKey != "" {
		t.Fatalf("%+v %v", event, err)
	}
	normalized, err := Domain(event, time.Now())
	if err != nil || !normalized.WaitingNotification || normalized.EventKey != "" {
		t.Fatalf("%+v %v", normalized, err)
	}
	data, _ := json.Marshal(event)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "needs your permission") {
		t.Fatalf("泄露原文 %s", data)
	}
	for _, input := range []string{
		`{"hook_event_name":"Notification","notification_type":"idle_prompt","session_id":"s","message":"needs your permission to use Bash"}`,
		`{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"s","message":"user question?"}`,
		`{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"s","message":"needs your permission to use Bash\nsecret"}`,
	} {
		if _, err := parseHookEvent(strings.NewReader(input)); !errors.Is(err, errIgnoredEvent) {
			t.Fatalf("未忽略不匹配的通知: %v", err)
		}
	}
}
