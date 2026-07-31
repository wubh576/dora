package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

type quotaRoundTrip struct {
	mu        sync.Mutex
	statuses  []int
	bodies    []string
	requests  []*http.Request
	returnErr error
}

func TestQuotaClientRejectsRedirects(t *testing.T) {
	client := NewQuotaClient(nil)
	httpClient, ok := client.doer.(*http.Client)
	if !ok {
		t.Fatalf("doer type = %T, want *http.Client", client.doer)
	}

	err := httpClient.CheckRedirect(nil, nil)
	if !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v, want ErrUseLastResponse", err)
	}
}

func (f *quotaRoundTrip) Do(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request.Clone(request.Context()))
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	index := len(f.requests) - 1
	return &http.Response{
		StatusCode: f.statuses[index],
		Body:       io.NopCloser(strings.NewReader(f.bodies[index])),
		Header:     make(http.Header),
	}, nil
}

func TestQuotaUsesDurationWhenPrimaryAndSecondaryAreSwapped(t *testing.T) {
	home := writeQuotaAuth(t, "chatgpt", true)
	fixture, err := os.ReadFile(filepath.Join("testdata", "quota_swapped.json"))
	if err != nil {
		t.Fatalf("读取 quota fixture 失败: %v", err)
	}
	fake := &quotaRoundTrip{
		statuses: []int{http.StatusOK},
		bodies:   []string{string(fixture)},
	}
	client := NewQuotaClient([]string{home})
	client.doer = fake
	client.now = func() time.Time {
		return time.Date(2026, 7, 31, 4, 0, 0, 0, time.UTC)
	}

	snapshots, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() 失败: %v", err)
	}
	if len(snapshots) != 2 ||
		snapshots[0].WindowKey != domain.QuotaWindowSevenDay ||
		snapshots[1].WindowKey != domain.QuotaWindowFiveHour ||
		snapshots[1].RemainingPercent != 58 {
		t.Fatalf("quota 窗口识别错误: %+v", snapshots)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("quota 请求次数 = %d，期望 1", len(fake.requests))
	}
	assertQuotaRequest(t, fake.requests[0], quotaPrimaryURL)
	encoded, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatalf("编码 snapshots 失败: %v", err)
	}
	if strings.Contains(string(encoded), "fixture-access") || strings.Contains(string(encoded), "fixture-account") {
		t.Fatal("quota snapshot 泄漏凭证")
	}
}

func TestQuotaFallsBackOnlyOnPrimary404(t *testing.T) {
	home := writeQuotaAuth(t, "chatgpt", true)
	body := `{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000}}}`
	fake := &quotaRoundTrip{
		statuses: []int{http.StatusNotFound, http.StatusOK},
		bodies:   []string{"", body},
	}
	client := NewQuotaClient([]string{home})
	client.doer = fake
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("404 fallback 失败: %v", err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("fallback 请求次数 = %d，期望 2", len(fake.requests))
	}
	assertQuotaRequest(t, fake.requests[0], quotaPrimaryURL)
	assertQuotaRequest(t, fake.requests[1], quotaFallbackURL)

	fake = &quotaRoundTrip{statuses: []int{http.StatusInternalServerError}, bodies: []string{""}}
	client.doer = fake
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("500 未返回错误")
	}
	if len(fake.requests) != 1 {
		t.Fatalf("非 404 不应 fallback: %d", len(fake.requests))
	}
}

func TestQuotaReportsUnauthorizedNetworkAndUnsupportedAuth(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewQuotaClient([]string{writeQuotaAuth(t, "chatgpt", true)})
			client.doer = &quotaRoundTrip{statuses: []int{status}, bodies: []string{""}}
			_, err := client.Fetch(context.Background())
			var quotaErr *QuotaError
			if !errors.As(err, &quotaErr) ||
				quotaErr.State != "error" ||
				!strings.Contains(quotaErr.Advice, "codex login") {
				t.Fatalf("%d 错误不明确: %v", status, err)
			}
		})
	}

	t.Run("network", func(t *testing.T) {
		client := NewQuotaClient([]string{writeQuotaAuth(t, "chatgpt", true)})
		client.doer = &quotaRoundTrip{returnErr: errors.New("fixture network failure")}
		_, err := client.Fetch(context.Background())
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) || quotaErr.Message != "无法连接 Codex 配额服务" {
			t.Fatalf("网络错误不明确: %v", err)
		}
	})

	t.Run("api key", func(t *testing.T) {
		client := NewQuotaClient([]string{writeQuotaAuth(t, "apikey", false)})
		_, err := client.Fetch(context.Background())
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) || quotaErr.State != "unsupported" {
			t.Fatalf("API key 状态错误: %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		client := NewQuotaClient([]string{t.TempDir()})
		_, err := client.Fetch(context.Background())
		var quotaErr *QuotaError
		if !errors.As(err, &quotaErr) || quotaErr.State != "not_configured" {
			t.Fatalf("缺失 auth 状态错误: %v", err)
		}
	})
}

func TestQuotaClampsPercentAndRejectsUnknownWindows(t *testing.T) {
	home := writeQuotaAuth(t, "chatgpt", true)
	client := NewQuotaClient([]string{home})
	client.doer = &quotaRoundTrip{
		statuses: []int{http.StatusOK},
		bodies: []string{`{
			"rate_limit": {
				"primary_window": {"used_percent": 120, "limit_window_seconds": 18000},
				"secondary_window": {"used_percent": -5, "limit_window_seconds": 604800}
			}
		}`},
	}
	snapshots, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() clamp 失败: %v", err)
	}
	if snapshots[0].UsedPercent != 100 ||
		snapshots[0].RemainingPercent != 0 ||
		snapshots[1].UsedPercent != 0 ||
		snapshots[1].RemainingPercent != 100 {
		t.Fatalf("quota clamp 错误: %+v", snapshots)
	}

	client.doer = &quotaRoundTrip{
		statuses: []int{http.StatusOK},
		bodies:   []string{`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":60}}}`},
	}
	if _, err := client.Fetch(context.Background()); err == nil {
		t.Fatal("未知 quota 窗口未报错")
	}
}

func writeQuotaAuth(t *testing.T, mode string, oauth bool) string {
	t.Helper()
	home := t.TempDir()
	auth := map[string]any{"auth_mode": mode}
	if oauth {
		auth["tokens"] = map[string]string{
			"access_token": "fixture-access",
			"account_id":   "fixture-account",
			"id_token":     "fixture-id",
		}
	} else {
		auth["OPENAI_API_KEY"] = "fixture-api-key"
	}
	content, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("编码 auth fixture 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), content, 0o600); err != nil {
		t.Fatalf("写入 auth fixture 失败: %v", err)
	}
	return home
}

func assertQuotaRequest(t *testing.T, request *http.Request, expectedURL string) {
	t.Helper()
	if request.URL.String() != expectedURL ||
		request.Header.Get("Authorization") != "Bearer fixture-access" ||
		request.Header.Get("Accept") != "application/json" ||
		request.Header.Get("ChatGPT-Account-ID") != "fixture-account" ||
		request.Header.Get("X-Account-ID") != "fixture-account" ||
		request.Header.Get("ChatClaude-Account-ID") != "fixture-account" {
		t.Fatal("quota 请求缺少固定地址或兼容认证头")
	}
}
