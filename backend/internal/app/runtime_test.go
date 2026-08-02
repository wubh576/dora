package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestRuntimeMarksOnlyPreStartupAttentionAsNotified(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dora.db")
	store, err := dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	event := domain.CodexHookEvent{
		ExternalSessionID: "runtime-restart",
		EventName:         "PermissionRequest",
		TurnID:            "turn",
		CWDBasename:       "dora",
		Surface:           domain.CodexSurfaceApp,
		ToolName:          "apply_patch",
		EventKey:          "codex:runtime-restart",
		ReceivedAt:        time.Now().UTC().Add(-time.Minute),
	}
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatalf("保存历史等待请求失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}

	runtime, err := Start(ctx, Config{
		Address:      "127.0.0.1:0",
		DBPath:       dbPath,
		CodexHomes:   []string{filepath.Join(t.TempDir(), "codex")},
		ScanInterval: time.Hour,
		Logger:       log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("Start() 失败: %v", err)
	}
	defer runtime.Close()
	requests, err := runtime.store.UnnotifiedAttention(ctx)
	if err != nil || len(requests) != 0 {
		t.Fatalf("Runtime 重复提醒启动前的请求: %+v, %v", requests, err)
	}
}

func TestRuntimeBindFailureDoesNotMarkAttentionNotified(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dora.db")
	store, err := dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	event := domain.CodexHookEvent{
		ExternalSessionID: "runtime-bind-failure",
		EventName:         "PermissionRequest",
		TurnID:            "turn",
		CWDBasename:       "dora",
		Surface:           domain.CodexSurfaceApp,
		ToolName:          "apply_patch",
		EventKey:          "codex:runtime-bind-failure",
		ReceivedAt:        time.Now().UTC().Add(-time.Minute),
	}
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatalf("保存历史等待请求失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口失败: %v", err)
	}
	defer occupied.Close()
	_, err = Start(ctx, Config{
		Address:      occupied.Addr().String(),
		DBPath:       dbPath,
		CodexHomes:   []string{filepath.Join(t.TempDir(), "codex")},
		ScanInterval: time.Hour,
		Logger:       log.New(io.Discard, "", 0),
	})
	if err == nil {
		t.Fatal("Start() 在端口冲突时意外成功")
	}

	store, err = dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("重新打开 SQLite 失败: %v", err)
	}
	defer store.Close()
	requests, err := store.UnnotifiedAttention(ctx)
	if err != nil {
		t.Fatalf("读取未提醒请求失败: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("端口冲突修改了提醒状态: %+v", requests)
	}
}

func TestBackgroundFailureLogsIncludeActionableCause(t *testing.T) {
	tests := []struct {
		name   string
		log    func(context.Context, Logger, error)
		cause  error
		values []string
	}{
		{
			name: "usage scan",
			log: func(ctx context.Context, logger Logger, err error) {
				logUsageScanResult(ctx, logger, scan.Report{}, err)
			},
			cause:  errors.New("fixture parser failed\nsecond detail"),
			values: []string{"Codex 用量自动扫描失败", "fixture parser failed second detail", "可运行 dora scan 重试并查看终端输出"},
		},
		{
			name: "quota refresh",
			log: func(ctx context.Context, logger Logger, err error) {
				logQuotaRefreshResult(ctx, logger, quota.View{}, err)
			},
			cause:  errors.New("fixture quota transport failed"),
			values: []string{"Codex 配额自动刷新失败", "fixture quota transport failed", "本地 token 统计不受影响"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			test.log(context.Background(), log.New(&output, "", 0), test.cause)
			for _, value := range test.values {
				if !strings.Contains(output.String(), value) {
					t.Fatalf("后台失败日志缺少 %q: %q", value, output.String())
				}
			}
			if strings.Count(strings.TrimSpace(output.String()), "\n") != 0 {
				t.Fatalf("后台失败日志不是单行: %q", output.String())
			}
		})
	}
}

func TestBackgroundCancellationDoesNotLogFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, cancellation := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{name: "canceled context", ctx: ctx, err: errors.New("fixture shutdown race")},
		{name: "wrapped cancellation", ctx: context.Background(), err: fmt.Errorf("后台退出: %w", context.Canceled)},
	} {
		t.Run(cancellation.name, func(t *testing.T) {
			for _, logResult := range []func(context.Context, Logger, error){
				func(ctx context.Context, logger Logger, err error) {
					logUsageScanResult(ctx, logger, scan.Report{}, err)
				},
				func(ctx context.Context, logger Logger, err error) {
					logQuotaRefreshResult(ctx, logger, quota.View{}, err)
				},
			} {
				var output bytes.Buffer
				logResult(cancellation.ctx, log.New(&output, "", 0), cancellation.err)
				if output.Len() != 0 {
					t.Fatalf("context cancellation 产生失败日志: %q", output.String())
				}
			}
		})
	}
}

func TestBackgroundSuccessLogsSurviveLateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		log  func(Logger)
		want string
	}{
		{
			name: "usage scan",
			log: func(logger Logger) {
				logUsageScanResult(ctx, logger, scan.Report{Mode: "incremental", FilesSeen: 1}, nil)
			},
			want: "Codex 用量扫描完成",
		},
		{
			name: "multi-provider usage scan",
			log: func(logger Logger) {
				logUsageScanResult(ctx, logger, scan.Report{Providers: []scan.ProviderReport{
					{Source: "provider.codex", Mode: "incremental"},
					{Source: "provider.claude-code", Mode: "incremental"},
				}}, nil)
			},
			want: "Claude Code 用量扫描完成",
		},
		{
			name: "quota refresh",
			log: func(logger Logger) {
				logQuotaRefreshResult(ctx, logger, quota.View{Enabled: true, Status: "ready"}, nil)
			},
			want: "Codex 配额刷新完成",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			test.log(log.New(&output, "", 0))
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("成功日志在延迟取消后丢失: %q", output.String())
			}
		})
	}
}

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

func TestRuntimeChecksAndStopsLogRotation(t *testing.T) {
	rotator := &testLogRotator{running: make(chan struct{}), stopped: make(chan struct{})}
	runtime, err := Start(context.Background(), Config{
		Address:      "127.0.0.1:0",
		DBPath:       filepath.Join(t.TempDir(), "dora.db"),
		CodexHomes:   []string{t.TempDir()},
		ScanInterval: time.Hour,
		LogRotator:   rotator,
	})
	if err != nil {
		t.Fatalf("Start() 失败: %v", err)
	}
	if rotator.checks != 1 {
		t.Fatalf("启动阶段轮转检查次数 = %d", rotator.checks)
	}
	select {
	case <-rotator.running:
	case <-time.After(time.Second):
		t.Fatal("轮转周期任务未启动")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}
	select {
	case <-rotator.stopped:
	case <-time.After(time.Second):
		t.Fatal("轮转周期任务未随 runtime 退出")
	}
}

type testLogRotator struct {
	checks  int
	running chan struct{}
	stopped chan struct{}
}

func (r *testLogRotator) Check() {
	r.checks++
}

func (r *testLogRotator) Run(ctx context.Context) {
	close(r.running)
	<-ctx.Done()
	close(r.stopped)
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
