package jump

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/wubh576/dora/backend/internal/domain"
)

var ErrTargetGone = errors.New("Codex 目标已结束")

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type Service struct {
	runner Runner
}

func New(runner Runner) *Service {
	return &Service{runner: runner}
}

func (service *Service) Jump(ctx context.Context, session domain.RuntimeSession) error {
	if service == nil || service.runner == nil {
		return errors.New("Codex 跳转服务未配置")
	}
	switch session.Surface {
	case domain.CodexSurfaceApp:
		return service.jumpApp(ctx, session.ExternalSessionID)
	case domain.CodexSurfaceCLI:
		return service.jumpTerminal(ctx, session.TerminalKind, session.TTY)
	default:
		return errors.New("当前 Codex 会话来源不支持精确跳转")
	}
}

func (service *Service) jumpApp(ctx context.Context, externalSessionID string) error {
	externalSessionID = strings.TrimSpace(externalSessionID)
	if externalSessionID == "" {
		return errors.New("Codex App 会话缺少 thread ID")
	}
	deepLink := (&url.URL{
		Scheme: "codex", Host: "threads", Path: "/" + externalSessionID, RawPath: "/" + url.PathEscape(externalSessionID),
	}).String()
	if output, err := service.runner.Run(ctx, "/usr/bin/open", deepLink); err != nil {
		return commandError(ctx, "打开 Codex thread", output, err)
	}
	if output, err := service.runner.Run(ctx, "/usr/bin/osascript", "-e", activateCodexAppScript); err != nil {
		return commandError(ctx, "将 Codex App 切到前台", output, err)
	}
	return nil
}

func (service *Service) jumpTerminal(ctx context.Context, terminalKind, tty string) error {
	tty = strings.TrimSpace(tty)
	if tty == "" {
		return errors.New("Codex CLI 会话缺少 TTY")
	}
	var script string
	switch terminalKind {
	case domain.TerminalITerm2:
		script = jumpITerm2Script
	case domain.TerminalTerminal:
		script = jumpTerminalScript
	default:
		return errors.New("当前终端不支持精确跳转")
	}
	// TTY 通过 osascript argv 传入，绝不拼进 AppleScript 源码。
	output, err := service.runner.Run(ctx, "/usr/bin/osascript", "-e", script, "--", tty)
	if err != nil {
		return commandError(ctx, "执行终端精确跳转", output, err)
	}
	if strings.TrimSpace(string(output)) == targetGoneMarker {
		return fmt.Errorf("%w: 无法定位 TTY %s", ErrTargetGone, tty)
	}
	return nil
}

func commandError(ctx context.Context, action string, output []byte, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s超时或已取消: %w", action, ctxErr)
	}
	detail := strings.Join(strings.Fields(string(output)), " ")
	const maxDetailRunes = 300
	detailRunes := []rune(detail)
	if len(detailRunes) > maxDetailRunes {
		detail = string(detailRunes[:maxDetailRunes]) + "…"
	}
	if detail != "" {
		return fmt.Errorf("%s: %s: %w", action, detail, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

const activateCodexAppScript = `tell application id "com.openai.codex" to activate`

const targetGoneMarker = "DORA_TARGET_GONE"

const jumpITerm2Script = `on run argv
  set targetTTY to item 1 of argv
  tell application "iTerm2"
    repeat with targetWindow in windows
      repeat with targetTab in tabs of targetWindow
        repeat with targetSession in sessions of targetTab
          if tty of targetSession is targetTTY then
            tell targetSession to select
            tell targetTab to select
            set miniaturized of targetWindow to false
            tell targetWindow to select
            activate
            return
          end if
        end repeat
      end repeat
    end repeat
  end tell
  return "DORA_TARGET_GONE"
end run`

const jumpTerminalScript = `on run argv
  set targetTTY to item 1 of argv
  tell application "Terminal"
    repeat with targetWindow in windows
      repeat with targetTab in tabs of targetWindow
        if tty of targetTab is targetTTY then
          set selected tab of targetWindow to targetTab
          set miniaturized of targetWindow to false
          set index of targetWindow to 1
          activate
          return
        end if
      end repeat
    end repeat
  end tell
  return "DORA_TARGET_GONE"
end run`
