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
		{SessionID: "session", HookEvent: "PreToolUse", Surface: domain.CodexSurfaceCLI, ToolName: "Bash", ToolUseID: "call"},
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
