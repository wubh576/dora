package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/app"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/codexhooks"
	"github.com/wubh576/dora/backend/internal/launchagent"
	"github.com/wubh576/dora/backend/internal/menubar"
	"github.com/wubh576/dora/backend/internal/settings"
)

func TestWriteCodexHooksStatus(t *testing.T) {
	var output bytes.Buffer
	status := codexhooks.Status{
		Path:       "/Users/test/.codex/hooks.json",
		Executable: "/Applications/Dora/dora",
		Installed:  true,
		Trust:      "untrusted",
	}
	if err := writeCodexHooksStatus(&output, status); err != nil {
		t.Fatalf("writeCodexHooksStatus() 失败: %v", err)
	}
	for _, value := range []string{"已安装", status.Path, status.Executable, "等待 Codex 授权", "/hooks"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("hooks 状态缺少 %q: %s", value, output.String())
		}
	}
}

func TestWriteRealtimeReminderStatus(t *testing.T) {
	tests := []struct {
		name   string
		status codexhooks.Status
		err    error
		want   string
	}{
		{name: "enabled", status: codexhooks.Status{Installed: true, Trust: "trusted"}, want: "实时提醒：已启用"},
		{name: "pending trust", status: codexhooks.Status{Installed: true, Trust: "untrusted"}, want: "实时提醒：待授权"},
		{name: "missing", status: codexhooks.Status{}, want: "dora install 自动修复"},
		{name: "broken", status: codexhooks.Status{Broken: "invalid"}, want: "Hook 配置损坏"},
		{name: "invalid executable", status: codexhooks.Status{Installed: true, ExecutableProblem: "missing"}, want: "handler 路径失效"},
		{name: "status error", err: errors.New("unavailable"), want: "实时提醒：未启用"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeRealtimeReminderStatus(&output, test.status, test.err); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("实时提醒状态缺少 %q: %s", test.want, output.String())
			}
		})
	}
}

func TestLaunchAgentHookOnboardingUsesStablePath(t *testing.T) {
	home := t.TempDir()
	executable := launchagent.PathsForHome(home).Binary
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := installLaunchAgentCodexHooks(home, executable)
	if err != nil || !status.Installed || status.Executable != executable || status.ExecutableProblem != "" {
		t.Fatalf("安装 LaunchAgent hooks = %+v, %v", status, err)
	}
	first, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), executable) {
		t.Fatalf("Hook 未使用稳定安装路径: %s", first)
	}
	if _, err := installLaunchAgentCodexHooks(home, executable); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("重复安装改变了 Hook 配置: %v", err)
	}
	if _, err := uninstallLaunchAgentCodexHooks(home, executable); err != nil {
		t.Fatal(err)
	}
	status, err = launchAgentCodexHooksStatus(home, executable)
	if err != nil || status.Installed {
		t.Fatalf("卸载后 Hook 仍安装: %+v, %v", status, err)
	}
}

func TestUninstallComponentsAttemptsBothAndReturnsPartialFailure(t *testing.T) {
	tests := []struct {
		name           string
		launchAgentErr error
		hookErr        error
		wantOutput     []string
		wantError      []string
	}{
		{
			name: "success", wantOutput: []string{"LaunchAgent：已卸载", "hooks：已移除"},
		},
		{
			name: "hook cleanup fails", hookErr: errors.New("broken hooks"),
			wantOutput: []string{"LaunchAgent：已卸载", "hooks：未能自动移除"},
			wantError:  []string{"broken hooks"},
		},
		{
			name: "launchagent cleanup fails", launchAgentErr: errors.New("launchctl failed"),
			wantOutput: []string{"LaunchAgent：未能卸载", "hooks：已移除"},
			wantError:  []string{"launchctl failed"},
		},
		{
			name: "both fail", launchAgentErr: errors.New("launchctl failed"), hookErr: errors.New("broken hooks"),
			wantOutput: []string{"LaunchAgent：未能卸载", "hooks：未能自动移除"},
			wantError:  []string{"launchctl failed", "broken hooks"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			launchAgentCalls, hookCalls := 0, 0
			err := uninstallComponents(
				context.Background(),
				&output,
				func(context.Context) error { launchAgentCalls++; return test.launchAgentErr },
				func() error { hookCalls++; return test.hookErr },
			)
			if launchAgentCalls != 1 || hookCalls != 1 {
				t.Fatalf("卸载未尝试两个组件: launchagent=%d hooks=%d", launchAgentCalls, hookCalls)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("卸载输出缺少 %q: %s", want, output.String())
				}
			}
			if len(test.wantError) == 0 && err != nil {
				t.Fatalf("卸载意外失败: %v", err)
			}
			for _, want := range test.wantError {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("卸载错误缺少 %q: %v", want, err)
				}
			}
		})
	}
}

type unavailableHookEmitter struct{}

func (unavailableHookEmitter) Emit(context.Context, io.Reader) error {
	return codexhooks.ErrServiceUnavailable
}

func TestEmitCodexHookSilentlyAcceptsUnavailableService(t *testing.T) {
	if err := emitCodexHook(context.Background(), strings.NewReader("private input"), unavailableHookEmitter{}); err != nil {
		t.Fatalf("服务不可用没有静默成功: %v", err)
	}
}

func TestStatusUsesRunningLaunchAgentBuildInfo(t *testing.T) {
	commandInfo := buildinfo.New("new", true, "new-time", "go1.26.5", "darwin", "arm64", "15.6")
	runningInfo := buildinfo.New("0123456789abcdef", false, "running-time", "go1.26.5", "darwin", "arm64", "15.6")
	status := launchagent.Status{Loaded: true, Running: true, Healthy: true, DashboardURL: launchagent.DashboardURL, BuildInfo: &runningInfo}
	var output bytes.Buffer
	if err := writeLaunchAgentStatus(&output, status, "是", "已加载", commandInfo); err != nil {
		t.Fatalf("writeLaunchAgentStatus() 失败: %v", err)
	}
	if !strings.Contains(output.String(), "构建来源：运行中的 LaunchAgent") || !strings.Contains(output.String(), "构建标识：0123456789ab") || !strings.Contains(output.String(), "构建状态：clean") || strings.Contains(output.String(), "new-dirty") {
		t.Fatalf("status 未展示运行中的 build info: %q", output.String())
	}
}

func TestParseMenubarOptions(t *testing.T) {
	defaultDB := filepath.Join(t.TempDir(), "default.db")
	firstHome, secondHome := t.TempDir(), t.TempDir()
	options, err := parseApplicationOptions("menubar", []string{"--addr", "127.0.0.1:9090", "--db", filepath.Join(t.TempDir(), "custom.db"), "--codex-home", firstHome, "--codex-home", secondHome}, defaultDB)
	if err != nil {
		t.Fatalf("parseApplicationOptions() 失败: %v", err)
	}
	if options.address != "127.0.0.1:9090" || options.dbPath == defaultDB || !reflect.DeepEqual(options.codexHomes, []string{firstHome, secondHome}) {
		t.Fatalf("menubar 参数解析错误: %+v", options)
	}
}

func TestParseMenubarLaunchAgentLogRotation(t *testing.T) {
	defaults, err := parseApplicationOptions("menubar", []string{"--launchagent"}, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("解析默认 LaunchAgent 轮转参数失败: %v", err)
	}
	if defaults.logRotationBytes != launchagent.DefaultLogMaxBytes || defaults.logRotationInterval != launchagent.DefaultLogCheckInterval {
		t.Fatalf("LaunchAgent 未使用生产轮转默认值: %+v", defaults)
	}

	options, err := parseApplicationOptions("menubar", []string{"--launchagent", "--log-rotation-bytes", "128", "--log-rotation-interval", "10ms"}, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("解析 LaunchAgent 轮转参数失败: %v", err)
	}
	if !options.launchAgent || options.logRotationBytes != 128 || options.logRotationInterval != 10*time.Millisecond {
		t.Fatalf("LaunchAgent 轮转参数错误: %+v", options)
	}
	if _, err := parseApplicationOptions("menubar", []string{"--log-rotation-bytes", "128"}, filepath.Join(t.TempDir(), "dora.db")); err == nil || !strings.Contains(err.Error(), "--launchagent") {
		t.Fatalf("未拒绝脱离 LaunchAgent 的轮转参数: %v", err)
	}
	if _, err := parseApplicationOptions("serve", []string{"--launchagent"}, filepath.Join(t.TempDir(), "dora.db")); err == nil {
		t.Fatal("serve 接受了 LaunchAgent 日志轮转参数")
	}
}

func TestOnlyLaunchAgentApplicationRotatesUserLogPaths(t *testing.T) {
	home := t.TempDir()
	paths := launchagent.PathsForHome(home)
	for _, directory := range []string{paths.Logs, filepath.Dir(paths.Binary)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.Binary, []byte("fixture binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.StdoutLog, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.StderrLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	manual, err := startApplication(context.Background(), applicationOptions{
		address:    "127.0.0.1:0",
		dbPath:     filepath.Join(t.TempDir(), "manual.db"),
		codexHomes: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("启动手动 runtime 失败: %v", err)
	}
	if err := manual.Close(); err != nil {
		t.Fatalf("关闭手动 runtime 失败: %v", err)
	}
	if _, err := os.Stat(paths.StdoutLog + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("手动 runtime 操作了 LaunchAgent 日志: %v", err)
	}

	stdout, err := os.OpenFile(paths.StdoutLog, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(paths.StderrLog, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	rotationOptions := applicationOptions{
		launchAgent:         true,
		logRotationBytes:    6,
		logRotationInterval: time.Hour,
	}
	rotator, err := launchAgentLogRotator(rotationOptions, launchAgentProcess{
		serviceName: launchagent.Label,
		home:        home,
		executable:  paths.Binary,
		stdout:      stdout,
		stderr:      stderr,
	})
	if err != nil {
		t.Fatalf("创建 LaunchAgent 轮转任务失败: %v", err)
	}
	managed, err := app.Start(context.Background(), app.Config{
		Address:      "127.0.0.1:0",
		DBPath:       filepath.Join(t.TempDir(), "managed.db"),
		CodexHomes:   []string{t.TempDir()},
		ScanInterval: time.Hour,
		LogRotator:   rotator,
	})
	if err != nil {
		t.Fatalf("启动 LaunchAgent runtime 失败: %v", err)
	}
	if err := managed.Close(); err != nil {
		t.Fatalf("关闭 LaunchAgent runtime 失败: %v", err)
	}
	backup, err := os.ReadFile(paths.StdoutLog + ".1")
	if err != nil || string(backup) != "manual" {
		t.Fatalf("LaunchAgent 启动轮转错误: backup=%q err=%v", backup, err)
	}
	active, err := os.ReadFile(paths.StdoutLog)
	if err != nil || len(active) != 0 {
		t.Fatalf("LaunchAgent 活动日志未清空: active=%q err=%v", active, err)
	}
}

func TestLaunchAgentLogRotationRejectsUnmanagedProcess(t *testing.T) {
	home := t.TempDir()
	paths := launchagent.PathsForHome(home)
	if err := os.MkdirAll(paths.Logs, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.StdoutLog, paths.StderrLog} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, err := os.OpenFile(paths.StdoutLog, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(paths.StderrLog, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	options := applicationOptions{launchAgent: true, logRotationBytes: 1, logRotationInterval: time.Second}

	tests := []struct {
		name    string
		process launchAgentProcess
		want    string
	}{
		{
			name:    "not launchd service",
			process: launchAgentProcess{serviceName: "terminal", home: home, executable: paths.Binary, stdout: stdout, stderr: stderr},
			want:    launchagent.Label,
		},
		{
			name:    "wrong executable",
			process: launchAgentProcess{serviceName: launchagent.Label, home: home, executable: filepath.Join(home, "manual-dora"), stdout: stdout, stderr: stderr},
			want:    "可执行文件路径不匹配",
		},
		{
			name:    "wrong stdout",
			process: launchAgentProcess{serviceName: launchagent.Label, home: home, executable: paths.Binary, stdout: os.Stderr, stderr: stderr},
			want:    "stdout 未指向",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := launchAgentLogRotator(options, test.process); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("未拒绝非受管进程: %v", err)
			}
		})
	}

	t.Setenv(launchAgentServiceEnvironment, "")
	if _, err := startApplication(context.Background(), applicationOptions{
		address:             "127.0.0.1:0",
		dbPath:              filepath.Join(t.TempDir(), "dora.db"),
		codexHomes:          []string{t.TempDir()},
		launchAgent:         true,
		logRotationBytes:    1,
		logRotationInterval: time.Second,
	}); err == nil || !strings.Contains(err.Error(), launchagent.Label) {
		t.Fatalf("手动 --launchagent 未被拒绝: %v", err)
	}
}

func TestCommandExitCodes(t *testing.T) {
	if got := commandExitCode(errors.New("regular")); got != 1 {
		t.Fatalf("普通错误 exit code = %d", got)
	}
	if got := commandExitCode(&commandExitError{code: 2, err: errors.New("status")}); got != 2 {
		t.Fatalf("状态检查错误 exit code = %d", got)
	}
}

func TestServeAndMenubarShareApplicationDefaults(t *testing.T) {
	defaultDB := filepath.Join(t.TempDir(), "dora.db")
	serveOptions, err := parseApplicationOptions("serve", nil, defaultDB)
	if err != nil {
		t.Fatalf("解析 serve 默认参数失败: %v", err)
	}
	menuOptions, err := parseApplicationOptions("menubar", nil, defaultDB)
	if err != nil {
		t.Fatalf("解析 menubar 默认参数失败: %v", err)
	}
	if !reflect.DeepEqual(serveOptions, menuOptions) {
		t.Fatalf("serve 与 menubar 运行参数不一致: serve=%+v menu=%+v", serveOptions, menuOptions)
	}
}

func TestMenubarPortConflictFailsBeforeUI(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口失败: %v", err)
	}
	defer listener.Close()
	err = run([]string{"menubar", "--addr", listener.Addr().String(), "--db", filepath.Join(t.TempDir(), "dora.db"), "--codex-home", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "监听 HTTP 地址") {
		t.Fatalf("menubar 端口冲突错误 = %v", err)
	}
}

func TestMenubarSignalAndQuitUseGracefulRuntimeClose(t *testing.T) {
	for _, test := range []struct {
		name        string
		requestQuit bool
	}{
		{name: "signal"},
		{name: "menu quit", requestQuit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			application, err := app.Start(ctx, app.Config{
				Address:      "127.0.0.1:0",
				DBPath:       filepath.Join(t.TempDir(), "dora.db"),
				CodexHomes:   []string{t.TempDir()},
				ScanInterval: time.Hour,
			})
			if err != nil {
				t.Fatalf("启动测试 runtime 失败: %v", err)
			}
			address := application.Address()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runMenubarApplication(ctx, stop, application, func(ctx context.Context, config menubar.Config) error {
					close(started)
					if test.requestQuit {
						config.Quit()
					}
					<-ctx.Done()
					return nil
				})
			}()
			<-started
			if !test.requestQuit {
				stop()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("退出菜单 runtime 失败: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("等待菜单 runtime 退出超时")
			}
			assertAddressReleased(t, address)
		})
	}
}

func TestMenubarRunnerErrorStillClosesRuntime(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	application, err := app.Start(ctx, app.Config{
		Address:      "127.0.0.1:0",
		DBPath:       filepath.Join(t.TempDir(), "dora.db"),
		CodexHomes:   []string{t.TempDir()},
		ScanInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("启动测试 runtime 失败: %v", err)
	}
	address := application.Address()
	menuFailure := errors.New("menu loop failed")
	done := make(chan error, 1)
	go func() {
		done <- runMenubarApplication(ctx, stop, application, func(context.Context, menubar.Config) error {
			return menuFailure
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, menuFailure) {
			t.Fatalf("菜单错误未保留: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("菜单 runner 异常返回后 Runtime.Close 卡住")
	}
	assertAddressReleased(t, address)
}

func assertAddressReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("菜单 runtime 未释放 %s: %v", address, err)
	}
	_ = listener.Close()
}

func TestRunManualScanWithConfiguredHome(t *testing.T) {
	if err := run([]string{
		"scan", "--db", filepath.Join(t.TempDir(), "dora.db"),
		"--codex-home", t.TempDir(), "--claude-home", t.TempDir(),
	}); err != nil {
		t.Fatalf("run(scan) 失败: %v", err)
	}
}

func TestRunManualScanRedactsInvalidFilePath(t *testing.T) {
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "broken.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatalf("写入无效 session 失败: %v", err)
	}
	err := run([]string{
		"scan", "--db", filepath.Join(t.TempDir(), "dora.db"),
		"--codex-home", home, "--claude-home", t.TempDir(),
	})
	if err == nil || strings.Contains(err.Error(), sessionPath) || !strings.Contains(err.Error(), "不能为负数") {
		t.Fatalf("scan 错误未脱敏或缺少原因: %v", err)
	}
}

func TestQuotaCommandHonorsDisabledConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dora.db")
	if err := settings.New(filepath.Join(filepath.Dir(path), "settings.json")).Save(settings.Values{CodexQuotaConsent: false}); err != nil {
		t.Fatalf("保存关闭 consent 失败: %v", err)
	}
	err := run([]string{"quota", "refresh", "--db", path, "--codex-home", t.TempDir()})
	if err == nil || err.Error() != "Codex 订阅配额尚未授权，请先在 Dora Diagnostics 中启用" {
		t.Fatalf("未授权 quota refresh 错误 = %v", err)
	}
}
