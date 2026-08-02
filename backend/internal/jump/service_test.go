package jump

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wubh576/dora/backend/internal/domain"
)

type commandCall struct {
	name string
	args []string
}

type recordingRunner struct {
	calls  []commandCall
	output []byte
	err    error
}

func (runner *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{name: name, args: append([]string(nil), args...)})
	return runner.output, runner.err
}

func TestJumpCodexAppUsesParameterizedDeepLinkAndForeground(t *testing.T) {
	runner := &recordingRunner{}
	service := New(runner)
	session := domain.RuntimeSession{Surface: domain.CodexSurfaceApp, ExternalSessionID: "thread/with space"}
	if err := service.Jump(context.Background(), session); err != nil {
		t.Fatalf("Jump() 失败: %v", err)
	}
	want := []commandCall{
		{name: "/usr/bin/open", args: []string{"codex://threads/thread%2Fwith%20space"}},
		{name: "/usr/bin/osascript", args: []string{"-e", activateCodexAppScript}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("App 跳转命令 = %+v，期望 %+v", runner.calls, want)
	}
}

func TestJumpTerminalsPassExactTTYAsArgument(t *testing.T) {
	for _, test := range []struct {
		name string
		kind string
	}{
		{name: "iTerm2", kind: domain.TerminalITerm2},
		{name: "Terminal", kind: domain.TerminalTerminal},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{}
			service := New(runner)
			tty := `/dev/ttys007" & do shell script "bad"`
			err := service.Jump(context.Background(), domain.RuntimeSession{
				Surface: domain.CodexSurfaceCLI, TerminalKind: test.kind, TTY: tty,
			})
			if err != nil {
				t.Fatalf("Jump() 失败: %v", err)
			}
			if len(runner.calls) != 1 || runner.calls[0].name != "/usr/bin/osascript" {
				t.Fatalf("终端命令错误: %+v", runner.calls)
			}
			args := runner.calls[0].args
			if len(args) != 4 || args[2] != "--" || args[3] != tty || strings.Contains(args[1], tty) {
				t.Fatalf("TTY 未作为独立 argv 传递: %+v", args)
			}
		})
	}
}

func TestJumpTerminalReportsGoneTarget(t *testing.T) {
	runner := &recordingRunner{output: []byte(targetGoneMarker + "\n")}
	service := New(runner)
	err := service.Jump(context.Background(), domain.RuntimeSession{
		Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal, TTY: "/dev/ttys009",
	})
	if !errors.Is(err, ErrTargetGone) || !strings.Contains(err.Error(), "/dev/ttys009") {
		t.Fatalf("消失目标错误不明确: %v", err)
	}
}

func TestJumpTerminalExecutionFailureKeepsDistinctError(t *testing.T) {
	runner := &recordingRunner{err: errors.New("automation permission denied")}
	service := New(runner)
	err := service.Jump(context.Background(), domain.RuntimeSession{
		Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal, TTY: "/dev/ttys009",
	})
	if err == nil || errors.Is(err, ErrTargetGone) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("执行错误被误判为目标消失: %v", err)
	}
}

type realExitErrorRunner struct{}

func (realExitErrorRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'Not authorized to send Apple events\nsecond line' >&2; exit 1`).CombinedOutput()
}

func TestJumpIncludesBoundedSingleLineCommandDiagnostic(t *testing.T) {
	service := New(realExitErrorRunner{})
	err := service.Jump(context.Background(), domain.RuntimeSession{
		Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal, TTY: "/dev/ttys009",
	})
	if err == nil || !strings.Contains(err.Error(), "Not authorized to send Apple events second line") {
		t.Fatalf("真实 ExitError 未包含可读 stderr: %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("命令诊断未压缩为单行: %q", err)
	}
}

func TestCommandDiagnosticTruncatesUnicodeSafely(t *testing.T) {
	err := commandError(context.Background(), "执行跳转", []byte(strings.Repeat("错误", 200)), errors.New("exit status 1"))
	if !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Fatalf("长中文诊断截断结果无效: %q", err)
	}
}

func TestJumpRejectsFuzzyFallbackTargets(t *testing.T) {
	service := New(&recordingRunner{})
	for _, session := range []domain.RuntimeSession{
		{Surface: domain.CodexSurfaceUnknown, CWDBasename: "dora"},
		{Surface: domain.CodexSurfaceCLI, TerminalKind: "vscode", TTY: "/dev/ttys001"},
		{Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal},
	} {
		if err := service.Jump(context.Background(), session); err == nil {
			t.Fatalf("Jump() 接受了不可精确定位的目标: %+v", session)
		}
	}
}

func TestCapabilityExplainsOnlySanitizedLocatorRequirements(t *testing.T) {
	tests := []struct {
		name    string
		session domain.RuntimeSession
		ok      bool
		reason  string
	}{
		{name: "app", session: domain.RuntimeSession{Surface: domain.CodexSurfaceApp, ExternalSessionID: "private-thread"}, ok: true},
		{name: "app missing thread", session: domain.RuntimeSession{Surface: domain.CodexSurfaceApp}, reason: "Codex App 会话缺少 thread ID"},
		{name: "cli", session: domain.RuntimeSession{Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalITerm2, TTY: "/dev/ttys009"}, ok: true},
		{name: "cli missing tty", session: domain.RuntimeSession{Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal}, reason: "Codex CLI 会话缺少精确 TTY"},
		{name: "unknown", session: domain.RuntimeSession{Surface: domain.CodexSurfaceUnknown}, reason: "无法识别 Codex 会话来源"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ok, reason := Capability(test.session)
			if ok != test.ok || reason != test.reason {
				t.Fatalf("Capability() = %t, %q", ok, reason)
			}
			if test.session.ExternalSessionID != "" && strings.Contains(reason, test.session.ExternalSessionID) {
				t.Fatalf("能力说明泄露 thread ID: %q", reason)
			}
			if test.session.TTY != "" && strings.Contains(reason, test.session.TTY) {
				t.Fatalf("能力说明泄露 TTY: %q", reason)
			}
		})
	}
}
