package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/scan"
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
	if len(breakdown.Items) != 2 || breakdown.Items[0].Name != "gpt-a" || breakdown.Items[0].TotalTokens != 130 {
		t.Fatalf("breakdown 错误: %+v", breakdown)
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
	if len(dashboard.Models) != 2 ||
		len(dashboard.Projects) != 2 ||
		dashboard.Diagnostics.StoredEvents != 3 {
		t.Fatalf("dashboard 数据不完整: %+v", dashboard)
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
		snapshot.Usage.AllTimeTokens != 172 ||
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
