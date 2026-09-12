package workbuddyhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
)

const DefaultEndpoint = "http://127.0.0.1:8080/api/v1/hooks/workbuddy"
const maxInputBytes = 256 << 10

var ErrServiceUnavailable = errors.New("Dora 服务不可用")
var errIgnoredEvent = errors.New("忽略无关的 WorkBuddy 事件")

type Emitter struct {
	endpoint string
	client   *http.Client
}

func NewEmitter() *Emitter {
	return &Emitter{endpoint: DefaultEndpoint, client: &http.Client{Timeout: 450 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (e *Emitter) Emit(ctx context.Context, input io.Reader) error {
	event, err := parseHookEvent(input)
	if errors.Is(err, errIgnoredEvent) {
		return nil
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return errors.New("编码 WorkBuddy 事件失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(req)
	if err != nil {
		return ErrServiceUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if response.StatusCode >= 500 {
		return ErrServiceUnavailable
	}
	return fmt.Errorf("Dora 拒绝 WorkBuddy 事件（HTTP %d）", response.StatusCode)
}

type rawHook struct {
	SessionID        string `json:"session_id"`
	EventName        string `json:"hook_event_name"`
	Source           string `json:"source"`
	CWD              string `json:"cwd"`
	AgentID          string `json:"agent_id"`
	ToolName         string `json:"tool_name"`
	CallID           string `json:"call_id"`
	ToolUseID        string `json:"tool_use_id"`
	NotificationType string `json:"notification_type"`
	Prompt           string `json:"prompt"`
	Message          string `json:"message"`
}

func parseHookEvent(input io.Reader) (attention.Event, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil || len(data) > maxInputBytes {
		return attention.Event{}, errors.New("读取 WorkBuddy Hook 失败或事件超过大小限制")
	}
	var raw rawHook
	if err := json.Unmarshal(data, &raw); err != nil {
		return attention.Event{}, errors.New("WorkBuddy Hook 不是有效 JSON")
	}
	event := attention.Event{SessionID: strings.TrimSpace(raw.SessionID), HookEvent: raw.EventName, SessionStartSource: raw.Source, CWDBasename: filepath.Base(raw.CWD), Surface: domain.WorkBuddySurfaceApp, ToolName: raw.ToolName}
	// 根 agent 也可能带 agent_type；只有独立 store ID 才表示子代理。
	if raw.AgentID != "" && raw.AgentID != raw.SessionID {
		event.SubagentEvent = true
		event.SubagentScope = opaqueKey("agent", raw.AgentID)
	}
	switch event.HookEvent {
	case "SessionStart", "SessionEnd", "UserPromptSubmit", "PermissionRequest", "PostToolUse", "Stop", "SubagentStop":
	case "PostToolUseFailure":
		event.HookEvent = "PostToolUse"
	case "FinalStop", "StopFailure":
		event.HookEvent = "Stop"
	case "Notification":
		// 5.5.6 的桌面提问先触发此通知；只接受引擎的固定模板，不保留消息正文。
		const prefix = "needs your permission to use "
		if raw.NotificationType != "permission_prompt" || !strings.HasPrefix(raw.Message, prefix) {
			return attention.Event{}, errIgnoredEvent
		}
		event.ToolName = strings.TrimPrefix(raw.Message, prefix)
		if !validToolName(event.ToolName) {
			return attention.Event{}, errIgnoredEvent
		}
		event.HookEvent = "PermissionNotification"
	case "PreToolUse":
		if raw.ToolName != "AskUserQuestion" {
			return attention.Event{}, errIgnoredEvent
		}
	default:
		return attention.Event{}, errIgnoredEvent
	}
	if event.SubagentEvent {
		switch event.HookEvent {
		case "PermissionRequest", "PermissionNotification", "PreToolUse", "PostToolUse", "Stop", "SubagentStop":
		default:
			return attention.Event{}, errIgnoredEvent
		}
	}
	// 提问工具在两个客户端间归一为同一种等待语义，完成事件使用相同名字关联。
	if event.ToolName == "AskUserQuestion" {
		event.ToolName = "request_user_input"
	}
	if event.HookEvent == "PermissionRequest" && event.ToolName == "request_user_input" {
		event.HookEvent = "PreToolUse"
	}
	if event.HookEvent == "UserPromptSubmit" {
		event.PromptPreview = raw.Prompt
	}
	callID := raw.ToolUseID
	if callID == "" {
		callID = raw.CallID
	}
	event.ToolUseKey = opaqueKey("tool", callID)
	normalized, err := Domain(event, time.Now())
	if err != nil {
		return attention.Event{}, errors.New("WorkBuddy Hook 缺少有效会话或工具调用标识")
	}
	event.PromptPreview = normalized.PromptPreview
	event.SessionID = normalized.ExternalSessionID
	event.CWDBasename = normalized.CWDBasename
	event.ToolName = normalized.ToolName
	event.SessionStartSource = normalized.SessionStartSource
	return event, nil
}
func validToolName(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func opaqueKey(kind, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("workbuddy\x00" + kind + "\x00" + value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Domain 只接受脱敏后的桌面事件，原始消息、工具参数和完整路径不进入数据库。
func Domain(event attention.Event, at time.Time) (domain.HookEvent, error) {
	if event.Surface != domain.WorkBuddySurfaceApp || event.TTY != "" || event.TerminalKind != "" || event.EventKey != "" || event.InputHash != "" || event.ToolInputKey != "" || event.Model != "" || event.PromptPreview != "" && event.HookEvent != "UserPromptSubmit" {
		return domain.HookEvent{}, errors.New("WorkBuddy 事件包含不支持的字段")
	}
	notification := event.HookEvent == "PermissionNotification"
	if notification {
		if !validToolName(event.ToolName) || event.ToolUseKey != "" {
			return domain.HookEvent{}, errors.New("WorkBuddy 等待通知格式无效")
		}
		event.HookEvent = "PermissionRequest"
		if event.ToolName == "request_user_input" {
			event.HookEvent = "PreToolUse"
		}
		// 仅用于复用字段校验；事务内按当前等待去重，解决后下一次通知重新提醒。
		event.InputHash = opaqueKey("notification", event.ToolName)
	}
	switch event.HookEvent {
	case "SessionStart", "SessionEnd", "UserPromptSubmit", "PermissionRequest", "PreToolUse", "PostToolUse", "Stop", "SubagentStop":
	default:
		return domain.HookEvent{}, errors.New("WorkBuddy 事件名无效")
	}
	result, err := event.Domain(at)
	if err != nil {
		return result, err
	}
	result.Provider = domain.WorkBuddySource
	result.WaitingNotification = notification
	if notification {
		result.EventKey = ""
	}
	if result.EventKey != "" {
		result.EventKey = "workbuddy:" + strings.TrimPrefix(result.EventKey, "codex:")
	}
	return result, nil
}
