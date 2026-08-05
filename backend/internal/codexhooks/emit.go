package codexhooks

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

const (
	DefaultEndpoint              = "http://127.0.0.1:8080/api/v1/hooks/codex"
	maxInputBytes                = 256 << 10
	codexAppOverviewPromptPrefix = "# Overview Generate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project:"
	codexAppUserRequestMarker    = "## My request for Codex:"
)

var (
	ErrServiceUnavailable = errors.New("Dora 服务不可用")
	errIgnoredHookEvent   = errors.New("忽略不产生 attention 的 Codex 事件")
)

type eventSurfaceDetector interface {
	Detect() Surface
}

type Emitter struct {
	endpoint string
	client   *http.Client
	detector eventSurfaceDetector
}

func NewEmitter() *Emitter {
	return &Emitter{
		endpoint: DefaultEndpoint,
		client: &http.Client{
			Timeout: 450 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		detector: newSurfaceDetector(),
	}
}

func (emitter *Emitter) Emit(ctx context.Context, input io.Reader) error {
	event, err := parseHookEvent(input, emitter.detector.Detect())
	if errors.Is(err, errIgnoredHookEvent) {
		return nil
	}
	if err != nil {
		return err
	}
	event = normalizeCodexAppBackgroundEvent(event)
	body, err := json.Marshal(event)
	if err != nil {
		return errors.New("编码 Codex Hook 事件失败")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, emitter.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("创建 Dora Hook 请求失败")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := emitter.client.Do(request)
	if err != nil {
		return ErrServiceUnavailable
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if response.StatusCode >= 500 {
		return ErrServiceUnavailable
	}
	return fmt.Errorf("Dora 拒绝 Codex Hook 事件（HTTP %d）", response.StatusCode)
}

func normalizeCodexAppBackgroundEvent(event attention.Event) attention.Event {
	if event.Surface == domain.CodexSurfaceApp &&
		event.HookEvent == "UserPromptSubmit" &&
		strings.HasPrefix(event.PromptPreview, codexAppOverviewPromptPrefix) {
		// Ambient Suggestions 不发送 Stop；用最小结束事件清除已注册的后台 runtime。
		event.HookEvent = "SessionEnd"
		event.PromptPreview = ""
	}
	return event
}

type rawHookEvent struct {
	SessionID     string          `json:"session_id"`
	TurnID        string          `json:"turn_id"`
	AgentID       string          `json:"agent_id"`
	AgentType     string          `json:"agent_type"`
	Source        string          `json:"source"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	Model         string          `json:"model"`
	ToolName      string          `json:"tool_name"`
	ToolUseID     string          `json:"tool_use_id"`
	ToolInput     json.RawMessage `json:"tool_input"`
	Prompt        string          `json:"prompt"`
}

func parseHookEvent(input io.Reader, surface Surface) (attention.Event, error) {
	limited := &io.LimitedReader{R: input, N: maxInputBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return attention.Event{}, errors.New("读取 Codex Hook 事件失败")
	}
	if len(data) > maxInputBytes {
		return attention.Event{}, errors.New("Codex Hook 事件超过大小限制")
	}
	var raw rawHookEvent
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return attention.Event{}, errors.New("Codex Hook 事件不是有效 JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return attention.Event{}, errors.New("Codex Hook 事件包含多余内容")
	}
	raw.SessionID = strings.TrimSpace(raw.SessionID)
	raw.HookEventName = strings.TrimSpace(raw.HookEventName)
	if raw.SessionID == "" || raw.HookEventName == "" {
		return attention.Event{}, errors.New("Codex Hook 事件缺少 session 或事件名")
	}
	agentID := strings.TrimSpace(raw.AgentID)
	agentType := strings.TrimSpace(raw.AgentType)
	isSubagent := agentID != "" || agentType != ""
	if isSubagent {
		switch raw.HookEventName {
		case "PermissionRequest", "PostToolUse", "Stop", "SubagentStop":
		default:
			// 普通 child 生命周期不能进入父 runtime 状态机。
			return attention.Event{}, errIgnoredHookEvent
		}
	}
	event := attention.Event{
		SessionID:          raw.SessionID,
		HookEvent:          raw.HookEventName,
		SessionStartSource: strings.TrimSpace(raw.Source),
		TurnID:             strings.TrimSpace(raw.TurnID),
		SubagentEvent:      isSubagent,
		SubagentScope:      subagentScope(agentID),
		CWDBasename:        filepath.Base(strings.TrimSpace(raw.CWD)),
		Model:              strings.TrimSpace(raw.Model),
		Surface:            surface.Name,
		TerminalKind:       surface.TerminalKind,
		TTY:                surface.TTY,
		ToolName:           strings.TrimSpace(raw.ToolName),
		ToolUseKey:         opaqueKey("tool-use", raw.ToolUseID),
	}
	if event.HookEvent == "UserPromptSubmit" {
		event.PromptPreview = userPrompt(raw.Prompt, surface)
	}
	if event.HookEvent == "PermissionRequest" || event.HookEvent == "PostToolUse" {
		if len(raw.ToolInput) == 0 {
			if event.HookEvent == "PermissionRequest" {
				return attention.Event{}, errors.New("Codex 授权事件缺少工具输入")
			}
		} else {
			inputValue, canonicalInput, err := canonicalToolInput(raw.ToolInput)
			if err != nil {
				return attention.Event{}, err
			}
			event.ToolInputKey = toolInputCorrelationKey(event.ToolName, inputValue, canonicalInput)
			if event.HookEvent == "PermissionRequest" {
				// 既有 event key 继续使用无命名空间 hash，避免升级后重复提醒。
				event.InputHash = completeToolInputHash(canonicalInput)
			}
		}
	}
	domainEvent, err := event.Domain(time.Now().UTC())
	if err != nil {
		return attention.Event{}, errors.New("Codex Hook 事件字段无效")
	}
	if event.SubagentScope == "" && (event.HookEvent == "PermissionRequest" ||
		(event.HookEvent == "PreToolUse" && event.ToolName == "request_user_input")) {
		stablePart := strings.TrimSpace(raw.ToolUseID)
		if stablePart == "" {
			stablePart = event.InputHash
		}
		// root key 继续使用旧版 raw tool ID 参与最终 hash，但 raw ID 不穿过 loopback。
		event.EventKey = attention.RootEventKey(
			domainEvent.ExternalSessionID, domainEvent.TurnID,
			domainEvent.EventName, domainEvent.ToolName, stablePart,
		)
	}
	// 在 helper 出站前完成脱敏和截断，原始 prompt 不穿过 loopback API。
	event.SessionID = domainEvent.ExternalSessionID
	event.SessionStartSource = domainEvent.SessionStartSource
	event.TurnID = domainEvent.TurnID
	event.SubagentEvent = domainEvent.SubagentEvent
	event.SubagentScope = domainEvent.SubagentScope
	event.CWDBasename = domainEvent.CWDBasename
	event.Model = domainEvent.Model
	event.TTY = domainEvent.TTY
	event.ToolName = domainEvent.ToolName
	event.ToolUseKey = domainEvent.ToolUseKey
	event.ToolInputKey = domainEvent.ToolInputKey
	event.PromptPreview = domainEvent.PromptPreview
	return event, nil
}

func subagentScope(agentID string) string {
	return opaqueKey("agent-id", agentID)
}

func opaqueKey(kind, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return opaqueBytesKey(kind, []byte(value))
}

func opaqueBytesKey(kind string, value []byte) string {
	hash := sha256.Sum256(append([]byte(kind+"\x00"), value...))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func canonicalToolInput(raw json.RawMessage) (any, []byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, errors.New("Codex 工具输入无效")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, nil, errors.New("规范化 Codex 工具输入失败")
	}
	return value, canonical, nil
}

func completeToolInputHash(canonical []byte) string {
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:])
}

func toolInputCorrelationKey(toolName string, value any, canonical []byte) string {
	correlationInput := canonical
	if toolName == "Bash" {
		if fields, ok := value.(map[string]any); ok {
			if command, ok := fields["command"].(string); ok {
				// Bash description 只解释授权原因，真正执行身份由 command 决定。
				correlationInput, _ = json.Marshal(map[string]string{"command": command})
			}
		}
	}
	return opaqueBytesKey("tool-input", correlationInput)
}

func userPrompt(value string, surface Surface) string {
	if surface.Name != domain.CodexSurfaceApp {
		return value
	}
	offset := 0
	requestStart := -1
	for _, line := range strings.SplitAfter(value, "\n") {
		offset += len(line)
		if requestStart < 0 && strings.TrimSpace(line) == codexAppUserRequestMarker {
			requestStart = offset
		}
	}
	if requestStart >= 0 {
		return value[requestStart:]
	}
	return value
}
