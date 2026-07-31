package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	quotaPrimaryURL  = "https://chatgpt.com/backend-api/wham/usage"
	quotaFallbackURL = "https://chatgpt.com/api/codex/usage"
	quotaBodyLimit   = 1 << 20
)

type QuotaError struct {
	State   string
	Message string
	Advice  string
	Cause   error
}

func (e *QuotaError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *QuotaError) Unwrap() error { return e.Cause }

type QuotaHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type QuotaClient struct {
	homes []string
	doer  QuotaHTTPDoer
	now   func() time.Time
}

type authFile struct {
	AuthMode string `json:"auth_mode"`
	APIKey   string `json:"OPENAI_API_KEY"`
	Tokens   struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		IDToken     string `json:"id_token"`
	} `json:"tokens"`
}

type quotaCredential struct {
	accessToken  string
	accountID    string
	accountLabel string
}

type upstreamQuota struct {
	PlanType  string `json:"plan_type"`
	RateLimit struct {
		PrimaryWindow   *upstreamWindow `json:"primary_window"`
		SecondaryWindow *upstreamWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type upstreamWindow struct {
	UsedPercent       *float64 `json:"used_percent"`
	LimitWindowSecond int64    `json:"limit_window_seconds"`
	ResetAt           *int64   `json:"reset_at"`
}

func NewQuotaClient(homes []string) *QuotaClient {
	transport := &http.Transport{
		Proxy:                 quotaProxy,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	return NewQuotaClientWithHTTP(homes, &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: rejectQuotaRedirect,
	})
}

func NewQuotaClientWithHTTP(homes []string, doer QuotaHTTPDoer) *QuotaClient {
	return &QuotaClient{
		homes: append([]string(nil), homes...),
		doer:  doer,
		now:   time.Now,
	}
}

func rejectQuotaRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func (c *QuotaClient) Fetch(ctx context.Context) ([]domain.QuotaSnapshot, error) {
	credential, err := c.loadCredential()
	if err != nil {
		return nil, err
	}

	response, err := c.fetch(ctx, quotaPrimaryURL, credential)
	if err != nil {
		return nil, err
	}
	if response == nil {
		response, err = c.fetch(ctx, quotaFallbackURL, credential)
		if err != nil {
			return nil, err
		}
	}
	return normalizeQuota(*response, c.now().UTC(), credential.accountLabel)
}

func (c *QuotaClient) loadCredential() (quotaCredential, error) {
	homes, err := ResolveHomes(c.homes)
	if err != nil {
		return quotaCredential{}, quotaFailureWithCause("error", "读取 Codex 登录信息失败", "请检查 Codex home 配置", err)
	}

	foundAuth := false
	foundAPIKey := false
	for _, home := range homes {
		content, err := os.ReadFile(filepath.Join(home, "auth.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return quotaCredential{}, quotaFailureWithCause("error", "读取 Codex 登录信息失败", "请检查 auth.json 权限", err)
		}
		foundAuth = true
		var auth authFile
		if err := json.Unmarshal(content, &auth); err != nil {
			return quotaCredential{}, quotaFailureWithCause("error", "Codex 登录信息格式无效", "请重新运行 codex login", err)
		}
		if strings.TrimSpace(auth.APIKey) != "" || strings.EqualFold(auth.AuthMode, "apikey") {
			foundAPIKey = true
		}
		if !strings.EqualFold(auth.AuthMode, "chatgpt") ||
			strings.TrimSpace(auth.Tokens.AccessToken) == "" ||
			strings.TrimSpace(auth.Tokens.AccountID) == "" {
			continue
		}
		return quotaCredential{
			accessToken:  auth.Tokens.AccessToken,
			accountID:    auth.Tokens.AccountID,
			accountLabel: anonymizedAccountLabel(auth.Tokens.IDToken),
		}, nil
	}

	if foundAPIKey {
		return quotaCredential{}, quotaFailure("unsupported", "API key 不提供 Codex 订阅配额", "请使用 codex login 登录 ChatGPT 订阅账号")
	}
	if foundAuth {
		return quotaCredential{}, quotaFailure("not_configured", "未找到可用的 Codex OAuth 登录", "请重新运行 codex login")
	}
	return quotaCredential{}, quotaFailure("not_configured", "未找到 Codex 登录信息", "请先运行 codex login")
}

func (c *QuotaClient) fetch(
	ctx context.Context,
	endpoint string,
	credential quotaCredential,
) (*upstreamQuota, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, quotaFailure("error", "创建 Codex 配额请求失败", "请重试")
	}
	request.Header.Set("Authorization", "Bearer "+credential.accessToken)
	if credential.accountID != "" {
		request.Header.Set("ChatGPT-Account-ID", credential.accountID)
		request.Header.Set("X-Account-ID", credential.accountID)
		request.Header.Set("ChatClaude-Account-ID", credential.accountID)
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.doer.Do(request)
	if err != nil {
		return nil, quotaFailureWithCause("error", "无法连接 Codex 配额服务", "请检查网络后重试", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && endpoint == quotaPrimaryURL {
		return nil, nil
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, quotaFailure("error", "Codex 登录已过期或无权读取配额", "请重新运行 codex login")
	}
	if response.StatusCode != http.StatusOK {
		return nil, quotaFailureWithCause("error", "Codex 配额服务返回错误", "请稍后重试", fmt.Errorf("HTTP %d", response.StatusCode))
	}

	var value upstreamQuota
	decoder := json.NewDecoder(io.LimitReader(response.Body, quotaBodyLimit))
	if err := decoder.Decode(&value); err != nil {
		return nil, quotaFailureWithCause("error", "Codex 配额响应格式无效", "请稍后重试", err)
	}
	return &value, nil
}

func normalizeQuota(value upstreamQuota, fetchedAt time.Time, accountLabel string) ([]domain.QuotaSnapshot, error) {
	result := make([]domain.QuotaSnapshot, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, window := range []*upstreamWindow{value.RateLimit.PrimaryWindow, value.RateLimit.SecondaryWindow} {
		if window == nil || window.UsedPercent == nil {
			continue
		}
		windowKey, label := quotaWindow(window.LimitWindowSecond)
		if windowKey == "" {
			continue
		}
		if _, exists := seen[windowKey]; exists {
			continue
		}
		seen[windowKey] = struct{}{}
		used := clampPercent(*window.UsedPercent)
		snapshot := domain.QuotaSnapshot{
			Provider:         domain.CodexSource,
			WindowKey:        windowKey,
			Label:            label,
			UsedPercent:      used,
			RemainingPercent: clampPercent(100 - used),
			FetchedAt:        fetchedAt,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
			Plan:             value.PlanType,
			AccountLabel:     accountLabel,
		}
		if window.ResetAt != nil && *window.ResetAt > 0 {
			reset := time.Unix(*window.ResetAt, 0).UTC()
			snapshot.ResetsAt = &reset
		}
		result = append(result, snapshot)
	}
	if len(result) == 0 {
		return nil, quotaFailure("error", "Codex 配额响应没有可识别的 5 小时或 7 日窗口", "请稍后重试")
	}
	return result, nil
}

func quotaWindow(seconds int64) (string, string) {
	switch seconds {
	case int64(5 * time.Hour / time.Second):
		return domain.QuotaWindowFiveHour, "5 hours"
	case int64(7 * 24 * time.Hour / time.Second):
		return domain.QuotaWindowSevenDay, "7 days"
	default:
		return "", ""
	}
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func anonymizedAccountLabel(idToken string) string {
	if idToken == "" {
		return "Codex account"
	}
	sum := sha256.Sum256([]byte(idToken))
	return fmt.Sprintf("Codex account %s", hex.EncodeToString(sum[:4]))
}

func quotaFailure(state, message, advice string) error {
	return &QuotaError{State: state, Message: message, Advice: advice}
}

func quotaFailureWithCause(state, message, advice string, cause error) error {
	return &QuotaError{State: state, Message: message, Advice: advice, Cause: cause}
}
