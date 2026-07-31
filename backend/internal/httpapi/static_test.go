package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestStaticResourcesAndSPAFallback(t *testing.T) {
	store := openStaticTestStore(t)
	files := fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><main>Dora</main>")},
		"assets/app-ABC123.js":    {Data: []byte("console.log('dora')")},
		"assets/app-ABC123.css":   {Data: []byte("body { color: black; }")},
		"assets/icon.png":         {Data: []byte("\x89PNG\r\n")},
		"private/diagnostic.json": {Data: []byte(`{"private":true}`)},
	}
	handler := NewHandler(store, Options{StaticFS: files})

	for _, test := range []struct {
		name        string
		path        string
		status      int
		body        string
		contentType string
		cache       string
	}{
		{
			name:        "index",
			path:        "/",
			status:      http.StatusOK,
			body:        "<main>Dora</main>",
			contentType: "text/html",
			cache:       "no-cache",
		},
		{
			name:        "javascript",
			path:        "/assets/app-ABC123.js",
			status:      http.StatusOK,
			body:        "console.log('dora')",
			contentType: "javascript",
			cache:       "immutable",
		},
		{
			name:        "css",
			path:        "/assets/app-ABC123.css",
			status:      http.StatusOK,
			body:        "color: black",
			contentType: "text/css",
			cache:       "immutable",
		},
		{
			name:        "frontend route",
			path:        "/diagnostics",
			status:      http.StatusOK,
			body:        "<main>Dora</main>",
			contentType: "text/html",
			cache:       "no-cache",
		},
		{
			name:   "missing asset",
			path:   "/assets/missing.js",
			status: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.status {
				t.Fatalf("状态码 = %d，期望 %d；响应: %s", recorder.Code, test.status, recorder.Body.String())
			}
			if test.body != "" && !strings.Contains(recorder.Body.String(), test.body) {
				t.Fatalf("响应缺少 %q: %s", test.body, recorder.Body.String())
			}
			if test.contentType != "" && !strings.Contains(recorder.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("Content-Type = %q，期望包含 %q", recorder.Header().Get("Content-Type"), test.contentType)
			}
			if test.cache != "" && !strings.Contains(recorder.Header().Get("Cache-Control"), test.cache) {
				t.Fatalf("Cache-Control = %q，期望包含 %q", recorder.Header().Get("Cache-Control"), test.cache)
			}
		})
	}
}

func TestAPIRoutesTakePriorityOverSPAFallback(t *testing.T) {
	store := openStaticTestStore(t)
	handler := NewHandler(store, Options{StaticFS: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Dora</main>")},
	}})

	healthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(
		healthRecorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/health", nil),
	)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health 状态码 = %d，期望 200", healthRecorder.Code)
	}
	var health healthResponse
	if err := json.NewDecoder(healthRecorder.Body).Decode(&health); err != nil {
		t.Fatalf("health 未返回 JSON: %v", err)
	}
	if !health.Backend || !health.SQLite {
		t.Fatalf("health 状态错误: %+v", health)
	}

	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(
		missingRecorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/not-found", nil),
	)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("未知 API 状态码 = %d，期望 404", missingRecorder.Code)
	}
	if strings.Contains(missingRecorder.Header().Get("Content-Type"), "text/html") ||
		strings.Contains(missingRecorder.Body.String(), "<main>Dora</main>") {
		t.Fatalf("未知 API 被 SPA fallback 接管: %s", missingRecorder.Body.String())
	}
}

func TestStaticHandlerRejectsTraversal(t *testing.T) {
	store := openStaticTestStore(t)
	handler := NewHandler(store, Options{StaticFS: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Dora</main>")},
		"secret.txt": {Data: []byte("do-not-serve-through-traversal")},
	}})

	for _, target := range []string{
		"/../secret.txt",
		"/%2e%2e/secret.txt",
		`/..\secret.txt`,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 状态码 = %d，期望 404", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "do-not-serve") {
			t.Fatalf("%s 暴露了 traversal 目标", target)
		}
	}
}

func TestHandlerWithoutStaticResourcesKeepsAPIMode(t *testing.T) {
	store := openStaticTestStore(t)
	handler := NewHandler(store)

	rootRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rootRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if rootRecorder.Code != http.StatusNotFound {
		t.Fatalf("无静态资源时根路径状态码 = %d，期望 404", rootRecorder.Code)
	}

	healthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(
		healthRecorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/health", nil),
	)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("无静态资源时 health 状态码 = %d，期望 200", healthRecorder.Code)
	}
}

func TestStaticHandlerWithoutIndexReturnsNotFound(t *testing.T) {
	store := openStaticTestStore(t)
	handler := NewHandler(store, Options{StaticFS: fstest.MapFS{
		"assets/app.js": {Data: []byte("console.log('dora')")},
	}})

	for _, target := range []string{"/", "/diagnostics"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 状态码 = %d，期望 404", target, recorder.Code)
		}
	}

	healthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(
		healthRecorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/health", nil),
	)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("缺少 index 时 health 状态码 = %d，期望 200", healthRecorder.Code)
	}
}

func openStaticTestStore(t *testing.T) *dorasqlite.Store {
	t.Helper()
	store, err := dorasqlite.Open(context.Background(), filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})
	return store
}
