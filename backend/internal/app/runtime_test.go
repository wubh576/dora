package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wubh576/dora/backend/internal/buildinfo"
)

func TestRuntimeLogsBuildAndEnvironmentInfo(t *testing.T) {
	var output bytes.Buffer
	runtime, err := Start(context.Background(), Config{
		Address:      "127.0.0.1:0",
		DBPath:       filepath.Join(t.TempDir(), "dora.db"),
		CodexHomes:   []string{t.TempDir()},
		ScanInterval: time.Hour,
		Logger:       log.New(&output, "", 0),
		BuildInfo:    buildinfo.New("abc123", true, "2026-07-31T08:00:00Z", "go1.26.5", "darwin", "arm64", "15.6"),
	})
	if err != nil {
		t.Fatalf("Start() 失败: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}
	logOutput := output.String()
	for _, value := range []string{"build=abc123-dirty", "commit=abc123", "dirty=true", "build_time=2026-07-31T08:00:00Z", "go=go1.26.5", "platform=darwin/arm64", "macos=15.6"} {
		if !strings.Contains(logOutput, value) {
			t.Fatalf("启动日志缺少 %q: %q", value, logOutput)
		}
	}
}

func TestRuntimeServesHealthAndEmbeddedPage(t *testing.T) {
	runtime, err := Start(context.Background(), Config{
		Address:    "127.0.0.1:0",
		DBPath:     filepath.Join(t.TempDir(), "dora.db"),
		CodexHomes: []string{t.TempDir()},
		StaticFS: fstest.MapFS{
			"index.html": {Data: []byte("<!doctype html><title>Dora test</title>")},
		},
		ScanInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Start() 失败: %v", err)
	}
	address := runtime.Address()
	if address == "127.0.0.1:0" {
		t.Fatal("运行时未记录实际监听地址")
	}

	response, err := http.Get(runtime.DashboardURL() + "/api/v1/health")
	if err != nil {
		t.Fatalf("请求 health 失败: %v", err)
	}
	defer response.Body.Close()
	var health struct {
		Backend bool `json:"backend"`
		SQLite  bool `json:"sqlite"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("解析 health 失败: %v", err)
	}
	if response.StatusCode != http.StatusOK || !health.Backend || !health.SQLite {
		t.Fatalf("health = status %d, %+v", response.StatusCode, health)
	}

	page, err := http.Get(runtime.DashboardURL())
	if err != nil {
		t.Fatalf("请求嵌入页面失败: %v", err)
	}
	defer page.Body.Close()
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("读取嵌入页面失败: %v", err)
	}
	if page.StatusCode != http.StatusOK || !strings.Contains(string(body), "Dora test") {
		t.Fatalf("嵌入页面 = status %d, %q", page.StatusCode, body)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}
	assertPortReleased(t, address)
}

func TestRuntimeFailsClearlyWhenPortIsOccupied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口失败: %v", err)
	}
	defer listener.Close()
	_, err = Start(context.Background(), Config{
		Address: listener.Addr().String(), DBPath: filepath.Join(t.TempDir(), "dora.db"), CodexHomes: []string{t.TempDir()},
	})
	if err == nil || !strings.Contains(err.Error(), "监听 HTTP 地址") || !strings.Contains(err.Error(), listener.Addr().String()) {
		t.Fatalf("端口冲突错误不明确: %v", err)
	}
}

func TestRuntimeStopsBackgroundWorkAndReleasesPort(t *testing.T) {
	runtime, err := Start(context.Background(), Config{
		Address: "127.0.0.1:0", DBPath: filepath.Join(t.TempDir(), "dora.db"), CodexHomes: []string{t.TempDir()}, ScanInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Start() 失败: %v", err)
	}
	address := runtime.Address()
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("重复 Close() 失败: %v", err)
	}
	assertPortReleased(t, address)
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		valid   bool
	}{
		{address: "127.0.0.1:8080", valid: true}, {address: "127.0.0.1:0", valid: true},
		{address: "0.0.0.0:8080"}, {address: "localhost:8080"}, {address: "127.0.0.1"},
	} {
		err := ValidateLoopbackAddress(test.address)
		if test.valid && err != nil {
			t.Fatalf("ValidateLoopbackAddress(%q) = %v", test.address, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("ValidateLoopbackAddress(%q) 未返回错误", test.address)
		}
	}
}

func assertPortReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("测试端口 %s 未释放: %v", address, err)
	}
	_ = listener.Close()
}
