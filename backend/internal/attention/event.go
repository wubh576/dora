package attention

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

type Event struct {
	SessionID    string `json:"sessionId"`
	HookEvent    string `json:"hookEvent"`
	TurnID       string `json:"turnId,omitempty"`
	CWDBasename  string `json:"cwdBasename,omitempty"`
	Model        string `json:"model,omitempty"`
	Surface      string `json:"surface"`
	TerminalKind string `json:"terminalKind,omitempty"`
	TTY          string `json:"tty,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
	ToolUseID    string `json:"toolUseId,omitempty"`
	InputHash    string `json:"inputHash,omitempty"`
}

func (event Event) Domain(receivedAt time.Time) (domain.CodexHookEvent, error) {
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.TurnID = strings.TrimSpace(event.TurnID)
	event.ToolName = cleanLabel(event.ToolName, 80)
	if event.SessionID == "" || event.HookEvent == "" {
		return domain.CodexHookEvent{}, errors.New("事件缺少 sessionId 或 hookEvent")
	}
	if event.Surface == "" {
		event.Surface = domain.CodexSurfaceUnknown
	}
	if event.TerminalKind != domain.TerminalITerm2 && event.TerminalKind != domain.TerminalTerminal && event.TerminalKind != domain.TerminalUnknown {
		return domain.CodexHookEvent{}, errors.New("事件包含未知 terminalKind")
	}
	if event.HookEvent == "PreToolUse" && event.ToolName != "request_user_input" {
		return domain.CodexHookEvent{}, errors.New("仅接收 request_user_input 的 PreToolUse")
	}
	eventKey := ""
	if event.HookEvent == "PermissionRequest" || (event.HookEvent == "PreToolUse" && event.ToolName == "request_user_input") {
		stablePart := event.ToolUseID
		if stablePart == "" {
			stablePart = event.InputHash
		}
		if stablePart == "" {
			return domain.CodexHookEvent{}, errors.New("等待事件缺少稳定标识")
		}
		eventKey = stableEventKey(event.SessionID, event.TurnID, event.HookEvent, event.ToolName, stablePart)
	}
	return domain.CodexHookEvent{
		ExternalSessionID: event.SessionID,
		EventName:         event.HookEvent,
		TurnID:            event.TurnID,
		CWDBasename:       cwdBasename(event.CWDBasename),
		Model:             cleanLabel(event.Model, 160),
		Surface:           event.Surface,
		TerminalKind:      event.TerminalKind,
		TTY:               cleanLabel(event.TTY, 80),
		ToolName:          event.ToolName,
		EventKey:          eventKey,
		ReceivedAt:        receivedAt.UTC(),
	}, nil
}

func cwdBasename(value string) string {
	base := filepath.Base(strings.TrimSpace(value))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return cleanLabel(base, 120)
}

func stableEventKey(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "codex:" + hex.EncodeToString(hash[:])
}

func cleanLabel(value string, limit int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
