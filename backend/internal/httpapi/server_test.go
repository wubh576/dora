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
