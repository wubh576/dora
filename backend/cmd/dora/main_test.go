package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/app"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/launchagent"
	"github.com/wubh576/dora/backend/internal/menubar"
	"github.com/wubh576/dora/backend/internal/settings"
)

func TestVersionCommand(t *testing.T) {
	info := buildinfo.New("v1.2.3", "abc123", "2026-07-31T08:00:00Z", "go1.26.5", "darwin", "arm64", "15.6")
	var output bytes.Buffer
	if err := versionCommand(nil, &output, info); err != nil {
		t.Fatalf("versionCommand() 失败: %v", err)
	}
	for _, value := range []string{"Dora v1.2.3", "Git commit: abc123", "Go version: go1.26.5", "Platform: darwin/arm64", "macOS version: 15.6"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("version 输出缺少 %q: %q", value, output.String())
		}
	}
	if err := versionCommand([]string{"extra"}, &output, info); err == nil {
		t.Fatal("version 接受了多余参数")
	}
}

func TestStatusUsesRunningLaunchAgentBuildInfo(t *testing.T) {
	commandInfo := buildinfo.New("dev+new", "new", "new-time", "go1.26.5", "darwin", "arm64", "15.6")
	runningInfo := buildinfo.New("v1.2.3", "running", "running-time", "go1.26.5", "darwin", "arm64", "15.6")
	status := launchagent.Status{Loaded: true, Running: true, Healthy: true, DashboardURL: launchagent.DashboardURL, BuildInfo: &runningInfo}
	var output bytes.Buffer
	if err := writeLaunchAgentStatus(&output, status, "是", "已加载", commandInfo); err != nil {
		t.Fatalf("writeLaunchAgentStatus() 失败: %v", err)
	}
	if !strings.Contains(output.String(), "版本来源：运行中的 LaunchAgent") || !strings.Contains(output.String(), "Dora 版本：v1.2.3") || strings.Contains(output.String(), "dev+new") {
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

func assertAddressReleased(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("菜单 runtime 未释放 %s: %v", address, err)
	}
	_ = listener.Close()
}

func TestRunManualScanWithConfiguredHome(t *testing.T) {
	if err := run([]string{"scan", "--db", filepath.Join(t.TempDir(), "dora.db"), "--codex-home", t.TempDir()}); err != nil {
		t.Fatalf("run(scan) 失败: %v", err)
	}
}

func TestRunManualScanReportsInvalidFilePath(t *testing.T) {
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "broken.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatalf("写入无效 session 失败: %v", err)
	}
	err := run([]string{"scan", "--db", filepath.Join(t.TempDir(), "dora.db"), "--codex-home", home})
	if err == nil || !strings.Contains(err.Error(), sessionPath) {
		t.Fatalf("scan 错误未包含目标文件路径: %v", err)
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
