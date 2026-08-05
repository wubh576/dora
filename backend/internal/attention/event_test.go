package attention

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestEventDomainKeepsOnlySafeRuntimeLabels(t *testing.T) {
	event, err := (Event{
		SessionID:    "session",
		HookEvent:    "Stop",
		CWDBasename:  "/Users/private/work/project",
		Surface:      domain.CodexSurfaceCLI,
		TerminalKind: domain.TerminalITerm2,
		ToolName:     "  Bash\nforged-log  ",
	}).Domain(time.Now())
	if err != nil {
		t.Fatalf("Domain() 失败: %v", err)
	}
	if event.CWDBasename != "project" {
		t.Fatalf("cwd 标签 = %q，期望只保留 basename", event.CWDBasename)
	}
	if event.ToolName != "Bash forged-log" {
		t.Fatalf("tool 标签未压缩为单行: %q", event.ToolName)
	}
	if event.PromptPreview != "" {
		t.Fatalf("非 UserPromptSubmit 保留了 preview: %q", event.PromptPreview)
	}

	for _, invalid := range []Event{
		{SessionID: "session", HookEvent: "Stop", Surface: domain.CodexSurfaceCLI, TerminalKind: "custom-terminal"},
		{SessionID: "session", HookEvent: "PreToolUse", Surface: domain.CodexSurfaceCLI, ToolName: "Bash"},
	} {
		if _, err := invalid.Domain(time.Now()); err == nil {
			t.Fatalf("Domain() 接受无效 runtime 元数据: %+v", invalid)
		}
	}
}

func TestEventDomainSanitizesPromptPreview(t *testing.T) {
	input := "  第一行\n\t第二行\u200b\u001b[31m  第三行  "
	event, err := (Event{
		SessionID: "session", HookEvent: "UserPromptSubmit",
		Surface: domain.CodexSurfaceApp, PromptPreview: input,
	}).Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.PromptPreview != "第一行 第二行 [31m 第三行" {
		t.Fatalf("prompt preview = %q", event.PromptPreview)
	}

	long, err := (Event{
		SessionID: "session", HookEvent: "UserPromptSubmit",
		Surface: domain.CodexSurfaceApp, PromptPreview: strings.Repeat("界", 200),
	}).Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if count := utf8.RuneCountInString(long.PromptPreview); count != 160 {
		t.Fatalf("prompt preview 长度 = %d", count)
	}
}

func TestEventDomainKeepsOnlySessionStartSource(t *testing.T) {
	start, err := (Event{
		SessionID: "session", HookEvent: "SessionStart", SessionStartSource: " compact\n",
		Surface: domain.CodexSurfaceApp,
	}).Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if start.SessionStartSource != "compact" {
		t.Fatalf("SessionStart source = %q", start.SessionStartSource)
	}
	stop, err := (Event{
		SessionID: "session", HookEvent: "Stop", SessionStartSource: "compact",
		Surface: domain.CodexSurfaceApp,
	}).Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stop.SessionStartSource != "" {
		t.Fatalf("非 SessionStart 保留 source: %q", stop.SessionStartSource)
	}
}

func TestSubagentEventKeyIncludesValidatedScope(t *testing.T) {
	scopeA := "sha256:" + strings.Repeat("a", 64)
	scopeB := "sha256:" + strings.Repeat("b", 64)
	toolKey := "sha256:" + strings.Repeat("c", 64)
	inputKey := "sha256:" + strings.Repeat("d", 64)
	base := Event{
		SessionID: "parent", HookEvent: "PermissionRequest", TurnID: "turn",
		Surface: domain.CodexSurfaceApp, ToolName: "Bash", ToolUseKey: toolKey,
		ToolInputKey: inputKey, SubagentEvent: true,
	}
	root := base
	root.EventKey = RootEventKey("parent", "turn", "PermissionRequest", "Bash", "raw-tool-id")
	rootEvent, err := root.Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantRootKey := RootEventKey("parent", "turn", "PermissionRequest", "Bash", "raw-tool-id")
	if rootEvent.EventKey != wantRootKey {
		t.Fatalf("root event key 兼容性被破坏: got=%s want=%s", rootEvent.EventKey, wantRootKey)
	}
	base.SubagentScope = scopeA
	first, err := base.Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base.SubagentScope = scopeB
	second, err := base.Domain(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.EventKey == second.EventKey || first.SubagentScope != scopeA || second.SubagentScope != scopeB ||
		!first.SubagentEvent || first.ToolInputKey != inputKey {
		t.Fatalf("child scope 未参与稳定 key: first=%+v second=%+v", first, second)
	}
	for _, invalid := range []Event{
		{SessionID: "parent", HookEvent: "Stop", Surface: domain.CodexSurfaceApp, SubagentScope: "raw-agent-id"},
		{SessionID: "parent", HookEvent: "PostToolUse", Surface: domain.CodexSurfaceApp, ToolUseKey: "raw-tool-id"},
		{SessionID: "parent", HookEvent: "PostToolUse", Surface: domain.CodexSurfaceApp, ToolInputKey: "raw-tool-input"},
		{SessionID: "parent", HookEvent: "PermissionRequest", Surface: domain.CodexSurfaceApp, ToolUseKey: toolKey, EventKey: "raw-event-key"},
		{SessionID: "parent", HookEvent: "Stop", Surface: domain.CodexSurfaceApp, EventKey: wantRootKey},
	} {
		if _, err := invalid.Domain(time.Now()); err == nil {
			t.Fatalf("Domain() 接受未脱敏 child key: %+v", invalid)
		}
	}
}
