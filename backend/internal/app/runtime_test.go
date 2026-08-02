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
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/jump"
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
		ReceivedAt:        time.Now().UTC().Add(-8 * 24 * time.Hour),
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
	if state, err := store.RuntimeSessionState(ctx, "runtime-bind-failure"); err != nil || state != domain.RuntimeStateWaiting {
		t.Fatalf("端口冲突清理了 runtime session: state=%q, err=%v", state, err)
	}
}

func TestRuntimeReconcilesSevenDayStaleAttention(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dora.db")
	store, err := dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, fixture := range []struct {
		sessionID string
		at        time.Time
		key       string
	}{
		{sessionID: "stale-runtime", at: now.Add(-8 * 24 * time.Hour), key: "codex:stale-runtime"},
		{sessionID: "recent-runtime", at: now.Add(-time.Hour), key: "codex:recent-runtime"},
	} {
		event := domain.CodexHookEvent{
			ExternalSessionID: fixture.sessionID, EventName: "PermissionRequest", TurnID: "turn",
			CWDBasename: "dora", Surface: domain.CodexSurfaceApp, ToolName: "Bash",
			EventKey: fixture.key, ReceivedAt: fixture.at,
		}
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := Start(ctx, Config{
		Address: "127.0.0.1:0", DBPath: dbPath,
		CodexHomes: []string{filepath.Join(t.TempDir(), "codex")}, ScanInterval: time.Hour,
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	waiting, err := runtime.store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 1 || waiting[0].Session.ExternalSessionID != "recent-runtime" {
		t.Fatalf("启动 stale reconciliation 结果 = %+v, %v", waiting, err)
	}
}

type recordingAttentionStore struct {
	requests []domain.AttentionRequest
	marked   map[int64]bool
}

func (store *recordingAttentionStore) ClaimUnnotifiedAttention(_ context.Context, _ time.Time) ([]domain.AttentionRequest, error) {
	result := make([]domain.AttentionRequest, 0, len(store.requests))
	for _, request := range store.requests {
		if !store.marked[request.ID] {
			result = append(result, request)
			store.marked[request.ID] = true
		}
	}
	return result, nil
}

type recordingNotifier struct{ ids []int64 }

func (notifier *recordingNotifier) Notify(_ context.Context, request domain.AttentionRequest) error {
	notifier.ids = append(notifier.ids, request.ID)
	return nil
}

type failingNotifier struct {
	ids    []int64
	failID int64
}

func (notifier *failingNotifier) Notify(_ context.Context, request domain.AttentionRequest) error {
	notifier.ids = append(notifier.ids, request.ID)
	if request.ID == notifier.failID {
		return errors.New("sound unavailable")
	}
	return nil
}

func TestNotifyAttentionOnceSoundsOncePerNewRequest(t *testing.T) {
	store := &recordingAttentionStore{
		requests: []domain.AttentionRequest{{ID: 1}, {ID: 2}},
		marked:   make(map[int64]bool),
	}
	notifier := &recordingNotifier{}
	if _, err := notifyAttentionOnce(context.Background(), store, notifier, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := notifyAttentionOnce(context.Background(), store, notifier, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(notifier.ids, []int64{1, 2}) {
		t.Fatalf("提醒次数错误: %+v", notifier.ids)
	}
}

func TestNotifyFailureDoesNotReplayClaimedSound(t *testing.T) {
	store := &recordingAttentionStore{
		requests: []domain.AttentionRequest{{ID: 1}, {ID: 2}},
		marked:   make(map[int64]bool),
	}
	notifier := &failingNotifier{failID: 1}
	if attempted, err := notifyAttentionOnce(context.Background(), store, notifier, time.Now().UTC()); err == nil || attempted != 2 {
		t.Fatal("首次声音失败未返回错误")
	}
	if attempted, err := notifyAttentionOnce(context.Background(), store, notifier, time.Now().UTC()); err != nil || attempted != 0 {
		t.Fatalf("第二次 claim 失败: %v", err)
	}
	if !reflect.DeepEqual(notifier.ids, []int64{1, 2}) {
		t.Fatalf("失败后未继续尝试同批提醒，或已 claim 请求被重放: %+v", notifier.ids)
	}
}

type configurableJumpRunner struct {
	output []byte
	err    error
}

func (runner configurableJumpRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return runner.output, runner.err
}

func TestJumpAttentionSessionOnlyResolvesGoneTarget(t *testing.T) {
	for _, test := range []struct {
		name         string
		runnerOutput []byte
		runnerError  error
		wantWaiting  int
	}{
		{name: "successful click keeps waiting", wantWaiting: 1},
		{name: "gone terminal reconciles", runnerOutput: []byte("DORA_TARGET_GONE\n"), wantWaiting: 0},
		{name: "execution error keeps waiting", runnerError: errors.New("permission denied"), wantWaiting: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			event := domain.CodexHookEvent{
				ExternalSessionID: "jump-session", EventName: "PermissionRequest", TurnID: "turn",
				CWDBasename: "dora", Surface: domain.CodexSurfaceCLI, TerminalKind: domain.TerminalTerminal,
				TTY: "/dev/ttys009", ToolName: "Bash", EventKey: "codex:jump", ReceivedAt: time.Now().UTC(),
			}
			if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			runtime := &Runtime{
				store: store, jump: jump.New(configurableJumpRunner{output: test.runnerOutput, err: test.runnerError}),
				logger: log.New(&logs, "", 0),
			}
			err = runtime.JumpAttentionSession(ctx, 1)
			if test.runnerError == nil && len(test.runnerOutput) == 0 && err != nil {
				t.Fatalf("跳转成功意外报错: %v", err)
			}
			if len(test.runnerOutput) > 0 && !errors.Is(err, jump.ErrTargetGone) {
				t.Fatalf("目标消失错误 = %v", err)
			}
			if test.runnerError != nil && (err == nil || errors.Is(err, jump.ErrTargetGone)) {
				t.Fatalf("执行错误被误判为目标消失: %v", err)
			}
			waiting, loadErr := store.WaitingSessions(ctx)
			if loadErr != nil || len(waiting) != test.wantWaiting {
				t.Fatalf("点击后的等待状态 = %+v, %v", waiting, loadErr)
			}
			if strings.Contains(logs.String(), "jump-session") || !strings.Contains(logs.String(), "session=") {
				t.Fatalf("回跳日志泄露原始 session 或缺少安全标识: %s", logs.String())
			}
			if test.runnerError == nil && len(test.runnerOutput) == 0 && !strings.Contains(logs.String(), "result=success") {
				t.Fatalf("成功回跳日志缺少结果: %s", logs.String())
			}
		})
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
