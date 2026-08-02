package codexhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type fixedDetector struct{ surface Surface }

func (detector fixedDetector) Detect() Surface { return detector.surface }

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
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
	serialized := event.SessionID + event.TurnID + event.CWDBasename + event.Model + event.ToolName + event.InputHash + event.PromptPreview
	for _, secret := range []string{"rm -rf private", "never store me", "/Users/example"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("净化事件泄漏 %q: %+v", secret, event)
		}
	}
}

func TestParseVerifiedHookFixtures(t *testing.T) {
	tests := []struct {
		file      string
		event     string
		tool      string
		toolUseID string
		needsHash bool
	}{
		{file: "session-start.json", event: "SessionStart"},
		{file: "user-prompt-submit.json", event: "UserPromptSubmit"},
		{file: "permission-request.json", event: "PermissionRequest", tool: "Bash", needsHash: true},
		{file: "request-user-input-pre.json", event: "PreToolUse", tool: "request_user_input", toolUseID: "fixture-tool"},
		{file: "request-user-input-post.json", event: "PostToolUse", tool: "request_user_input", toolUseID: "fixture-tool"},
		{file: "stop.json", event: "Stop"},
		{file: "session-end.json", event: "SessionEnd"},
	}
	for _, test := range tests {
		t.Run(test.event, func(t *testing.T) {
			input, err := os.Open("testdata/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			event, err := parseHookEvent(input, Surface{
				Name: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalITerm2, TTY: "/dev/ttys099",
			})
			if err != nil {
				t.Fatalf("解析 fixture 失败: %v", err)
			}
			if event.SessionID != "fixture-session" || event.CWDBasename != "dora" ||
				event.HookEvent != test.event || event.ToolName != test.tool || event.ToolUseID != test.toolUseID {
				t.Fatalf("fixture 解析结果错误: %+v", event)
			}
			if (event.InputHash != "") != test.needsHash {
				t.Fatalf("fixture input hash 错误: %q", event.InputHash)
			}
			if test.event == "UserPromptSubmit" && event.PromptPreview != "帮我实现 灵动岛 状态" {
				t.Fatalf("fixture prompt 未在 helper 出站前净化: %q", event.PromptPreview)
			}
		})
	}
}

func TestVerifiedCancelSequenceResolvesOnNextPrompt(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	parseFixture := func(file string, at time.Time) domain.CodexHookEvent {
		t.Helper()
		input, err := os.Open("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		event, err := parseHookEvent(input, Surface{Name: domain.CodexSurfaceApp})
		closeErr := input.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("解析 fixture 失败: %v；关闭 fixture: %v", err, closeErr)
		}
		domainEvent, err := event.Domain(at)
		if err != nil {
			t.Fatal(err)
		}
		return domainEvent
	}

	permission := parseFixture("permission-request.json", now)
	if created, err := store.ApplyCodexHookEvent(ctx, permission); err != nil || !created {
		t.Fatalf("PermissionRequest = created %t, err %v", created, err)
	}
	if waiting, err := store.WaitingSessions(ctx); err != nil || len(waiting) != 1 {
		t.Fatalf("取消前 waiting = %+v, %v", waiting, err)
	}

	// CLI 取消授权没有额外 Hook；下一条 prompt 是最早可观察的解除事件。
	nextPrompt := parseFixture("user-prompt-submit.json", now.Add(time.Minute))
	if _, err := store.ApplyCodexHookEvent(ctx, nextPrompt); err != nil {
		t.Fatal(err)
	}
	if waiting, err := store.WaitingSessions(ctx); err != nil || len(waiting) != 0 {
		t.Fatalf("UserPromptSubmit 后 waiting = %+v, %v", waiting, err)
	}
	if state, err := store.RuntimeSessionState(ctx, permission.ExternalSessionID); err != nil || state != domain.RuntimeStateRunning {
		t.Fatalf("UserPromptSubmit 后 state = %q, %v", state, err)
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

func TestEmitterBoundsPromptBeforeLoopbackRequest(t *testing.T) {
	var body []byte
	emitter := &Emitter{
		endpoint: "http://127.0.0.1:8080/api/v1/hooks/codex",
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var err error
			body, err = io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
		detector: fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}},
	}
	rawPrompt := strings.Repeat("x", 100_000)
	input := `{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":"` + rawPrompt + `"}`
	if err := emitter.Emit(context.Background(), strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if len(body) >= 64<<10 {
		t.Fatalf("helper 请求正文未限制在 API 上限内: %d bytes", len(body))
	}
	var event struct {
		PromptPreview string `json:"promptPreview"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	if event.PromptPreview != strings.Repeat("x", 160) {
		t.Fatalf("prompt preview 长度或内容错误: %d", len(event.PromptPreview))
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
