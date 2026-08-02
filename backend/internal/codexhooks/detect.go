package codexhooks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/wubh576/dora/backend/internal/domain"
)

type Surface struct {
	Name         string
	TerminalKind string
	TTY          string
}

type processInfo struct {
	PID     int
	Parent  int
	TTY     string
	Command string
}

type processInspector interface {
	Inspect(pid int) (processInfo, error)
}

type surfaceDetector struct {
	inspector processInspector
	getenv    func(string) string
	parentPID int
}

func newSurfaceDetector() *surfaceDetector {
	return &surfaceDetector{inspector: systemProcessInspector{}, getenv: os.Getenv, parentPID: os.Getppid()}
}

func (detector *surfaceDetector) Detect() Surface {
	processes := make([]processInfo, 0, 8)
	pid := detector.parentPID
	for depth := 0; depth < 8; depth++ {
		if pid <= 1 {
			break
		}
		info, err := detector.inspector.Inspect(pid)
		if err != nil {
			break
		}
		processes = append(processes, info)
		pid = info.Parent
	}
	tty := firstTTY(processes)
	terminalKind := terminalKind(detector.getenv("TERM_PROGRAM"), processes)
	if tty != "" && terminalKind != "" {
		return Surface{Name: domain.CodexSurfaceCLI, TerminalKind: terminalKind, TTY: tty}
	}
	for _, process := range processes {
		if isCodexAppProcess(process.Command) {
			return Surface{Name: domain.CodexSurfaceApp}
		}
	}
	return Surface{Name: domain.CodexSurfaceUnknown}
}

func isCodexAppProcess(command string) bool {
	executable := processExecutable(command)
	return strings.Contains(executable, "/ChatGPT.app/Contents/") || strings.Contains(executable, "/Codex.app/Contents/")
}

func firstTTY(processes []processInfo) string {
	for _, process := range processes {
		tty := strings.TrimSpace(process.TTY)
		if tty == "" || tty == "??" || tty == "-" {
			continue
		}
		if !strings.HasPrefix(tty, "/dev/") {
			tty = "/dev/" + tty
		}
		return tty
	}
	return ""
}

func terminalKind(termProgram string, processes []processInfo) string {
	switch termProgram {
	case "iTerm.app":
		return domain.TerminalITerm2
	case "Apple_Terminal":
		return domain.TerminalTerminal
	}
	for _, process := range processes {
		executable := processExecutable(process.Command)
		switch {
		case strings.Contains(executable, "/iTerm.app/Contents/") || strings.Contains(executable, "/iTerm2.app/Contents/"):
			return domain.TerminalITerm2
		case strings.Contains(executable, "/Terminal.app/Contents/"):
			return domain.TerminalTerminal
		}
	}
	return ""
}

func processExecutable(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

type systemProcessInspector struct{}

func (systemProcessInspector) Inspect(pid int) (processInfo, error) {
	output, err := exec.Command("/bin/ps", "-o", "ppid=,tty=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return processInfo{}, fmt.Errorf("读取进程 %d: %w", pid, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 3 {
		return processInfo{}, errors.New("进程信息不完整")
	}
	parent, err := strconv.Atoi(fields[0])
	if err != nil {
		return processInfo{}, fmt.Errorf("解析父进程: %w", err)
	}
	return processInfo{PID: pid, Parent: parent, TTY: fields[1], Command: strings.Join(fields[2:], " ")}, nil
}
