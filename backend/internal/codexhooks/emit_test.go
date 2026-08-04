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

func TestEmitterIgnoresSubagentEventsWithoutChangingRootSession(t *testing.T) {
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

	emit(`{"session_id":"root-session","turn_id":"turn-1","cwd":"/tmp/dora","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_use_id":"call-1","tool_input":{"command":"make verify"}}`)
	emit(`{"session_id":"root-session","cwd":"/tmp/dora","hook_event_name":"Stop","agent_type":"explorer"}`)
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].Session.PromptPreview != "实现根任务" || active[0].RequestCount != 1 || active[0].Latest == nil {
		t.Fatalf("subagent 事件破坏 waiting 根 session: %+v, %v", active, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("waiting 期间 subagent 事件发起了 loopback 请求: %d", requests.Load())
	}

	emit(`{"session_id":"root-session","cwd":"/tmp/dora","hook_event_name":"SessionEnd"}`)
	if _, err := store.RuntimeSessionState(ctx, sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("真正的根 SessionEnd 未删除 session: %v", err)
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
