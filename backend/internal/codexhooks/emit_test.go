package codexhooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

type fixedDetector struct{ surface Surface }

func (detector fixedDetector) Detect() Surface { return detector.surface }

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

func TestParseHookEventSanitizesSensitiveFields(t *testing.T) {
	input := `{
  "session_id":"session-1",
  "turn_id":"turn-1",
  "cwd":"/Users/example/secret-project",
  "hook_event_name":"PermissionRequest",
  "model":"gpt-test",
  "tool_name":"Bash",
  "tool_input":{"command":"rm -rf private"},
  "prompt":"never store me",
  "transcript_path":"/private/session.jsonl"
}`
	event, err := parseHookEvent(strings.NewReader(input), Surface{
		Name: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal, TTY: "/dev/ttys003",
	})
	if err != nil {
		t.Fatalf("parseHookEvent() 失败: %v", err)
	}
	if event.CWDBasename != "secret-project" || event.InputHash == "" || event.ToolName != "Bash" {
		t.Fatalf("事件字段错误: %+v", event)
	}
	serialized := event.SessionID + event.TurnID + event.CWDBasename + event.Model + event.ToolName + event.InputHash
	for _, secret := range []string{"rm -rf private", "never store me", "/Users/example"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("净化事件泄漏 %q: %+v", secret, event)
		}
	}
}

func TestPermissionRequestHashUsesCanonicalToolInput(t *testing.T) {
	first := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PermissionRequest","model":"gpt","tool_name":"Bash","tool_input":{"command":"git status","nested":{"b":2,"a":1}}}`
	second := `{ "tool_input": { "nested": { "a": 1, "b": 2 }, "command": "git status" }, "tool_name":"Bash", "model":"gpt", "hook_event_name":"PermissionRequest", "cwd":"/tmp/dora", "turn_id":"t", "session_id":"s" }`
	firstEvent, err := parseHookEvent(strings.NewReader(first), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	secondEvent, err := parseHookEvent(strings.NewReader(second), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	if firstEvent.InputHash != secondEvent.InputHash {
		t.Fatalf("等价 tool_input hash 不稳定: %s != %s", firstEvent.InputHash, secondEvent.InputHash)
	}
	firstDomain, _ := firstEvent.Domain(time.Unix(1, 0))
	secondDomain, _ := secondEvent.Domain(time.Unix(2, 0))
	if firstDomain.EventKey != secondDomain.EventKey {
		t.Fatalf("等价授权事件 key 不稳定: %s != %s", firstDomain.EventKey, secondDomain.EventKey)
	}
}

func TestPermissionRequestHashDoesNotRoundLargeIntegers(t *testing.T) {
	base := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PermissionRequest","model":"gpt","tool_name":"Tool","tool_input":{"value":%s}}`
	first, err := parseHookEvent(strings.NewReader(fmt.Sprintf(base, "9007199254740992")), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseHookEvent(strings.NewReader(fmt.Sprintf(base, "9007199254740993")), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	if first.InputHash == second.InputHash {
		t.Fatal("不同大整数被舍入为相同授权事件 hash")
	}
}

func TestParseHookEventUsesToolUseIDForQuestion(t *testing.T) {
	input := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PreToolUse","model":"gpt","tool_name":"request_user_input","tool_use_id":"tool-1","tool_input":{"questions":[]}}`
	event, err := parseHookEvent(strings.NewReader(input), Surface{Name: domain.CodexSurfaceApp})
	if err != nil || event.ToolUseID != "tool-1" || event.InputHash != "" {
		t.Fatalf("request_user_input 解析错误: %+v, %v", event, err)
	}
}

func TestParseHookEventRejectsOversizedInput(t *testing.T) {
	input := strings.NewReader(strings.Repeat("x", maxInputBytes+1))
	if _, err := parseHookEvent(input, Surface{Name: domain.CodexSurfaceUnknown}); err == nil {
		t.Fatal("超大 Hook 输入未被拒绝")
	}
}

func TestEmitterTreatsUnavailableServiceAsSilentCondition(t *testing.T) {
	emitter := &Emitter{
		endpoint: "http://127.0.0.1:1/api/v1/hooks/codex",
		client:   &http.Client{Transport: failingTransport{}},
		detector: fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}},
	}
	err := emitter.Emit(context.Background(), strings.NewReader(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"Stop","model":"gpt"}`))
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("Emit() = %v，期望 ErrServiceUnavailable", err)
	}
}

func TestEmitterNeverFollowsRedirectsOutsideLoopback(t *testing.T) {
	var received atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received.Store(true)
	}))
	defer receiver.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	emitter := NewEmitter()
	emitter.endpoint = redirector.URL
	emitter.detector = fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}}
	err := emitter.Emit(context.Background(), strings.NewReader(`{"session_id":"private-session","cwd":"/tmp/dora","hook_event_name":"Stop","model":"gpt"}`))
	if err == nil {
		t.Fatal("Emit() 接受了重定向响应")
	}
	if received.Load() {
		t.Fatal("Emit() 将事件跟随重定向发送到了第二个服务")
	}
}

func TestParseHookEventRejectsTrailingJSON(t *testing.T) {
	input := io.MultiReader(
		strings.NewReader(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"Stop"}`),
		strings.NewReader(` {}`),
	)
	if _, err := parseHookEvent(input, Surface{Name: domain.CodexSurfaceUnknown}); err == nil {
		t.Fatal("多段 JSON 未被拒绝")
	}
}
