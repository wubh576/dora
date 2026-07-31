package launchagent

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPathsForHomeUsesStableUserLocations(t *testing.T) {
	paths := PathsForHome("/Users/tester")
	if paths.Binary != "/Users/tester/Library/Application Support/Dora/bin/dora" {
		t.Fatalf("Binary = %q", paths.Binary)
	}
	if paths.Plist != "/Users/tester/Library/LaunchAgents/io.github.wubh576.dora.plist" {
		t.Fatalf("Plist = %q", paths.Plist)
	}
	if paths.StdoutLog != "/Users/tester/Library/Logs/Dora/dora.stdout.log" || paths.StderrLog != "/Users/tester/Library/Logs/Dora/dora.stderr.log" {
		t.Fatalf("日志路径错误: %+v", paths)
	}
}

func TestPlistIsValidAndContainsRequiredLifecycle(t *testing.T) {
	paths := PathsForHome("/Users/A&B")
	data, err := buildPlist(paths)
	if err != nil {
		t.Fatalf("buildPlist() 失败: %v", err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		if _, err := decoder.Token(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("plist 不是合法 XML: %v", err)
		}
	}
	content := string(data)
	for _, expected := range []string{
		"<string>io.github.wubh576.dora</string>",
		"<string>/Users/A&amp;B/Library/Application Support/Dora/bin/dora</string>",
		"<string>menubar</string>",
		"<key>RunAtLoad</key>\n  <true/>",
		"<key>SuccessfulExit</key>\n    <false/>",
		"<key>ThrottleInterval</key>\n  <integer>10</integer>",
		"dora.stdout.log",
		"dora.stderr.log",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("plist 缺少 %q:\n%s", expected, content)
		}
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "OPENAI_API_KEY", "OAuth"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("plist 包含凭证字段 %q", forbidden)
		}
	}
}

func TestInstallRejectsDevelopmentBinary(t *testing.T) {
	manager, _, _, _ := newTestManager(t, false)
	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "make build") || !strings.Contains(err.Error(), "./bin/dora install") {
		t.Fatalf("开发构建安装错误 = %v", err)
	}
}

func TestInstallCopiesAtomicallyAndReloadsIdempotently(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.loaded = true
	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("首次 Install() 失败: %v", err)
	}
	assertInstalledFiles(t, manager)
	firstCalls := runner.callArgs()
	if !reflect.DeepEqual(firstCalls, [][]string{
		{"print", "gui/501/" + Label},
		{"bootout", "gui/501/" + Label},
		{"print", "gui/501/" + Label},
		{"bootstrap", "gui/501", manager.Paths().Plist},
		{"kickstart", "-k", "gui/501/" + Label},
		{"print", "gui/501/" + Label},
	}) {
		t.Fatalf("首次 install launchctl 顺序错误: %+v", firstCalls)
	}
	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("重复 Install() 失败: %v", err)
	}
	assertInstalledFiles(t, manager)
	if countCalls(runner.callArgs(), "bootstrap") != 2 || countCalls(runner.callArgs(), "kickstart") != 2 {
		t.Fatalf("重复 install 未保持单个可重载 LaunchAgent: %+v", runner.callArgs())
	}
}

func TestInstallWaitsUntilBootoutIsFullyRemoved(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.loaded, runner.running, runner.unloadDelay = true, true, 2
	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() 失败: %v", err)
	}
	calls := runner.callArgs()
	bootstrapIndex, printCount := -1, 0
	for index, call := range calls {
		if call[0] == "print" {
			printCount++
		}
		if call[0] == "bootstrap" {
			bootstrapIndex = index
		}
	}
	if printCount != 5 || bootstrapIndex != 5 {
		t.Fatalf("bootstrap 未等待 launchd 完全卸载: %+v", calls)
	}
}

func TestInstallReportsLaunchctlFailureWithLabel(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.failVerb = "bootstrap"
	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), Label) || !strings.Contains(err.Error(), "launchctl bootstrap") || !strings.Contains(err.Error(), "dora status") || !strings.Contains(err.Error(), "dora install") {
		t.Fatalf("launchctl 失败错误不清楚: %v", err)
	}
}

func TestInstallReportsHealthFailure(t *testing.T) {
	manager, _, health, _ := newTestManager(t, true)
	health.err = errors.New("health offline")
	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "健康检查失败") || !strings.Contains(err.Error(), "dora.stderr.log") {
		t.Fatalf("health 失败错误不清楚: %v", err)
	}
}

func TestInstallRejectsOccupiedDashboardPort(t *testing.T) {
	manager, runner, _, port := newTestManager(t, true)
	port.err = errors.New("address already in use")
	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:8080 已被占用") {
		t.Fatalf("端口冲突错误 = %v", err)
	}
	if countCalls(runner.callArgs(), "bootstrap") != 0 {
		t.Fatalf("端口冲突后仍加载 LaunchAgent: %+v", runner.callArgs())
	}
}

func TestInstallDoesNotTrustUnrelatedHealthyEndpoint(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.stopAfterKickstart = true
	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "菜单栏进程未保持运行") {
		t.Fatalf("未识别 health 与 launchd job 脱节: %v", err)
	}
}

func TestStatusDistinguishesInstallLoadRunAndHealth(t *testing.T) {
	tests := []struct {
		name      string
		files     bool
		loaded    bool
		running   bool
		healthErr error
		installed bool
		runState  string
		exitCode  int
	}{
		{name: "not installed", runState: "未运行", exitCode: 1},
		{name: "installed not loaded", files: true, installed: true, runState: "未运行", exitCode: 1},
		{name: "loaded but stopped", files: true, loaded: true, installed: true, runState: "未运行", exitCode: 1},
		{name: "running unhealthy", files: true, loaded: true, running: true, healthErr: errors.New("offline"), installed: true, runState: "异常", exitCode: 1},
		{name: "healthy", files: true, loaded: true, running: true, installed: true, runState: "正常", exitCode: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, runner, health, _ := newTestManager(t, true)
			runner.loaded, runner.running = test.loaded, test.running
			health.err = test.healthErr
			if test.files {
				writeInstalledFiles(t, manager.Paths())
			}
			status, err := manager.Status(context.Background())
			if err != nil {
				t.Fatalf("Status() 失败: %v", err)
			}
			if status.Installed() != test.installed || status.Loaded != test.loaded || status.Running != test.running || status.RunState() != test.runState || status.ExitCode() != test.exitCode {
				t.Fatalf("Status() = %+v state=%s exit=%d", status, status.RunState(), status.ExitCode())
			}
		})
	}
}

func TestStatusReportsInspectionFailure(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.failVerb = "print"
	if _, err := manager.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "检查 LaunchAgent") {
		t.Fatalf("Status() 检查失败错误 = %v", err)
	}
}

func TestStatusRechecksLaunchdAfterHealthyResponse(t *testing.T) {
	manager, runner, health, _ := newTestManager(t, true)
	writeInstalledFiles(t, manager.Paths())
	runner.loaded, runner.running = true, true
	health.after = func() {
		runner.mu.Lock()
		runner.running = false
		runner.mu.Unlock()
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() 失败: %v", err)
	}
	if status.Healthy || status.Running || status.RunState() != "未运行" {
		t.Fatalf("health 后未复查 launchd: %+v", status)
	}
}

func TestUninstallIsIdempotentAndPreservesUserData(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	paths := manager.Paths()
	writeInstalledFiles(t, paths)
	if err := os.WriteFile(paths.BinaryTemp, []byte("temporary binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.PlistTemp, []byte("temporary plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Database, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Settings, []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.loaded, runner.running = true, true
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("首次 Uninstall() 失败: %v", err)
	}
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("重复 Uninstall() 失败: %v", err)
	}
	for _, removed := range []string{paths.Binary, paths.Plist, paths.BinaryTemp, paths.PlistTemp} {
		if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("安装文件仍存在 %s: %v", removed, err)
		}
	}
	for _, preserved := range []string{paths.Database, paths.Settings} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("用户数据未保留 %s: %v", preserved, err)
		}
	}
	if countCalls(runner.callArgs(), "bootout") != 1 {
		t.Fatalf("Uninstall() bootout 次数错误: %+v", runner.callArgs())
	}
}

func TestUninstallLaunchctlFailureHasRecoverySteps(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	runner.loaded, runner.running, runner.failVerb = true, true, "bootout"
	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), Label) || !strings.Contains(err.Error(), "dora status") || !strings.Contains(err.Error(), "dora uninstall") {
		t.Fatalf("uninstall 失败缺少恢复指引: %v", err)
	}
}

func TestManagerUsesArgumentRunnerWithoutShell(t *testing.T) {
	manager, runner, _, _ := newTestManager(t, true)
	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() 失败: %v", err)
	}
	for _, call := range runner.callsSnapshot() {
		if call.name != launchctl || len(call.args) == 0 {
			t.Fatalf("发现非参数化命令: %+v", call)
		}
	}
}

func TestLaunchAgentRejectsNonDarwin(t *testing.T) {
	manager, _, _, _ := newTestManager(t, true)
	manager.config.GOOS = "linux"
	if err := manager.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "只支持 macOS") {
		t.Fatalf("非 Darwin 错误 = %v", err)
	}
}

func assertInstalledFiles(t *testing.T, manager *Manager) {
	t.Helper()
	paths := manager.Paths()
	data, err := os.ReadFile(paths.Binary)
	if err != nil || string(data) != "production dora" {
		t.Fatalf("安装二进制错误: data=%q err=%v", data, err)
	}
	info, err := os.Stat(paths.Binary)
	if err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("安装二进制权限 = %v err=%v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(paths.Plist); err != nil {
		t.Fatalf("plist 未安装: %v", err)
	}
	for _, temporary := range []string{paths.BinaryTemp, paths.PlistTemp} {
		if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("原子安装临时文件仍存在 %s: %v", temporary, err)
		}
	}
}

func writeInstalledFiles(t *testing.T, paths Paths) {
	t.Helper()
	for _, directory := range []string{filepath.Dir(paths.Binary), paths.LaunchAgents} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.Binary, []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Plist, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(t *testing.T, production bool) (*Manager, *fakeRunner, *fakeHealth, *fakePort) {
	t.Helper()
	home := t.TempDir()
	executable := filepath.Join(t.TempDir(), "dora")
	if err := os.WriteFile(executable, []byte("production dora"), 0o751); err != nil {
		t.Fatal(err)
	}
	runner, health, port := &fakeRunner{}, &fakeHealth{}, &fakePort{}
	manager := New(Config{
		Home:           home,
		Executable:     executable,
		UID:            501,
		GOOS:           "darwin",
		Production:     production,
		FS:             OSFileSystem{},
		Runner:         runner,
		Health:         health,
		Port:           port,
		HealthTimeout:  20 * time.Millisecond,
		HealthPoll:     time.Millisecond,
		LaunchdTimeout: 20 * time.Millisecond,
		LaunchdPoll:    time.Millisecond,
	})
	return manager, runner, health, port
}

type runnerCall struct {
	name string
	args []string
}

type fakeRunner struct {
	mu                 sync.Mutex
	loaded             bool
	running            bool
	failVerb           string
	unloadDelay        int
	pendingUnload      bool
	stopAfterKickstart bool
	calls              []runnerCall
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, runnerCall{name: name, args: append([]string(nil), args...)})
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	if verb == runner.failVerb {
		return "synthetic launchctl failure", errors.New("exit status 1")
	}
	switch verb {
	case "print":
		if runner.pendingUnload {
			if runner.unloadDelay > 0 {
				runner.unloadDelay--
				return "state = running\npid = 123", nil
			}
			runner.pendingUnload = false
			runner.loaded, runner.running = false, false
		}
		if !runner.loaded {
			return "Could not find service", errors.New("exit status 113")
		}
		if runner.running {
			return "state = running\npid = 123", nil
		}
		return "state = exited", nil
	case "bootout":
		if runner.unloadDelay > 0 {
			runner.pendingUnload = true
		} else {
			runner.loaded, runner.running = false, false
		}
	case "bootstrap":
		runner.loaded, runner.running = true, true
	case "kickstart":
		runner.loaded, runner.running = true, true
		if runner.stopAfterKickstart {
			runner.running = false
		}
	}
	return "", nil
}

func (runner *fakeRunner) callsSnapshot() []runnerCall {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]runnerCall(nil), runner.calls...)
}

func (runner *fakeRunner) callArgs() [][]string {
	calls := runner.callsSnapshot()
	result := make([][]string, 0, len(calls))
	for _, call := range calls {
		result = append(result, call.args)
	}
	return result
}

type fakeHealth struct {
	mu    sync.Mutex
	err   error
	calls int
	after func()
}

func (health *fakeHealth) Check(context.Context, string) error {
	health.mu.Lock()
	health.calls++
	err, after := health.err, health.after
	health.mu.Unlock()
	if after != nil {
		after()
	}
	return err
}

type fakePort struct {
	mu     sync.Mutex
	err    error
	checks int
}

func (port *fakePort) Available(context.Context, string) error {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.checks++
	return port.err
}

func countCalls(calls [][]string, verb string) int {
	count := 0
	for _, call := range calls {
		if len(call) > 0 && call[0] == verb {
			count++
		}
	}
	return count
}
