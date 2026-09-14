package codexhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/approval"
	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
)

// EmitInteractive 的 stdout 专用于 Codex 决策；不可用时不作决定，保留原生审批。
func (e *Emitter) EmitInteractive(ctx context.Context, input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil || len(data) > maxInputBytes {
		return errors.New("读取 Codex Hook 失败或事件超过大小限制")
	}
	event, err := parseHookEvent(bytes.NewReader(data), e.detector.Detect())
	if errors.Is(err, errIgnoredHookEvent) {
		return nil
	}
	if err != nil {
		return err
	}
	event = normalizeCodexAppBackgroundEvent(event)
	if err = e.emitEvent(ctx, event); err != nil {
		if errors.Is(err, ErrServiceUnavailable) {
			return nil
		}
		return err
	}
	if event.Surface != domain.CodexSurfaceApp || event.HookEvent != "PermissionRequest" {
		return nil
	}
	var raw rawHookEvent
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	// 相对路径命令必须连同工作目录展示；这些内容仅进入短暂的内存审批通道。
	pretty, err := json.MarshalIndent(struct {
		CWD       string          `json:"cwd"`
		ToolInput json.RawMessage `json:"toolInput"`
	}{raw.CWD, raw.ToolInput}, "", "  ")
	if err != nil || len(pretty) > approval.MaxDetailBytes {
		return nil
	}
	decision := e.requestDecision(ctx, event, string(pretty))
	if decision != "allow" && decision != "deny" {
		return nil
	}
	return json.NewEncoder(output).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": map[string]string{"behavior": decision},
	}})
}

func (e *Emitter) requestDecision(ctx context.Context, event attention.Event, detail string) string {
	base := strings.TrimSuffix(e.endpoint, "/api/v1/hooks/codex")
	parsed, err := url.Parse(base)
	if err != nil {
		return "fallback"
	}
	origin := parsed.Scheme + "://" + parsed.Host
	healthReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
	if err != nil {
		return "fallback"
	}
	healthResp, err := e.client.Do(healthReq)
	if err != nil {
		return "fallback"
	}
	var health struct {
		ControlToken string `json:"controlToken"`
	}
	err = json.NewDecoder(io.LimitReader(healthResp.Body, 4096)).Decode(&health)
	healthResp.Body.Close()
	if err != nil || healthResp.StatusCode != 200 || health.ControlToken == "" {
		return "fallback"
	}
	body, err := json.Marshal(struct {
		Event  attention.Event `json:"event"`
		Detail string          `json:"detail"`
	}{event, detail})
	if err != nil {
		return "fallback"
	}
	waitCtx, cancel := context.WithTimeout(ctx, approval.WaitTimeout+time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(waitCtx, http.MethodPost, e.endpoint+"/approval", bytes.NewReader(body))
	if err != nil {
		return "fallback"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Dora-Control-Token", health.ControlToken)
	client := *e.client
	client.Timeout = approval.WaitTimeout + time.Second
	resp, err := client.Do(req)
	if err != nil {
		return "fallback"
	}
	defer resp.Body.Close()
	var result struct {
		Decision string `json:"decision"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&result) != nil {
		return "fallback"
	}
	return result.Decision
}
