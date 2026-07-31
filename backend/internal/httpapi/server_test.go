package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/analytics"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/claudecode"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestHealthReadsSQLiteState(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	expectedInitializedAt, err := store.InitializedAt(context.Background())
	if err != nil {
		t.Fatalf("读取测试初始化时间失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	NewHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 %d；响应: %s", response.Code, http.StatusOK, response.Body.String())
	}

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Backend {
		t.Fatal("backend = false，期望 true")
	}
	if !body.SQLite {
		t.Fatal("sqlite = false，期望 true")
	}
	if body.InitializedAt == "" {
		t.Fatal("initializedAt 为空")
	}
	initializedAt, err := time.Parse(time.RFC3339Nano, body.InitializedAt)
	if err != nil {
		t.Fatalf("initializedAt 格式错误: %v", err)
	}
	if !initializedAt.Equal(expectedInitializedAt) {
		t.Fatalf("initializedAt = %s，期望 %s", initializedAt, expectedInitializedAt)
	}
}

func TestHealthReportsUnavailableSQLite(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	NewHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 %d", response.Code, http.StatusServiceUnavailable)
	}

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Backend {
		t.Fatal("backend = false，期望 true")
	}
	if body.SQLite {
		t.Fatal("sqlite = true，期望 false")
	}
}

func TestHealthReturnsStartupControlToken(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	NewHandler(store, Options{ControlToken: "test-token"}).ServeHTTP(response, request)

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.ControlToken != "test-token" {
		t.Fatalf("controlToken = %q，期望 test-token", body.ControlToken)
	}
}

func TestHealthReturnsRunningBuildInfo(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	info := buildinfo.New("abc123", true, "2026-07-31T08:00:00Z", "go1.26.5", "darwin", "arm64", "15.6")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	NewHandler(store, Options{BuildInfo: info}).ServeHTTP(response, request)

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.BuildInfo != info {
		t.Fatalf("buildInfo = %+v，期望 %+v", body.BuildInfo, info)
	}
}

func TestManualScanRequiresOriginAndControlToken(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	home := t.TempDir()
	session := filepath.Join(home, "sessions", "usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}}` + "\n"
	if err := os.WriteFile(session, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 fixture 失败: %v", err)
	}

	handler := NewHandler(store, Options{
		Scanner:        scan.New(store, []string{home}),
		ControlToken:   "test-token",
		AllowedOrigins: []string{"http://127.0.0.1:5173"},
	})

	for _, test := range []struct {
		name   string
		origin string
		token  string
		status int
	}{
		{name: "missing token", origin: "http://127.0.0.1:5173", status: http.StatusForbidden},
		{name: "wrong origin", origin: "http://evil.example", token: "test-token", status: http.StatusForbidden},
		{name: "valid", origin: "http://127.0.0.1:5173", token: "test-token", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/scan", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Dora-Control-Token", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("状态码 = %d，期望 %d；响应: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestDiagnosticsDoesNotExposeRawScanError(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	home := t.TempDir()
	session := filepath.Join(home, "sessions", "private-name.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}` + "\n"
	if err := os.WriteFile(session, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 fixture 失败: %v", err)
	}
	usageScanner := scan.New(store, []string{home})
	if _, err := usageScanner.Scan(ctx, false); err == nil {
		t.Fatal("无效 fixture 扫描未失败")
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取原始扫描状态失败: %v", err)
	}
	if strings.Contains(state.LastError, session) || !strings.Contains(state.LastError, "不能为负数") {
		t.Fatalf("原始扫描状态未脱敏或缺少原因: %q", state.LastError)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil)
	response := httptest.NewRecorder()
	NewHandler(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "private-name") || strings.Contains(body, "不能为负数") {
		t.Fatalf("diagnostics 泄漏原始扫描错误: %s", body)
	}
	if !strings.Contains(body, `"status":"error"`) || !strings.Contains(body, `"advice":`) {
		t.Fatalf("diagnostics 缺少可行动状态: %s", body)
	}
}

func TestUsageAnalyticsEndpointsShareTokenWindow(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, now)

	handler := NewHandler(store, Options{
		Location: time.UTC,
		Now:      func() time.Time { return now },
	})

	summaryRecorder := httptest.NewRecorder()
	handler.ServeHTTP(summaryRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/summary?range=7D", nil))
	if summaryRecorder.Code != http.StatusOK {
		t.Fatalf("summary 状态码 = %d；响应: %s", summaryRecorder.Code, summaryRecorder.Body.String())
	}
	var summary summaryResponse
	if err := json.NewDecoder(summaryRecorder.Body).Decode(&summary); err != nil {
		t.Fatalf("解析 summary 失败: %v", err)
	}
	if summary.Range != "7D" || summary.TotalTokens != 162 || summary.EventCount != 2 {
		t.Fatalf("summary 错误: %+v", summary)
	}

	oneDayRecorder := httptest.NewRecorder()
	handler.ServeHTTP(oneDayRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/summary?range=1D", nil))
	if oneDayRecorder.Code != http.StatusOK {
		t.Fatalf("1D summary 状态码 = %d；响应: %s", oneDayRecorder.Code, oneDayRecorder.Body.String())
	}
	var oneDay summaryResponse
	if err := json.NewDecoder(oneDayRecorder.Body).Decode(&oneDay); err != nil {
		t.Fatalf("解析 1D summary 失败: %v", err)
	}
	if oneDay.Range != "1D" || oneDay.TotalTokens != 120 || oneDay.EventCount != 1 {
		t.Fatalf("1D summary 错误: %+v", oneDay)
	}

	timelineRecorder := httptest.NewRecorder()
	handler.ServeHTTP(timelineRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/timeline?range=7D&granularity=day", nil))
	var timeline timelineResponse
	if err := json.NewDecoder(timelineRecorder.Body).Decode(&timeline); err != nil {
		t.Fatalf("解析 timeline 失败: %v", err)
	}
	var timelineTotal int64
	for _, point := range timeline.Points {
		timelineTotal += point.TotalTokens
	}
	if timelineTotal != summary.TotalTokens {
		t.Fatalf("timeline total = %d，summary total = %d", timelineTotal, summary.TotalTokens)
	}

	breakdownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(breakdownRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/breakdown?range=All&dimension=model", nil))
	var breakdown breakdownResponse
	if err := json.NewDecoder(breakdownRecorder.Body).Decode(&breakdown); err != nil {
		t.Fatalf("解析 breakdown 失败: %v", err)
	}
	if len(breakdown.Items) != 2 || breakdown.Items[0].Name != "gpt-a" || breakdown.Items[0].TotalTokens != 120 {
		t.Fatalf("breakdown 错误: %+v", breakdown)
	}
}

func TestMultiProviderAPICombinesTotalsAndKeepsSourcesTraceable(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, now)
	seedClaudeUsage(t, store, now)
	handler := NewHandler(store, Options{Location: time.UTC, Now: func() time.Time { return now }})

	dashboardRecorder := httptest.NewRecorder()
	handler.ServeHTTP(dashboardRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard?range=7D", nil))
	if dashboardRecorder.Code != http.StatusOK {
		t.Fatalf("dashboard 状态码 = %d；响应: %s", dashboardRecorder.Code, dashboardRecorder.Body.String())
	}
	var dashboard dashboardResponse
	if err := json.NewDecoder(dashboardRecorder.Body).Decode(&dashboard); err != nil {
		t.Fatalf("解析 dashboard 失败: %v", err)
	}
	if dashboard.Summary.TotalTokens != 212 || dashboard.Summary.EventCount != 3 || len(dashboard.Summary.Providers) != 2 {
		t.Fatalf("多 provider 汇总错误: %+v", dashboard)
	}
	if dashboard.Summary.Providers[0].Source != domain.CodexSource || dashboard.Summary.Providers[0].TotalTokens != 162 ||
		dashboard.Summary.Providers[1].Source != domain.ClaudeCodeSource || dashboard.Summary.Providers[1].TotalTokens != 50 {
		t.Fatalf("provider 用量错误: %+v", dashboard.Summary.Providers)
	}
	if len(dashboard.ProviderDiagnostics) != 2 || dashboard.ProviderDiagnostics[1].SessionCount != 1 ||
		dashboard.ProviderDiagnostics[1].ParserVersion != claudecode.ParserVersion {
		t.Fatalf("provider 诊断错误: %+v", dashboard.ProviderDiagnostics)
	}
	if strings.Contains(dashboardRecorder.Body.String(), "sessionId") || strings.Contains(dashboardRecorder.Body.String(), "full/path") {
		t.Fatalf("dashboard 暴露了 session 或完整路径: %s", dashboardRecorder.Body.String())
	}

	for _, test := range []struct {
		dimension string
		wantItems int
	}{{"provider", 2}, {"provider_model", 3}} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/breakdown?range=7D&dimension="+test.dimension, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s breakdown 状态码 = %d；响应: %s", test.dimension, recorder.Code, recorder.Body.String())
		}
		var breakdown breakdownResponse
		if err := json.NewDecoder(recorder.Body).Decode(&breakdown); err != nil {
			t.Fatalf("解析 %s breakdown 失败: %v", test.dimension, err)
		}
		if len(breakdown.Items) != test.wantItems {
			t.Fatalf("%s breakdown 未保留 provider 归属: %+v", test.dimension, breakdown.Items)
		}
	}

	snapshotRecorder := httptest.NewRecorder()
	handler.ServeHTTP(snapshotRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot snapshotResponse
	if err := json.NewDecoder(snapshotRecorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析 snapshot 失败: %v", err)
	}
	if snapshot.Usage.TodayTokens != 170 || snapshot.Usage.SevenDayTokens != 212 ||
		snapshot.Usage.AllTimeTokens != 212 || len(snapshot.Usage.Providers) != 2 {
		t.Fatalf("多 provider snapshot 错误: %+v", snapshot)
	}

	if err := store.BeginProviderUsageScan(ctx, domain.ClaudeCodeSource, "claude-failed-run", "incremental", now); err != nil {
		t.Fatalf("创建 Claude 失败扫描: %v", err)
	}
	if err := store.FailProviderUsageScan(ctx, domain.ClaudeCodeSource, "claude-failed-run", now.Add(time.Second), 1, 0, errors.New("fixture failure")); err != nil {
		t.Fatalf("记录 Claude 失败扫描: %v", err)
	}
	failureRecorder := httptest.NewRecorder()
	handler.ServeHTTP(failureRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard?range=7D", nil))
	var failureDashboard dashboardResponse
	if err := json.NewDecoder(failureRecorder.Body).Decode(&failureDashboard); err != nil {
		t.Fatalf("解析 provider 失败后的 dashboard: %v", err)
	}
	if failureDashboard.Summary.TotalTokens != 212 || failureDashboard.ProviderDiagnostics[0].Status != "ready" ||
		failureDashboard.ProviderDiagnostics[1].Status != "error" {
		t.Fatalf("单 provider 失败破坏了合计或另一 provider: %+v", failureDashboard)
	}
}

func TestSnapshotUsesOldestActiveProviderScanForFreshness(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, now)
	claudeReference := now.Add(-11 * time.Minute)
	seedClaudeUsage(t, store, claudeReference)

	recorder := httptest.NewRecorder()
	NewHandler(store, Options{Location: time.UTC, Now: func() time.Time { return now }}).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot snapshotResponse
	if err := json.NewDecoder(recorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析 snapshot 失败: %v", err)
	}
	wantLastScan := claudeReference.Add(-30 * time.Second)
	if !snapshot.Usage.Stale || snapshot.Usage.LastScanAt == nil {
		t.Fatalf("旧 provider 未使合并 snapshot 过期: %+v", snapshot.Usage)
	}
	lastScan, err := time.Parse(time.RFC3339Nano, *snapshot.Usage.LastScanAt)
	if err != nil || !lastScan.Equal(wantLastScan) {
		t.Fatalf("合并 lastScanAt = %v, %v，期望 %v", lastScan, err, wantLastScan)
	}
}

func TestDashboardUsesOneConsistentSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, now)
	nowCalls := 0
	handler := NewHandler(store, Options{
		Location: time.UTC,
		Now: func() time.Time {
			nowCalls++
			return now
		},
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard?range=7D", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("dashboard 状态码 = %d；响应: %s", recorder.Code, recorder.Body.String())
	}
	var dashboard dashboardResponse
	if err := json.NewDecoder(recorder.Body).Decode(&dashboard); err != nil {
		t.Fatalf("解析 dashboard 失败: %v", err)
	}
	if nowCalls != 1 {
		t.Fatalf("dashboard 调用了 %d 次时钟，期望 1 次", nowCalls)
	}
	var timelineTotal int64
	for _, point := range dashboard.Timeline {
		timelineTotal += point.TotalTokens
	}
	if dashboard.Summary.Range != "7D" ||
		dashboard.Summary.TotalTokens != 162 ||
		timelineTotal != dashboard.Summary.TotalTokens {
		t.Fatalf("dashboard 时间窗口不一致: %+v", dashboard)
	}
	if dashboard.Summary.Cost.UnpricedTokens != dashboard.Summary.TotalTokens ||
		dashboard.Summary.Cost.PricedTokens != 0 ||
		dashboard.Summary.Cost.SourceURL == "" {
		t.Fatalf("dashboard 费用估算元数据不完整: %+v", dashboard.Summary.Cost)
	}
	if len(dashboard.Models) != 2 ||
		len(dashboard.Projects) != 2 ||
		dashboard.Diagnostics.StoredEvents != 3 {
		t.Fatalf("dashboard 数据不完整: %+v", dashboard)
	}
	if dashboard.Activity.StartDate != analytics.TrackingStartDate ||
		dashboard.Activity.EndDate != "2026-07-31" ||
		len(dashboard.Activity.Days) != 2 {
		t.Fatalf("dashboard 热力图错误: %+v", dashboard.Activity)
	}
	var activityTotal int64
	for _, point := range dashboard.Activity.Days {
		activityTotal += point.TotalTokens
	}
	if activityTotal != 162 {
		t.Fatalf("dashboard 热力图总量 = %d，期望 162", activityTotal)
	}
}

func TestSnapshotAndDiagnosticsUsePersistedUsage(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, now)
	handler := NewHandler(store, Options{Location: time.UTC, Now: func() time.Time { return now }})

	snapshotRecorder := httptest.NewRecorder()
	handler.ServeHTTP(snapshotRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot snapshotResponse
	if err := json.NewDecoder(snapshotRecorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析 snapshot 失败: %v", err)
	}
	if snapshot.Usage.TodayTokens != 120 ||
		snapshot.Usage.SevenDayTokens != 162 ||
		snapshot.Usage.AllTimeTokens != 162 ||
		snapshot.Usage.TopModel != "gpt-a" ||
		snapshot.Usage.Stale ||
		snapshot.Usage.LastScanAt == nil {
		t.Fatalf("snapshot 错误: %+v", snapshot)
	}
	if snapshot.Quotas == nil || snapshot.Errors == nil {
		t.Fatalf("snapshot 数组不能为 null: %+v", snapshot)
	}
	if snapshot.Usage.TodayTokens > snapshot.Usage.SevenDayTokens ||
		snapshot.Usage.SevenDayTokens > snapshot.Usage.AllTimeTokens {
		t.Fatalf("snapshot 窗口不是同一份数据的子集: %+v", snapshot.Usage)
	}

	diagnosticsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(diagnosticsRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil))
	var diagnostics diagnosticsResponse
	if err := json.NewDecoder(diagnosticsRecorder.Body).Decode(&diagnostics); err != nil {
		t.Fatalf("解析 diagnostics 失败: %v", err)
	}
	if diagnostics.Usage.StoredEvents != 3 || diagnostics.Usage.ParserVersion != codex.ParserVersion {
		t.Fatalf("diagnostics usage 错误: %+v", diagnostics)
	}
}

func TestFailedScanKeepsOldSnapshotStale(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()

	successAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	seedAnalyticsUsage(t, store, successAt.Add(time.Minute))
	failureAt := successAt.Add(11 * time.Minute)
	if err := store.BeginUsageScan(ctx, "failed-run", "incremental", failureAt.Add(-time.Second)); err != nil {
		t.Fatalf("创建失败扫描记录失败: %v", err)
	}
	if err := store.FailUsageScan(ctx, "failed-run", failureAt, 2, 1, errors.New("fixture failure")); err != nil {
		t.Fatalf("记录失败扫描失败: %v", err)
	}

	handler := NewHandler(store, Options{Location: time.UTC, Now: func() time.Time { return failureAt }})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot snapshotResponse
	if err := json.NewDecoder(recorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析失败后的 snapshot 失败: %v", err)
	}
	if !snapshot.Usage.Stale ||
		snapshot.Usage.LastScanAt == nil ||
		*snapshot.Usage.LastScanAt != successAt.Format(time.RFC3339Nano) {
		t.Fatalf("失败扫描掩盖了旧数据新鲜度: %+v", snapshot.Usage)
	}
	if len(snapshot.Errors) == 0 {
		t.Fatalf("失败后的 snapshot 未返回错误状态: %+v", snapshot)
	}
}

func TestUsageEndpointsRejectInvalidQueries(t *testing.T) {
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	handler := NewHandler(store)

	for _, path := range []string{
		"/api/v1/summary?range=1Y",
		"/api/v1/timeline?granularity=hour",
		"/api/v1/breakdown?dimension=source",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s 状态码 = %d，期望 400", path, response.Code)
		}
	}
}

func TestQuotaConsentRefreshAndReadFlow(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	settingsStore := settings.New(filepath.Join(t.TempDir(), "settings.json"))
	provider := &httpQuotaProvider{}
	service := quota.NewService(provider, store, settingsStore)
	handler := NewHandler(store, Options{
		ControlToken:   "test-token",
		AllowedOrigins: []string{"http://127.0.0.1:5173"},
		QuotaService:   service,
		Settings:       settingsStore,
	})

	settingsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(settingsRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	var initialSettings settingsResponse
	if err := json.NewDecoder(settingsRecorder.Body).Decode(&initialSettings); err != nil {
		t.Fatalf("解析 settings 失败: %v", err)
	}
	if !initialSettings.CodexQuotaConsent || provider.callCount() != 0 {
		t.Fatalf("quota 默认设置错误: settings=%+v calls=%d", initialSettings, provider.callCount())
	}

	quotaRecorder := httptest.NewRecorder()
	handler.ServeHTTP(quotaRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/quotas", nil))
	var initialQuota quotaResponse
	if err := json.NewDecoder(quotaRecorder.Body).Decode(&initialQuota); err != nil {
		t.Fatalf("解析初始 quota 失败: %v", err)
	}
	if !initialQuota.Enabled || initialQuota.Items == nil || provider.callCount() != 0 {
		t.Fatalf("GET quotas 未反映默认开启状态: %+v calls=%d", initialQuota, provider.callCount())
	}

	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(
		forbidden,
		httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"codexQuotaConsent":true}`)),
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("无保护设置写入状态码 = %d，期望 403", forbidden.Code)
	}
	invalid := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{}`))
	invalid.Header.Set("Origin", "http://127.0.0.1:5173")
	invalid.Header.Set("X-Dora-Control-Token", "test-token")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("缺失 consent 状态码 = %d，期望 400", invalidRecorder.Code)
	}

	enable := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/settings",
		strings.NewReader(`{"codexQuotaConsent":true}`),
	)
	enable.Header.Set("Origin", "http://127.0.0.1:5173")
	enable.Header.Set("X-Dora-Control-Token", "test-token")
	enableRecorder := httptest.NewRecorder()
	handler.ServeHTTP(enableRecorder, enable)
	if enableRecorder.Code != http.StatusOK {
		t.Fatalf("启用 quota 状态码 = %d；响应: %s", enableRecorder.Code, enableRecorder.Body.String())
	}

	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/quota/refresh", nil)
	refresh.Header.Set("Origin", "http://127.0.0.1:5173")
	refresh.Header.Set("X-Dora-Control-Token", "test-token")
	refreshRecorder := httptest.NewRecorder()
	handler.ServeHTTP(refreshRecorder, refresh)
	if refreshRecorder.Code != http.StatusOK {
		t.Fatalf("刷新 quota 状态码 = %d；响应: %s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
	var refreshed quotaResponse
	responseBody := append([]byte(nil), refreshRecorder.Body.Bytes()...)
	if err := json.Unmarshal(responseBody, &refreshed); err != nil {
		t.Fatalf("解析刷新 quota 失败: %v", err)
	}
	if !refreshed.Enabled || refreshed.Status != "ready" || len(refreshed.Items) != 2 {
		t.Fatalf("刷新 quota 响应错误: %+v", refreshed)
	}
	body := string(responseBody)
	if strings.Contains(body, "fixture-access") || strings.Contains(body, "fixture-account") {
		t.Fatal("quota API 泄漏凭证")
	}

	snapshotRecorder := httptest.NewRecorder()
	handler.ServeHTTP(snapshotRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot snapshotResponse
	if err := json.NewDecoder(snapshotRecorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析 quota snapshot 失败: %v", err)
	}
	if len(snapshot.Quotas) != 2 || provider.callCount() != 1 {
		t.Fatalf("snapshot quota 错误: %+v calls=%d", snapshot.Quotas, provider.callCount())
	}

	disable := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/settings",
		strings.NewReader(`{"codexQuotaConsent":false}`),
	)
	disable.Header.Set("Origin", "http://127.0.0.1:5173")
	disable.Header.Set("X-Dora-Control-Token", "test-token")
	disableRecorder := httptest.NewRecorder()
	handler.ServeHTTP(disableRecorder, disable)
	if disableRecorder.Code != http.StatusOK {
		t.Fatalf("关闭 quota 状态码 = %d", disableRecorder.Code)
	}
	afterDisableRecorder := httptest.NewRecorder()
	handler.ServeHTTP(afterDisableRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/quotas", nil))
	var afterDisable quotaResponse
	if err := json.NewDecoder(afterDisableRecorder.Body).Decode(&afterDisable); err != nil {
		t.Fatalf("解析关闭后 quota 失败: %v", err)
	}
	if afterDisable.Enabled || len(afterDisable.Items) != 0 {
		t.Fatalf("关闭 consent 后仍返回 quota: %+v", afterDisable)
	}
}

func TestQuotaCredentialsStayOutOfPersistenceAPIAndLogs(t *testing.T) {
	const (
		accessToken = "sentinel-access-token"
		accountID   = "sentinel-account-id"
		idToken     = "sentinel-id-token"
	)
	ctx := context.Background()
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("创建 auth 目录失败: %v", err)
	}
	authContent, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token": accessToken,
			"account_id":   accountID,
			"id_token":     idToken,
		},
	})
	if err != nil {
		t.Fatalf("编码 auth fixture 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), authContent, 0o600); err != nil {
		t.Fatalf("写入 auth fixture 失败: %v", err)
	}

	dbPath := filepath.Join(root, "dora.db")
	settingsPath := filepath.Join(root, "settings.json")
	store, err := dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	defer store.Close()
	settingsStore := settings.New(settingsPath)
	if err := settingsStore.Save(settings.Values{CodexQuotaConsent: true}); err != nil {
		t.Fatalf("保存 consent 失败: %v", err)
	}
	doer := &credentialQuotaHTTP{}
	client := codex.NewQuotaClientWithHTTP([]string{home}, doer)
	service := quota.NewService(client, store, settingsStore)

	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousLogWriter)

	view, err := service.Refresh(ctx, true)
	if err != nil {
		t.Fatalf("真实 quota 链路刷新失败: %v", err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("真实 quota 链路配额窗口 = %d，期望 2", len(view.Items))
	}
	if doer.request == nil {
		t.Fatal("真实 quota 链路未发出请求")
	}
	if doer.request.Header.Get("Authorization") != "Bearer "+accessToken ||
		doer.request.Header.Get("Accept") != "application/json" ||
		doer.request.Header.Get("ChatGPT-Account-ID") != accountID ||
		doer.request.Header.Get("X-Account-ID") != accountID ||
		doer.request.Header.Get("ChatClaude-Account-ID") != accountID {
		t.Fatal("真实 quota 请求缺少兼容认证头")
	}

	handler := NewHandler(store, Options{QuotaService: service, Settings: settingsStore})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/quotas", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("quota API 状态码 = %d；响应: %s", recorder.Code, recorder.Body.String())
	}

	settingsContent, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("读取 settings 失败: %v", err)
	}
	persisted := append([]byte(nil), settingsContent...)
	databaseFiles, err := filepath.Glob(dbPath + "*")
	if err != nil {
		t.Fatalf("查找 SQLite 文件失败: %v", err)
	}
	for _, path := range databaseFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 SQLite 文件 %s 失败: %v", path, err)
		}
		persisted = append(persisted, content...)
	}

	for name, content := range map[string][]byte{
		"SQLite/settings": persisted,
		"API":             recorder.Body.Bytes(),
		"log":             logs.Bytes(),
	} {
		for _, secret := range []string{accessToken, accountID, idToken} {
			if bytes.Contains(content, []byte(secret)) {
				t.Fatalf("%s 泄漏凭证", name)
			}
		}
	}
}

type httpQuotaProvider struct {
	mu    sync.Mutex
	calls int
}

type credentialQuotaHTTP struct {
	request *http.Request
}

func (c *credentialQuotaHTTP) Do(request *http.Request) (*http.Response, error) {
	c.request = request.Clone(request.Context())
	body := `{
		"plan_type": "pro",
		"rate_limit": {
			"primary_window": {
				"used_percent": 25,
				"limit_window_seconds": 18000,
				"reset_at": 1785474000
			},
			"secondary_window": {
				"used_percent": 50,
				"limit_window_seconds": 604800,
				"reset_at": 1786057200
			}
		}
	}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func (p *httpQuotaProvider) Fetch(context.Context) ([]domain.QuotaSnapshot, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	now := time.Now().UTC()
	resetFiveHour := now.Add(5 * time.Hour)
	resetSevenDay := now.Add(7 * 24 * time.Hour)
	return []domain.QuotaSnapshot{
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowFiveHour,
			Label:            "5 hours",
			UsedPercent:      25,
			RemainingPercent: 75,
			ResetsAt:         &resetFiveHour,
			FetchedAt:        now,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
			Plan:             "pro",
			AccountLabel:     "Codex account 12345678",
		},
		{
			Provider:         domain.CodexSource,
			WindowKey:        domain.QuotaWindowSevenDay,
			Label:            "7 days",
			UsedPercent:      50,
			RemainingPercent: 50,
			ResetsAt:         &resetSevenDay,
			FetchedAt:        now,
			Source:           "codex_oauth",
			SourceState:      "confirmed",
			Plan:             "pro",
			AccountLabel:     "Codex account 12345678",
		},
	}, nil
}

func (p *httpQuotaProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func seedAnalyticsUsage(t *testing.T, store *dorasqlite.Store, now time.Time) {
	t.Helper()
	events := []domain.UsageEvent{
		{
			Source:                   domain.CodexSource,
			DedupKey:                 "today",
			OccurredAt:               now.Add(-time.Hour),
			Model:                    "gpt-a",
			Project:                  "dora",
			InputTokens:              60,
			OutputTokens:             15,
			CachedInputTokens:        30,
			CacheCreationInputTokens: 10,
			ReasoningOutputTokens:    5,
			ReportedTotalTokens:      120,
			TotalTokens:              120,
		},
		{
			Source:              domain.CodexSource,
			DedupKey:            "recent",
			OccurredAt:          now.AddDate(0, 0, -2),
			Model:               "gpt-b",
			Project:             "other",
			ReportedTotalTokens: 42,
			TotalTokens:         42,
		},
		{
			Source:              domain.CodexSource,
			DedupKey:            "older",
			OccurredAt:          now.AddDate(0, 0, -8),
			Model:               "gpt-a",
			Project:             "dora",
			ReportedTotalTokens: 10,
			TotalTokens:         10,
		},
	}
	finishedAt := now.Add(-time.Minute)
	if err := store.BeginUsageScan(context.Background(), "analytics-run", "full", finishedAt.Add(-time.Second)); err != nil {
		t.Fatalf("创建 analytics scan 失败: %v", err)
	}
	if err := store.CompleteUsageScan(
		context.Background(),
		"analytics-run",
		finishedAt,
		events,
		nil,
		2,
		3,
		"",
	); err != nil {
		t.Fatalf("保存 analytics usage 失败: %v", err)
	}
}

func seedClaudeUsage(t *testing.T, store *dorasqlite.Store, now time.Time) {
	t.Helper()
	const runID = "claude-analytics-run"
	finishedAt := now.Add(-30 * time.Second)
	if err := store.BeginProviderUsageScan(context.Background(), domain.ClaudeCodeSource, runID, "full", finishedAt.Add(-time.Second)); err != nil {
		t.Fatalf("创建 Claude analytics scan 失败: %v", err)
	}
	events := []domain.UsageEvent{{
		Source:              domain.ClaudeCodeSource,
		DedupKey:            "claude-today",
		OccurredAt:          now.Add(-30 * time.Minute),
		Model:               "gpt-a",
		Project:             "dora",
		InputTokens:         30,
		OutputTokens:        20,
		ReportedTotalTokens: 50,
		TotalTokens:         50,
	}}
	if err := store.CompleteProviderUsageScanWithMetrics(
		context.Background(), domain.ClaudeCodeSource, runID, finishedAt, events, nil, 1, 1, "",
		domain.UsageScanMetrics{Status: "ready", ConfigFound: true, SessionCount: 1, ParserVersion: claudecode.ParserVersion},
	); err != nil {
		t.Fatalf("保存 Claude analytics usage 失败: %v", err)
	}
}
