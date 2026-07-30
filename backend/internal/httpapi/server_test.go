package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
