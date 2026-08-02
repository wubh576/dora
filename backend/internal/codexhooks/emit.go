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
)

const (
	DefaultEndpoint = "http://127.0.0.1:8080/api/v1/hooks/codex"
	maxInputBytes   = 256 << 10
)

var ErrServiceUnavailable = errors.New("Dora 服务不可用")

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
	if err != nil {
		return err
	}
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

type rawHookEvent struct {
	SessionID     string          `json:"session_id"`
	TurnID        string          `json:"turn_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	Model         string          `json:"model"`
	ToolName      string          `json:"tool_name"`
	ToolUseID     string          `json:"tool_use_id"`
	ToolInput     json.RawMessage `json:"tool_input"`
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
	event := attention.Event{
		SessionID:    raw.SessionID,
		HookEvent:    raw.HookEventName,
		TurnID:       strings.TrimSpace(raw.TurnID),
		CWDBasename:  filepath.Base(strings.TrimSpace(raw.CWD)),
		Model:        strings.TrimSpace(raw.Model),
		Surface:      surface.Name,
		TerminalKind: surface.TerminalKind,
		TTY:          surface.TTY,
		ToolName:     strings.TrimSpace(raw.ToolName),
		ToolUseID:    strings.TrimSpace(raw.ToolUseID),
	}
	if event.HookEvent == "PermissionRequest" {
		if len(raw.ToolInput) == 0 {
			return attention.Event{}, errors.New("Codex 授权事件缺少工具输入")
		}
		var inputValue any
		inputDecoder := json.NewDecoder(bytes.NewReader(raw.ToolInput))
		inputDecoder.UseNumber()
		if err := inputDecoder.Decode(&inputValue); err != nil {
			return attention.Event{}, errors.New("Codex 授权事件工具输入无效")
		}
		canonicalInput, err := json.Marshal(inputValue)
		if err != nil {
			return attention.Event{}, errors.New("规范化 Codex 授权事件失败")
		}
		hash := sha256.Sum256(canonicalInput)
		event.InputHash = hex.EncodeToString(hash[:])
	}
	if _, err := event.Domain(time.Now().UTC()); err != nil {
		return attention.Event{}, errors.New("Codex Hook 事件字段无效")
	}
	return event, nil
}
