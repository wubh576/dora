package menubar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/approval"
)

type Snapshot struct {
	GeneratedAt string        `json:"generatedAt"`
	Usage       SnapshotUsage `json:"usage"`
	Quotas      []QuotaItem   `json:"quotas"`
	Errors      []string      `json:"errors"`
}

type SnapshotUsage struct {
	TodayTokens     int64                   `json:"todayTokens"`
	SevenDayTokens  int64                   `json:"sevenDayTokens"`
	ThirtyDayTokens int64                   `json:"thirtyDayTokens"`
	AllTimeTokens   int64                   `json:"allTimeTokens"`
	TopModel        string                  `json:"topModel"`
	LastScanAt      *string                 `json:"lastScanAt"`
	Stale           bool                    `json:"stale"`
	Providers       []SnapshotProviderUsage `json:"providers"`
}

type SnapshotProviderUsage struct {
	Source string `json:"source"`
	Tokens int64  `json:"tokens"`
}

type QuotaState struct {
	Enabled bool        `json:"enabled"`
	Status  string      `json:"status"`
	Items   []QuotaItem `json:"items"`
	Message string      `json:"message"`
}

type QuotaItem struct {
	WindowKey        string  `json:"windowKey"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetsAt         *string `json:"resetsAt"`
	SourceState      string  `json:"sourceState"`
}

type RuntimeState struct {
	Approvals    []approval.Pending `json:"-"`
	GeneratedAt  string             `json:"generatedAt"`
	WaitingCount int                `json:"waitingCount"`
	RunningCount int                `json:"runningCount"`
	Sessions     []RuntimeSession   `json:"sessions"`
}

type RuntimeSession struct {
	ID            int64  `json:"id"`
	Provider      string `json:"provider"`
	State         string `json:"state"`
	Surface       string `json:"surface"`
	TerminalKind  string `json:"terminalKind"`
	CWDBasename   string `json:"cwdBasename"`
	SessionName   string `json:"sessionName"`
	Model         string `json:"model"`
	PromptPreview string `json:"promptPreview"`
	LastSeenAt    string `json:"lastSeenAt"`
	RequestID     int64  `json:"requestId"`
	Summary       string `json:"summary"`
	Kind          string `json:"kind"`
	WaitingSince  string `json:"waitingSince"`
	WaitSeconds   int64  `json:"waitSeconds"`
	RequestCount  int    `json:"requestCount"`
	Jumpable      bool   `json:"jumpable"`
	JumpReason    string `json:"jumpReason"`
}

type State struct {
	Snapshot Snapshot
	Quota    QuotaState
	Runtime  RuntimeState
}

type Loader interface {
	Load(context.Context) (State, error)
	LoadRuntime(context.Context) (RuntimeState, error)
}

type Client struct {
	controlToken string
	baseURL      string
	http         *http.Client
}

func NewClient(baseURL string, controlToken ...string) *Client {
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 3 * time.Second}}
	if len(controlToken) > 0 {
		c.controlToken = controlToken[0]
	}
	return c
}

func (c *Client) Load(ctx context.Context) (State, error) {
	var state State
	if err := c.getJSON(ctx, "/api/v1/snapshot", &state.Snapshot); err != nil {
		return State{}, err
	}
	// 配额端点异常不能丢弃已经读取到的 token 快照。
	if err := c.getJSON(ctx, "/api/v1/quotas", &state.Quota); err != nil {
		state.Quota = QuotaState{Enabled: true, Status: "error", Message: "Codex 配额状态读取失败"}
	}
	return state, nil
}

func (c *Client) LoadRuntime(ctx context.Context) (RuntimeState, error) {
	var state RuntimeState
	if err := c.getJSON(ctx, "/api/v1/runtime", &state); err != nil {
		return RuntimeState{}, err
	}
	if c.controlToken != "" {
		if err := c.getJSON(ctx, "/api/v1/approvals", &state.Approvals); err != nil {
			return RuntimeState{}, err
		}
	}
	return state, nil
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("创建本地状态请求: %w", err)
	}
	request.Header.Set("Origin", c.baseURL)
	request.Header.Set("X-Dora-Control-Token", c.controlToken)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("连接 Dora 本地服务: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Dora 本地服务返回 HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("解析 Dora 本地状态: %w", err)
	}
	return nil
}

func (c *Client) DecideApproval(ctx context.Context, id int64, decision string) error {
	body, _ := json.Marshal(map[string]any{"id": id, "decision": decision})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/approvals", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("X-Dora-Control-Token", c.controlToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("审批回传失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		return fmt.Errorf("审批请求已经结束，请回原应用查看")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("审批回传失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}
