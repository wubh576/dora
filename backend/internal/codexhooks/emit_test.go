package codexhooks

import (
	"context"
	"database/sql"
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

	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/httpapi"
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
	"agent_id":"private-agent-id",
	"agent_type":"reviewer",
  "cwd":"/Users/example/secret-project",
  "hook_event_name":"PermissionRequest",
  "model":"gpt-test",
  "tool_name":"Bash",
	"tool_use_id":"private-tool-use-id",
  "tool_input":{"command":"rm -rf private","description":"private approval reason"},
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
	serializedBytes, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(serializedBytes)
	for _, secret := range []string{
		"rm -rf private", "private approval reason", "never store me", "/Users/example",
		"private-agent-id", "private-tool-use-id",
	} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("净化事件泄漏 %q: %+v", secret, event)
		}
	}
}

func TestParseVerifiedHookFixtures(t *testing.T) {
	tests := []struct {
		file          string
		schema        string
		event         string
		tool          string
		toolUseID     string
		needsHash     bool
		needsInputKey bool
		subagent      bool
	}{
		{file: "session-start.json", schema: "官方 SessionStart", event: "SessionStart"},
		{file: "user-prompt-submit.json", schema: "官方 UserPromptSubmit", event: "UserPromptSubmit"},
		{file: "permission-request.json", schema: "官方 PermissionRequest（无 child/tool use 字段）", event: "PermissionRequest", tool: "Bash", needsHash: true, needsInputKey: true},
		{file: "request-user-input-pre.json", schema: "官方 PreToolUse", event: "PreToolUse", tool: "request_user_input", toolUseID: "fixture-tool"},
		{file: "request-user-input-post.json", schema: "官方 PostToolUse", event: "PostToolUse", tool: "request_user_input", toolUseID: "fixture-tool", needsInputKey: true},
		{file: "subagent-stop.json", schema: "官方 SubagentStop", event: "SubagentStop", subagent: true},
		{file: "stop.json", schema: "官方 root Stop", event: "Stop"},
		{file: "session-end.json", schema: "官方 root SessionEnd", event: "SessionEnd"},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
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
				event.HookEvent != test.event || event.ToolName != test.tool ||
				event.ToolUseKey != opaqueKey("tool-use", test.toolUseID) {
				t.Fatalf("%s fixture 解析结果错误: %+v", test.schema, event)
			}
			if (event.InputHash != "") != test.needsHash {
				t.Fatalf("fixture input hash 错误: %q", event.InputHash)
			}
			if (event.ToolInputKey != "") != test.needsInputKey {
				t.Fatalf("%s tool input key 错误: %q", test.schema, event.ToolInputKey)
			}
			if test.subagent && (!event.SubagentEvent || event.SubagentScope != subagentScope("fixture-child")) {
				t.Fatalf("subagent scope 未脱敏或不稳定: %q", event.SubagentScope)
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
	if firstEvent.ToolInputKey == "" || firstEvent.ToolInputKey != secondEvent.ToolInputKey {
		t.Fatalf("等价 tool_input key 不稳定: %s != %s", firstEvent.ToolInputKey, secondEvent.ToolInputKey)
	}
	firstDomain, _ := firstEvent.Domain(time.Unix(1, 0))
	secondDomain, _ := secondEvent.Domain(time.Unix(2, 0))
	if firstDomain.EventKey != secondDomain.EventKey {
		t.Fatalf("等价授权事件 key 不稳定: %s != %s", firstDomain.EventKey, secondDomain.EventKey)
	}
}

func TestBashCorrelationKeyUsesCommandWithoutChangingCompleteFingerprint(t *testing.T) {
	withDescription := `{"session_id":"s","turn_id":"t","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make verify","description":"需要在沙箱外运行验证"}}`
	withoutDescription := `{"session_id":"s","turn_id":"t","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make verify"}}`
	post := `{"session_id":"s","turn_id":"t","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"call-1","tool_input":{"command":"make verify"}}`
	parse := func(raw string) attention.Event {
		t.Helper()
		event, err := parseHookEvent(strings.NewReader(raw), Surface{Name: domain.CodexSurfaceCLI})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	permissionWithDescription := parse(withDescription)
	permissionWithoutDescription := parse(withoutDescription)
	completion := parse(post)

	if permissionWithDescription.ToolInputKey == "" ||
		permissionWithDescription.ToolInputKey != permissionWithoutDescription.ToolInputKey ||
		permissionWithDescription.ToolInputKey != completion.ToolInputKey {
		t.Fatalf(
			"Bash command 关联键不一致: with=%q without=%q post=%q",
			permissionWithDescription.ToolInputKey,
			permissionWithoutDescription.ToolInputKey,
			completion.ToolInputKey,
		)
	}
	const historicalCorrelationKey = "sha256:01f2388d28fe96acab0f731794dd8c300bfc9e025fcc3c85f5f512e610e6c210"
	if permissionWithoutDescription.ToolInputKey != historicalCorrelationKey {
		t.Fatalf("既有 Bash command 关联键发生变化: %q", permissionWithoutDescription.ToolInputKey)
	}
	const historicalFingerprint = "9bafe45b5c4eb8a4c38297d4f0f8e6db16418c1615ee697e67a43504d5d6cce2"
	if permissionWithDescription.InputHash != historicalFingerprint ||
		permissionWithDescription.InputHash == permissionWithoutDescription.InputHash {
		t.Fatalf(
			"完整输入指纹语义被改变: with=%q without=%q",
			permissionWithDescription.InputHash, permissionWithoutDescription.InputHash,
		)
	}
	withDomain, err := permissionWithDescription.Domain(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	withoutDomain, err := permissionWithoutDescription.Domain(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if withDomain.EventKey == withoutDomain.EventKey {
		t.Fatal("完整输入不同的 PermissionRequest 被错误合并为同一历史 event key")
	}
}

func TestNonBashCorrelationKeyKeepsDescription(t *testing.T) {
	base := `{"session_id":"s","turn_id":"t","hook_event_name":"PermissionRequest","tool_name":"mcp__server__tool","tool_input":{"query":"same","description":%q}}`
	first, err := parseHookEvent(
		strings.NewReader(fmt.Sprintf(base, "business-a")), Surface{Name: domain.CodexSurfaceCLI},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseHookEvent(
		strings.NewReader(fmt.Sprintf(base, "business-b")), Surface{Name: domain.CodexSurfaceCLI},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ToolInputKey == second.ToolInputKey {
		t.Fatal("非 Bash 工具的业务 description 被错误忽略")
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
	if first.ToolInputKey == second.ToolInputKey {
		t.Fatal("不同大整数被舍入为相同 tool input key")
	}
}

func TestOfficialPermissionAndPostToolInputKeysMatch(t *testing.T) {
	permission := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PermissionRequest","tool_name":"MCP","tool_input":{"value":9007199254740993,"nested":{"b":2,"a":1}}}`
	post := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PostToolUse","tool_name":"MCP","tool_use_id":"call-1","tool_input":{"nested":{"a":1,"b":2},"value":9007199254740993}}`
	permissionEvent, err := parseHookEvent(strings.NewReader(permission), Surface{Name: domain.CodexSurfaceCLI})
	if err != nil {
		t.Fatal(err)
	}
	postEvent, err := parseHookEvent(strings.NewReader(post), Surface{Name: domain.CodexSurfaceCLI})
	if err != nil {
		t.Fatal(err)
	}
	if permissionEvent.ToolInputKey == "" || permissionEvent.ToolInputKey != postEvent.ToolInputKey {
		t.Fatalf("官方 Permission/Post tool_input 未生成相同 key: permission=%q post=%q", permissionEvent.ToolInputKey, postEvent.ToolInputKey)
	}
	serialized, err := json.Marshal(postEvent)
	if err != nil || strings.Contains(string(serialized), "9007199254740993") || strings.Contains(string(serialized), "call-1") {
		t.Fatalf("原始 PostToolUse 关联字段穿过 loopback: %s, %v", serialized, err)
	}
}

func TestParseHookEventHashesToolUseIDForQuestion(t *testing.T) {
	input := `{"session_id":"s","turn_id":"t","cwd":"/tmp/dora","hook_event_name":"PreToolUse","model":"gpt","tool_name":"request_user_input","tool_use_id":"tool-1","tool_input":{"questions":[]}}`
	event, err := parseHookEvent(strings.NewReader(input), Surface{Name: domain.CodexSurfaceApp})
	// 该值由升级前公开版本的 raw tool_use_id 去重口径产生，不能随内部脱敏方式改变。
	const wantEventKey = "codex:5cee1597f41905d06230c5594e470a61070563fb9cc7abba13eb6603d07f6401"
	if err != nil || event.ToolUseKey != opaqueKey("tool-use", "tool-1") ||
		event.EventKey != wantEventKey || event.InputHash != "" {
		t.Fatalf("request_user_input 解析错误: %+v, %v", event, err)
	}
	domainEvent, err := event.Domain(time.Now())
	if err != nil || domainEvent.EventKey != wantEventKey {
		t.Fatalf("root event key 升级兼容性错误: %+v, %v", domainEvent, err)
	}
	serialized, err := json.Marshal(event)
	if err != nil || strings.Contains(string(serialized), "tool-1") {
		t.Fatalf("root tool use ID 穿过 loopback: %s, %v", serialized, err)
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

func TestOfficialPermissionRequestUsesBoundedLoopbackTimeout(t *testing.T) {
	emitter := NewEmitter()
	emitter.detector = fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}}
	emitter.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	started := time.Now()
	err := emitter.Emit(context.Background(), strings.NewReader(`{
		"session_id":"parent","turn_id":"turn",
		"cwd":"/tmp/dora","hook_event_name":"PermissionRequest",
		"tool_name":"Bash","tool_input":{"command":"git status"}
	}`))
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("超时结果 = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 650*time.Millisecond {
		t.Fatalf("Subagent PermissionRequest 阻塞过久: %s", elapsed)
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

func TestParseHookEventKeepsOnlyCodexAppUserRequest(t *testing.T) {
	wrapped := `# Context from my IDE setup:

## Active file: AGENTS.md

# Files mentioned by the user:

## screenshot.png

## My request for Codex:
改成跟随系统实际菜单栏高度？
而且展示真正的用户 prompt。`
	input := fmt.Sprintf(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":%q}`, wrapped)
	event, err := parseHookEvent(strings.NewReader(input), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	if event.PromptPreview != "改成跟随系统实际菜单栏高度？ 而且展示真正的用户 prompt。" {
		t.Fatalf("Codex App 用户 prompt 提取错误: %q", event.PromptPreview)
	}
	for _, injected := range []string{"Context from my IDE", "AGENTS.md", "screenshot.png", "My request for Codex"} {
		if strings.Contains(event.PromptPreview, injected) {
			t.Fatalf("preview 保留了 Codex App 注入上下文 %q: %q", injected, event.PromptPreview)
		}
	}
}

func TestParseHookEventDoesNotInterpretCLIUserTextAsAppWrapper(t *testing.T) {
	prompt := "说明下面这个标题：\n## My request for Codex:\n不要删掉前文"
	input := fmt.Sprintf(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":%q}`, prompt)
	event, err := parseHookEvent(strings.NewReader(input), Surface{Name: domain.CodexSurfaceCLI})
	if err != nil {
		t.Fatal(err)
	}
	if event.PromptPreview != "说明下面这个标题： ## My request for Codex: 不要删掉前文" {
		t.Fatalf("CLI prompt 被当成 App 包装处理: %q", event.PromptPreview)
	}
}

func TestParseHookEventKeepsMarkerQuotedInsideAppUserRequest(t *testing.T) {
	wrapped := `# Context from my IDE setup:

## Active file: AGENTS.md

## My request for Codex:
请解释这个包装标记：
## My request for Codex:
它为什么会出现？`
	input := fmt.Sprintf(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":%q}`, wrapped)
	event, err := parseHookEvent(strings.NewReader(input), Surface{Name: domain.CodexSurfaceApp})
	if err != nil {
		t.Fatal(err)
	}
	want := "请解释这个包装标记： ## My request for Codex: 它为什么会出现？"
	if event.PromptPreview != want {
		t.Fatalf("App 用户正文中的 marker 截断了请求: %q", event.PromptPreview)
	}
}

func TestEmitterEndsOnlyVerifiedBackgroundPrompts(t *testing.T) {
	for _, test := range []struct {
		name      string
		surface   Surface
		prompt    string
		eventName string
	}{
		{
			name: "Codex App background", surface: Surface{Name: domain.CodexSurfaceApp},
			prompt:    "# Overview\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /Users/example/dora",
			eventName: "SessionEnd",
		},
		{
			name: "Codex App user prompt", surface: Surface{Name: domain.CodexSurfaceApp},
			prompt: "帮我修复菜单栏", eventName: "UserPromptSubmit",
		},
		{
			name: "CLI same prefix", surface: Surface{Name: domain.CodexSurfaceCLI},
			prompt:    "# Overview\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /tmp/dora",
			eventName: "UserPromptSubmit",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
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
				detector: fixedDetector{surface: test.surface},
			}
			input := fmt.Sprintf(`{"session_id":"s","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":%q}`, test.prompt)
			if err := emitter.Emit(context.Background(), strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			var event attention.Event
			if err := json.Unmarshal(body, &event); err != nil {
				t.Fatal(err)
			}
			if event.HookEvent != test.eventName {
				t.Fatalf("hookEvent = %q，期望 %q", event.HookEvent, test.eventName)
			}
			if test.eventName == "SessionEnd" && event.PromptPreview != "" {
				t.Fatalf("后台结束事件保留了 prompt preview: %q", event.PromptPreview)
			}
		})
	}
}

func TestBackgroundPromptsRemoveRegisteredRuntimeSession(t *testing.T) {
	t.Run("Ambient Suggestions", func(t *testing.T) {
		ctx := context.Background()
		store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC)
		apply := func(raw string, at time.Time) {
			t.Helper()
			event, err := parseHookEvent(strings.NewReader(raw), Surface{Name: domain.CodexSurfaceApp})
			if err != nil {
				t.Fatal(err)
			}
			event = normalizeCodexAppBackgroundEvent(event)
			domainEvent, err := event.Domain(at)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyCodexHookEvent(ctx, domainEvent); err != nil {
				t.Fatal(err)
			}
		}
		apply(`{"session_id":"background","cwd":"/tmp/dora","hook_event_name":"SessionStart"}`, now)
		apply(`{"session_id":"background","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":"# Overview\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /tmp/dora"}`, now.Add(time.Second))
		if _, err := store.RuntimeSessionState(ctx, "background"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("后台 runtime 未被移除: %v", err)
		}
	})
}

func TestEmitterAggregatesSubagentAttentionWithoutChangingRootMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var requests atomic.Int64
	emitter := &Emitter{
		endpoint: "http://127.0.0.1:8080/api/v1/hooks/codex",
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests.Add(1)
			var event attention.Event
			if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
				return nil, err
			}
			domainEvent, err := event.Domain(time.Date(2026, 8, 3, 4, 0, int(requests.Load()), 0, time.UTC))
			if err != nil {
				return nil, err
			}
			if _, err := store.ApplyCodexHookEvent(ctx, domainEvent); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
		detector: fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}},
	}
	emit := func(value string) {
		t.Helper()
		if err := emitter.Emit(ctx, strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
	const sessionID = "root-session"
	emit(`{"session_id":"root-session","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":"实现根任务"}`)
	emit(`{"session_id":"root-session","cwd":"/tmp/dora","hook_event_name":"SessionEnd","agent_id":"child-1"}`)
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.ExternalSessionID != sessionID ||
		active[0].Session.State != domain.RuntimeStateRunning || active[0].Session.PromptPreview != "实现根任务" {
		t.Fatalf("subagent 事件破坏 running 根 session: %+v, %v", active, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("subagent 事件发起了 loopback 请求: %d", requests.Load())
	}

	// 非标准 child 字段仅作为向后兼容增强路径；两条事件使用相同 agent_type，隔离必须来自 agent_id。
	childA := `{"session_id":"root-session","turn_id":"turn-1","agent_id":"child-a","agent_type":"reviewer","cwd":"/tmp/child-a","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make verify","description":"批准 child A"}}`
	childB := `{"session_id":"root-session","turn_id":"turn-1","agent_id":"child-b","agent_type":"reviewer","cwd":"/tmp/child-b","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_use_id":"call-b","tool_input":{"command":"go test ./..."}}`
	emit(childA)
	emit(childA)
	emit(childB)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].Session.PromptPreview != "实现根任务" || active[0].Session.CWDBasename != "dora" ||
		active[0].RequestCount != 2 || active[0].Latest == nil || active[0].Latest.Summary != "Subagent 等待授权" {
		t.Fatalf("subagent 事件破坏 waiting 根 session: %+v, %v", active, err)
	}
	if unnotified, err := store.UnnotifiedAttention(ctx); err != nil || len(unnotified) != 2 {
		t.Fatalf("两个 child request 未各自进入提醒队列: %+v, %v", unnotified, err)
	}
	if requests.Load() != 4 {
		t.Fatalf("subagent attention loopback 次数 = %d", requests.Load())
	}

	// 普通 child 生命周期不发送 loopback，也不能覆盖父 runtime。
	emit(`{"session_id":"root-session","turn_id":"turn-1","agent_id":"child-a","cwd":"/tmp/child-a","hook_event_name":"UserPromptSubmit","prompt":"child prompt"}`)
	emit(`{"session_id":"root-session","agent_id":"child-a","cwd":"/tmp/child-a","hook_event_name":"SessionEnd"}`)
	if requests.Load() != 4 {
		t.Fatalf("普通 child 生命周期发起了 loopback 请求: %d", requests.Load())
	}

	// child A 的工具完成只能解决自己的请求。
	emit(`{"session_id":"root-session","turn_id":"turn-1","agent_id":"child-a","cwd":"/tmp/child-a","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"call-a","tool_input":{"command":"make verify"}}`)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].RequestCount != 1 || active[0].Session.PromptPreview != "实现根任务" {
		t.Fatalf("child A 完成污染 child B 或父 session: %+v, %v", active, err)
	}

	// 结构化 child 结束只解除同一 scope，最后恢复父任务 running。
	emit(`{"session_id":"root-session","turn_id":"turn-1","agent_id":"child-b","agent_type":"reviewer","cwd":"/tmp/child-b","hook_event_name":"SubagentStop"}`)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateRunning ||
		active[0].Session.PromptPreview != "实现根任务" || active[0].RequestCount != 0 {
		t.Fatalf("全部 child 请求解决后的父 session 错误: %+v, %v", active, err)
	}

	emit(`{"session_id":"root-session","cwd":"/tmp/dora","hook_event_name":"SessionEnd"}`)
	if _, err := store.RuntimeSessionState(ctx, sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("真正的根 SessionEnd 未删除 session: %v", err)
	}
}

func TestSchemaShapedApprovalHooksRemainIndependentlyCorrelated(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	emitter := &Emitter{
		endpoint: "http://127.0.0.1:8080/api/v1/hooks/codex",
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var event attention.Event
			if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
				return nil, err
			}
			domainEvent, err := event.Domain(now)
			if err != nil {
				return nil, err
			}
			now = now.Add(time.Second)
			if _, err := store.ApplyCodexHookEvent(ctx, domainEvent); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
		detector: fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}},
	}
	emit := func(value string) {
		t.Helper()
		if err := emitter.Emit(ctx, strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}

	emit(`{"session_id":"schema-parent","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","model":"parent-model","prompt":"保留父任务"}`)
	// 官方 PermissionRequest 形状：Bash 授权可额外携带给用户看的 description，但没有 child/tool use ID。
	emit(`{"session_id":"schema-parent","turn_id":"turn-1","cwd":"/tmp/child-a","hook_event_name":"PermissionRequest","model":"child-model","tool_name":"Bash","tool_input":{"command":"command-a","description":"批准命令 A"}}`)
	emit(`{"session_id":"schema-parent","turn_id":"turn-1","cwd":"/tmp/child-b","hook_event_name":"PermissionRequest","model":"child-model","tool_name":"Bash","tool_input":{"command":"command-b","description":"批准命令 B"}}`)
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].RequestCount != 2 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].Session.CWDBasename != "dora" || active[0].Session.Model != "parent-model" ||
		active[0].Session.PromptPreview != "保留父任务" || active[0].Latest == nil ||
		active[0].Latest.Summary != "Codex 等待授权" {
		t.Fatalf("两个 schema-shaped request 未独立聚合: %+v, %v", active, err)
	}

	// 官方 PostToolUse 形状：有 tool_use_id 与相同 tool_input，但不保证 child 身份字段。
	emit(`{"session_id":"schema-parent","turn_id":"turn-1","cwd":"/tmp/child-a","model":"child-model","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"call-a","tool_input":{"command":"command-a"}}`)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].RequestCount != 1 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].Session.CWDBasename != "dora" || active[0].Session.Model != "parent-model" {
		t.Fatalf("PostToolUse A 应只解除 request A: %+v, %v", active, err)
	}

	emit(`{"session_id":"schema-parent","turn_id":"turn-1","cwd":"/tmp/child-b","model":"child-model","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"call-b","tool_input":{"command":"command-b"}}`)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].RequestCount != 0 ||
		active[0].Session.State != domain.RuntimeStateRunning || active[0].Session.PromptPreview != "保留父任务" ||
		active[0].Session.CWDBasename != "dora" || active[0].Session.Model != "parent-model" {
		t.Fatalf("全部 request 解除后父状态错误: %+v, %v", active, err)
	}
}

func TestCompactionSourceReachesSQLiteFromRawHookJSON(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)
	nowIndex := 0
	handler := httpapi.NewHandler(store, httpapi.Options{Now: func() time.Time {
		value := base.Add(time.Duration(nowIndex) * time.Second)
		nowIndex++
		return value
	}})
	emitter := &Emitter{
		endpoint: DefaultEndpoint,
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			return recorder.Result(), nil
		})},
		detector: fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}},
	}
	for _, input := range []string{
		`{"session_id":"compact-e2e","source":"startup","cwd":"/tmp/dora","hook_event_name":"SessionStart"}`,
		`{"session_id":"compact-e2e","cwd":"/tmp/dora","hook_event_name":"UserPromptSubmit","prompt":"实现 compaction 修复"}`,
		`{"session_id":"compact-e2e","source":"compact","cwd":"/tmp/updated","model":"gpt-updated","hook_event_name":"SessionStart"}`,
	} {
		if err := emitter.Emit(ctx, strings.NewReader(input)); err != nil {
			t.Fatal(err)
		}
	}
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateRunning ||
		active[0].Session.PromptPreview != "实现 compaction 修复" || active[0].Session.CWDBasename != "updated" ||
		active[0].Session.Model != "gpt-updated" || !active[0].Session.LastSeenAt.Equal(base.Add(2*time.Second)) {
		t.Fatalf("source=compact 端到端传递失败: %+v, %v", active, err)
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
