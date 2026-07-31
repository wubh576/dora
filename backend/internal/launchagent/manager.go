package launchagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	Label        = "io.github.wubh576.dora"
	DashboardURL = "http://127.0.0.1:8080"
	launchctl    = "/bin/launchctl"

	defaultHealthTimeout  = 15 * time.Second
	defaultHealthPoll     = 250 * time.Millisecond
	defaultLaunchdTimeout = 5 * time.Second
	defaultLaunchdPoll    = 100 * time.Millisecond
	throttleInterval      = 10
)

type Paths struct {
	Binary       string
	BinaryTemp   string
	Plist        string
	PlistTemp    string
	StdoutLog    string
	StderrLog    string
	Database     string
	Settings     string
	Application  string
	LaunchAgents string
	Logs         string
}

func PathsForHome(home string) Paths {
	application := filepath.Join(home, "Library", "Application Support", "Dora")
	binary := filepath.Join(application, "bin", "dora")
	launchAgents := filepath.Join(home, "Library", "LaunchAgents")
	plist := filepath.Join(launchAgents, Label+".plist")
	logs := filepath.Join(home, "Library", "Logs", "Dora")
	return Paths{
		Binary:       binary,
		BinaryTemp:   binary + ".tmp",
		Plist:        plist,
		PlistTemp:    plist + ".tmp",
		StdoutLog:    filepath.Join(logs, "dora.stdout.log"),
		StderrLog:    filepath.Join(logs, "dora.stderr.log"),
		Database:     filepath.Join(application, "dora.db"),
		Settings:     filepath.Join(application, "settings.json"),
		Application:  application,
		LaunchAgents: launchAgents,
		Logs:         logs,
	}
}

type Config struct {
	Home           string
	Executable     string
	UID            int
	GOOS           string
	Production     bool
	FS             FileSystem
	Runner         Runner
	Health         HealthChecker
	Port           PortChecker
	HealthTimeout  time.Duration
	HealthPoll     time.Duration
	LaunchdTimeout time.Duration
	LaunchdPoll    time.Duration
}

type Manager struct {
	config Config
	paths  Paths
}

func New(config Config) *Manager {
	if config.FS == nil {
		config.FS = OSFileSystem{}
	}
	if config.Runner == nil {
		config.Runner = CommandRunner{}
	}
	if config.Health == nil {
		config.Health = HTTPHealthChecker{}
	}
	if config.Port == nil {
		config.Port = TCPPortChecker{}
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaultHealthTimeout
	}
	if config.HealthPoll <= 0 {
		config.HealthPoll = defaultHealthPoll
	}
	if config.LaunchdTimeout <= 0 {
		config.LaunchdTimeout = defaultLaunchdTimeout
	}
	if config.LaunchdPoll <= 0 {
		config.LaunchdPoll = defaultLaunchdPoll
	}
	return &Manager{config: config, paths: PathsForHome(config.Home)}
}

func (m *Manager) Paths() Paths {
	return m.paths
}

func (m *Manager) Install(ctx context.Context) error {
	if err := m.validatePlatform(); err != nil {
		return err
	}
	if !m.config.Production {
		return errors.New("当前可执行文件不包含生产 Web 资源；请先执行 make build，再运行 ./bin/dora install")
	}
	info, err := m.config.FS.Stat(m.config.Executable)
	if err != nil {
		return fmt.Errorf("读取当前 Dora 可执行文件: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("当前文件不是可执行的 Dora 二进制: %s", m.config.Executable)
	}
	launchd, err := m.inspectLaunchd(ctx)
	if err != nil {
		return err
	}
	if !launchd.Loaded {
		if err := m.ensurePortAvailable(ctx); err != nil {
			return err
		}
	}
	for _, directory := range []string{filepath.Dir(m.paths.Binary), m.paths.LaunchAgents, m.paths.Logs} {
		if err := m.config.FS.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("创建 Dora 安装目录 %s: %w", directory, err)
		}
	}
	if err := atomicCopy(m.config.FS, m.config.Executable, m.paths.BinaryTemp, m.paths.Binary, info.Mode().Perm()); err != nil {
		return fmt.Errorf("安装 Dora 可执行文件: %w", err)
	}
	plist, err := buildPlist(m.paths)
	if err != nil {
		return fmt.Errorf("生成 LaunchAgent plist: %w", err)
	}
	if err := atomicWrite(m.config.FS, m.paths.PlistTemp, m.paths.Plist, plist, 0o644); err != nil {
		return fmt.Errorf("安装 LaunchAgent plist: %w", err)
	}

	domain, target := m.domain(), m.serviceTarget()
	if launchd.Loaded {
		if _, err := m.runLaunchctl(ctx, "bootout", target); err != nil {
			return fmt.Errorf("更新 LaunchAgent %s 时停止旧进程: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora install", Label, err)
		}
		if err := m.waitForUnloaded(ctx); err != nil {
			return fmt.Errorf("更新 LaunchAgent %s 时等待旧进程退出: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora install", Label, err)
		}
		if err := m.ensurePortAvailable(ctx); err != nil {
			return err
		}
	}
	if _, err := m.runLaunchctl(ctx, "bootstrap", domain, m.paths.Plist); err != nil {
		return fmt.Errorf("加载 LaunchAgent %s: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora install", Label, err)
	}
	if _, err := m.runLaunchctl(ctx, "kickstart", "-k", target); err != nil {
		return fmt.Errorf("启动 LaunchAgent %s: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora install", Label, err)
	}
	if err := m.waitForHealth(ctx); err != nil {
		return fmt.Errorf("LaunchAgent %s 已加载，但 Dora 健康检查失败: %w；请查看 %s，排障后重试 ./bin/dora install", Label, err, m.paths.StderrLog)
	}
	started, err := m.inspectLaunchd(ctx)
	if err != nil {
		return err
	}
	if !started.Loaded || !started.Running {
		return fmt.Errorf("LaunchAgent %s 已加载，但菜单栏进程未保持运行；请查看 %s，排障后重试 ./bin/dora install", Label, m.paths.StderrLog)
	}
	return nil
}

type Status struct {
	PlistPresent  bool
	BinaryPresent bool
	Loaded        bool
	Running       bool
	Healthy       bool
	DashboardURL  string
}

func (s Status) Installed() bool {
	return s.PlistPresent && s.BinaryPresent
}

func (s Status) RunState() string {
	switch {
	case s.Healthy:
		return "正常"
	case !s.Running:
		return "未运行"
	default:
		return "异常"
	}
}

func (s Status) ExitCode() int {
	if s.Installed() && s.Loaded && s.Running && s.Healthy {
		return 0
	}
	return 1
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	if err := m.validatePlatform(); err != nil {
		return Status{}, err
	}
	plistPresent, err := fileExists(m.config.FS, m.paths.Plist)
	if err != nil {
		return Status{}, fmt.Errorf("检查 LaunchAgent plist: %w", err)
	}
	binaryPresent, err := fileExists(m.config.FS, m.paths.Binary)
	if err != nil {
		return Status{}, fmt.Errorf("检查安装后的 Dora 二进制: %w", err)
	}
	launchd, err := m.inspectLaunchd(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		PlistPresent:  plistPresent,
		BinaryPresent: binaryPresent,
		Loaded:        launchd.Loaded,
		Running:       launchd.Running,
		DashboardURL:  DashboardURL,
	}
	if launchd.Loaded && launchd.Running {
		if m.config.Health.Check(ctx, DashboardURL) == nil {
			confirmed, inspectErr := m.inspectLaunchd(ctx)
			if inspectErr != nil {
				return Status{}, inspectErr
			}
			status.Loaded = confirmed.Loaded
			status.Running = confirmed.Running
			status.Healthy = confirmed.Loaded && confirmed.Running
		}
	}
	return status, nil
}

func (m *Manager) Uninstall(ctx context.Context) error {
	if err := m.validatePlatform(); err != nil {
		return err
	}
	launchd, err := m.inspectLaunchd(ctx)
	if err != nil {
		return err
	}
	if launchd.Loaded {
		if _, err := m.runLaunchctl(ctx, "bootout", m.serviceTarget()); err != nil {
			return fmt.Errorf("卸载 LaunchAgent %s 时停止进程: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora uninstall", Label, err)
		}
		if err := m.waitForUnloaded(ctx); err != nil {
			return fmt.Errorf("卸载 LaunchAgent %s 时等待进程退出: %w；请运行 ./bin/dora status，排障后重试 ./bin/dora uninstall", Label, err)
		}
	}
	for _, path := range []string{m.paths.Plist, m.paths.Binary, m.paths.PlistTemp, m.paths.BinaryTemp} {
		if err := removeIfExists(m.config.FS, path); err != nil {
			return fmt.Errorf("删除 Dora 安装文件 %s: %w", path, err)
		}
	}
	return nil
}

type launchdState struct {
	Loaded  bool
	Running bool
}

func (m *Manager) inspectLaunchd(ctx context.Context) (launchdState, error) {
	output, err := m.runLaunchctl(ctx, "print", m.serviceTarget())
	if err == nil {
		return launchdState{Loaded: true, Running: strings.Contains(output, "state = running")}, nil
	}
	if isMissingService(output) {
		return launchdState{}, nil
	}
	return launchdState{}, fmt.Errorf("检查 LaunchAgent %s: %w；可运行 /bin/launchctl print %s 查看详情", Label, err, m.serviceTarget())
}

func (m *Manager) runLaunchctl(ctx context.Context, args ...string) (string, error) {
	output, err := m.config.Runner.Run(ctx, launchctl, args...)
	if err == nil {
		return output, nil
	}
	summary := strings.TrimSpace(output)
	if len(summary) > 500 {
		summary = summary[:500] + "…"
	}
	if summary == "" {
		return output, err
	}
	return output, fmt.Errorf("launchctl %s: %s: %w", args[0], summary, err)
}

func (m *Manager) waitForHealth(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, m.config.HealthTimeout)
	defer cancel()
	var lastErr error
	for {
		if err := m.config.Health.Check(waitCtx, DashboardURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(m.config.HealthPoll)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if lastErr != nil {
				return lastErr
			}
			return waitCtx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) waitForUnloaded(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, m.config.LaunchdTimeout)
	defer cancel()
	for {
		state, err := m.inspectLaunchd(waitCtx)
		if err != nil {
			return err
		}
		if !state.Loaded {
			return nil
		}
		timer := time.NewTimer(m.config.LaunchdPoll)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("等待 launchd 卸载超时: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (m *Manager) validatePlatform() error {
	if m.config.GOOS != "darwin" {
		return fmt.Errorf("LaunchAgent 只支持 macOS，当前系统为 %s", m.config.GOOS)
	}
	if m.config.Home == "" || m.config.Executable == "" || m.config.UID < 0 {
		return errors.New("LaunchAgent 运行环境不完整")
	}
	return nil
}

func (m *Manager) ensurePortAvailable(ctx context.Context) error {
	if err := m.config.Port.Available(ctx, "127.0.0.1:8080"); err != nil {
		return fmt.Errorf("无法安装 LaunchAgent %s：127.0.0.1:8080 已被占用；请先退出现有 Dora 或其他本地服务: %w", Label, err)
	}
	return nil
}

func (m *Manager) domain() string {
	return fmt.Sprintf("gui/%d", m.config.UID)
}

func (m *Manager) serviceTarget() string {
	return m.domain() + "/" + Label
}

func isMissingService(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "could not find service") || strings.Contains(lower, "service not found")
}

func fileExists(files FileSystem, path string) (bool, error) {
	_, err := files.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func removeIfExists(files FileSystem, path string) error {
	err := files.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
