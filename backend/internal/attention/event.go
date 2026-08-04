package attention

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/wubh576/dora/backend/internal/domain"
)

type Event struct {
	SessionID          string `json:"sessionId"`
	HookEvent          string `json:"hookEvent"`
	SessionStartSource string `json:"source,omitempty"`
	TurnID             string `json:"turnId,omitempty"`
	CWDBasename        string `json:"cwdBasename,omitempty"`
	Model              string `json:"model,omitempty"`
	Surface            string `json:"surface"`
	TerminalKind       string `json:"terminalKind,omitempty"`
	TTY                string `json:"tty,omitempty"`
	ToolName           string `json:"toolName,omitempty"`
	ToolUseID          string `json:"toolUseId,omitempty"`
	InputHash          string `json:"inputHash,omitempty"`
	PromptPreview      string `json:"promptPreview,omitempty"`
}

func (event Event) Domain(receivedAt time.Time) (domain.CodexHookEvent, error) {
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.TurnID = strings.TrimSpace(event.TurnID)
	event.ToolName = cleanLabel(event.ToolName, 80)
	event.SessionStartSource = cleanLabel(event.SessionStartSource, 40)
	if event.HookEvent != "SessionStart" {
		event.SessionStartSource = ""
	}
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
	promptPreview := ""
	if event.HookEvent == "UserPromptSubmit" {
		promptPreview = cleanPromptPreview(event.PromptPreview, 160)
	}
	return domain.CodexHookEvent{
		ExternalSessionID:  event.SessionID,
		EventName:          event.HookEvent,
		SessionStartSource: event.SessionStartSource,
		TurnID:             event.TurnID,
		CWDBasename:        cwdBasename(event.CWDBasename),
		Model:              cleanLabel(event.Model, 160),
		Surface:            event.Surface,
		TerminalKind:       event.TerminalKind,
		TTY:                cleanLabel(event.TTY, 80),
		ToolName:           event.ToolName,
		EventKey:           eventKey,
		PromptPreview:      promptPreview,
		ReceivedAt:         receivedAt.UTC(),
	}, nil
}

func cleanPromptPreview(value string, limit int) string {
	var builder strings.Builder
	separated := true
	count := 0
	for _, current := range value {
		if unicode.IsSpace(current) || unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			if !separated && count < limit {
				builder.WriteByte(' ')
				count++
			}
			separated = true
			continue
		}
		if count >= limit {
			break
		}
		builder.WriteRune(current)
		count++
		separated = false
	}
	return strings.TrimSpace(builder.String())
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
