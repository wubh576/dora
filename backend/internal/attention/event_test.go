package attention

import (
	"testing"
	"time"

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

	for _, invalid := range []Event{
		{SessionID: "session", HookEvent: "Stop", Surface: domain.CodexSurfaceCLI, TerminalKind: "custom-terminal"},
		{SessionID: "session", HookEvent: "PreToolUse", Surface: domain.CodexSurfaceCLI, ToolName: "Bash", ToolUseID: "call"},
	} {
		if _, err := invalid.Domain(time.Now()); err == nil {
			t.Fatalf("Domain() 接受无效 runtime 元数据: %+v", invalid)
		}
	}
}
