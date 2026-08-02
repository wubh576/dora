package codexhooks

import (
	"errors"
	"testing"

	"github.com/wubh576/dora/backend/internal/domain"
)

type fakeInspector map[int]processInfo

func (inspector fakeInspector) Inspect(pid int) (processInfo, error) {
	info, ok := inspector[pid]
	if !ok {
		return processInfo{}, errors.New("missing")
	}
	return info, nil
}

func TestSurfaceDetector(t *testing.T) {
	tests := []struct {
		name      string
		processes fakeInspector
		term      string
		want      Surface
	}{
		{
			name: "Codex App",
			processes: fakeInspector{
				10: {PID: 10, Parent: 9, TTY: "??", Command: "/Applications/ChatGPT.app/Contents/Resources/codex app-server"},
				9:  {PID: 9, Parent: 1, TTY: "??", Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"},
			},
			want: Surface{Name: domain.CodexSurfaceApp},
		},
		{
			name: "iTerm2 CLI",
			processes: fakeInspector{
				10: {PID: 10, Parent: 9, TTY: "ttys004", Command: "codex"},
				9:  {PID: 9, Parent: 1, TTY: "ttys004", Command: "-zsh"},
			},
			term: "iTerm.app",
			want: Surface{Name: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalITerm2, TTY: "/dev/ttys004"},
		},
		{
			name: "CLI 参数包含 App 路径",
			processes: fakeInspector{
				10: {PID: 10, Parent: 9, TTY: "ttys006", Command: `codex exec "inspect /Applications/Codex.app/Contents/file"`},
				9:  {PID: 9, Parent: 1, TTY: "ttys006", Command: "-zsh"},
			},
			term: "Apple_Terminal",
			want: Surface{Name: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal, TTY: "/dev/ttys006"},
		},
		{
			name: "unsupported terminal",
			processes: fakeInspector{
				10: {PID: 10, Parent: 1, TTY: "ttys005", Command: `codex exec "inspect /Applications/Terminal.app/Contents/file"`},
			},
			term: "vscode",
			want: Surface{Name: domain.CodexSurfaceUnknown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detector := &surfaceDetector{inspector: test.processes, parentPID: 10, getenv: func(string) string { return test.term }}
			if got := detector.Detect(); got != test.want {
				t.Fatalf("Detect() = %+v，期望 %+v", got, test.want)
			}
		})
	}
}
