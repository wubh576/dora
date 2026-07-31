package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/wubh576/dora/backend/internal/app"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/launchagent"
	doramenubar "github.com/wubh576/dora/backend/internal/menubar"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
	"github.com/wubh576/dora/backend/internal/webassets"
)

const (
	defaultServerAddress = app.DefaultAddress
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "menubar" {
		// AppKit 事件循环必须从进程主线程进入。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	if err := run(os.Args[1:]); err != nil {
		var commandErr *commandExitError
		if !errors.As(err, &commandErr) || commandErr.err != nil {
			log.Printf("Dora 命令失败: %v", err)
		}
		os.Exit(commandExitCode(err))
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: dora <serve|menubar|scan|quota|install|status|uninstall> [选项]")
	}
	switch args[0] {
	case "install":
		return installCommand(args[1:])
	case "status":
		return launchAgentStatusCommand(args[1:])
	case "uninstall":
		return uninstallCommand(args[1:])
	}

	defaultDBPath, err := databasePath()
	if err != nil {
		return err
	}

	switch args[0] {
	case "serve":
		return serve(args[1:], defaultDBPath)
	case "menubar":
		return runMenubar(args[1:], defaultDBPath)
	case "scan":
		return scanUsage(args[1:], defaultDBPath)
	case "quota":
		return quotaCommand(args[1:], defaultDBPath)
	default:
		return fmt.Errorf("未知命令 %q；用法: dora <serve|menubar|scan|quota|install|status|uninstall> [选项]", args[0])
	}
}

type commandExitError struct {
	code int
	err  error
}

func (e *commandExitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *commandExitError) Unwrap() error { return e.err }

func commandExitCode(err error) int {
	var commandErr *commandExitError
	if errors.As(err, &commandErr) {
		return commandErr.code
	}
	return 1
}

func installCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("install 不支持参数 %q", args[0])
	}
	manager, err := newLaunchAgentManager()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := manager.Install(ctx); err != nil {
		return err
	}
	fmt.Printf("Dora 已安装并正在运行\n菜单栏：点击 Dora 图标查看用量\n仪表盘：%s\n", launchagent.DashboardURL)
	return nil
}

func launchAgentStatusCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("status 不支持参数 %q", args[0])
	}
	manager, err := newLaunchAgentManager()
	if err != nil {
		return &commandExitError{code: 2, err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := manager.Status(ctx)
	if err != nil {
		return &commandExitError{code: 2, err: fmt.Errorf("检查 Dora 状态: %w", err)}
	}
	installed, loaded := "否", "未加载"
	if status.Installed() {
		installed = "是"
	}
	if status.Loaded {
		loaded = "已加载"
	}
	if err := writeLaunchAgentStatus(os.Stdout, status, installed, loaded, buildinfo.Current()); err != nil {
		return &commandExitError{code: 2, err: err}
	}
	if status.ExitCode() != 0 {
		return &commandExitError{code: status.ExitCode()}
	}
	return nil
}

func writeLaunchAgentStatus(output io.Writer, status launchagent.Status, installed, loaded string, commandInfo buildinfo.Info) error {
	info, source := commandInfo, "当前命令"
	if status.BuildInfo != nil {
		info, source = *status.BuildInfo, "运行中的 LaunchAgent"
	}
	state := "clean"
	if info.Dirty {
		state = "dirty"
	}
	_, err := fmt.Fprintf(output, "安装：%s\nLaunchAgent：%s\n运行：%s\n仪表盘：%s\n构建来源：%s\n构建标识：%s\nCommit：%s\n构建状态：%s\n构建时间：%s\nGo：%s\n架构：%s\nmacOS：%s\n", installed, loaded, status.RunState(), status.DashboardURL, source, info.BuildID(), info.GitCommit, state, info.BuildTime, info.GoVersion, info.Platform(), info.MacOSVersion)
	return err
}

func uninstallCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("uninstall 不支持参数 %q", args[0])
	}
	manager, err := newLaunchAgentManager()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := manager.Uninstall(ctx); err != nil {
		return err
	}
	fmt.Println("Dora LaunchAgent 已卸载；数据库、设置和日志均已保留")
	return nil
}

func newLaunchAgentManager() (*launchagent.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("读取当前用户目录: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("读取当前 Dora 可执行文件: %w", err)
	}
	return launchagent.New(launchagent.Config{
		Home:       home,
		Executable: executable,
		UID:        os.Getuid(),
		GOOS:       runtime.GOOS,
		Production: webassets.Files() != nil,
	}), nil
}

func serve(args []string, defaultDBPath string) error {
	options, err := parseApplicationOptions("serve", args, defaultDBPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := startApplication(ctx, options)
	if err != nil {
		return err
	}
	return waitForRuntime(ctx, application)
}

func runMenubar(args []string, defaultDBPath string) error {
	options, err := parseApplicationOptions("menubar", args, defaultDBPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := startApplication(ctx, options)
	if err != nil {
		return err
	}
	return runMenubarApplication(ctx, stop, application, doramenubar.Run)
}

type menuRunner func(context.Context, doramenubar.Config) error

func runMenubarApplication(ctx context.Context, stop context.CancelFunc, application *app.Runtime, runMenu menuRunner) error {
	runtimeErr := make(chan error, 1)
	go func() {
		select {
		case err := <-application.Errors():
			runtimeErr <- err
			stop()
		case <-ctx.Done():
		}
	}()
	menuErr := runMenu(ctx, doramenubar.Config{
		Loader:       doramenubar.NewClient(application.DashboardURL()),
		Refresher:    application,
		DashboardURL: application.DashboardURL(),
		Quit:         stop,
	})
	closeErr := application.Close()
	select {
	case err := <-runtimeErr:
		return errors.Join(menuErr, closeErr, err)
	default:
		return errors.Join(menuErr, closeErr)
	}
}

type applicationOptions struct {
	address, dbPath string
	codexHomes      []string
}

func parseApplicationOptions(command string, args []string, defaultDBPath string) (applicationOptions, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	address := flags.String("addr", defaultServerAddress, "HTTP 监听地址")
	dbPath := flags.String("db", defaultDBPath, "SQLite 数据库路径")
	var codexHomes stringListFlag
	flags.Var(&codexHomes, "codex-home", "Codex 数据目录，可重复指定")
	if err := flags.Parse(args); err != nil {
		return applicationOptions{}, err
	}
	if flags.NArg() != 0 {
		return applicationOptions{}, fmt.Errorf("%s 不支持位置参数 %q", command, flags.Arg(0))
	}
	if err := app.ValidateLoopbackAddress(*address); err != nil {
		return applicationOptions{}, err
	}
	return applicationOptions{address: *address, dbPath: *dbPath, codexHomes: append([]string(nil), codexHomes...)}, nil
}

func startApplication(ctx context.Context, options applicationOptions) (*app.Runtime, error) {
	return app.Start(ctx, app.Config{Address: options.address, DBPath: options.dbPath, CodexHomes: options.codexHomes, StaticFS: webassets.Files(), BuildInfo: buildinfo.Current()})
}

func waitForRuntime(ctx context.Context, application *app.Runtime) error {
	select {
	case err := <-application.Errors():
		return errors.Join(err, application.Close())
	case <-ctx.Done():
		return application.Close()
	}
}

func quotaCommand(args []string, defaultDBPath string) error {
	if len(args) == 0 || args[0] != "refresh" {
		return errors.New("用法: dora quota refresh [选项]")
	}
	flags := flag.NewFlagSet("quota refresh", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "SQLite 数据库路径")
	var codexHomes stringListFlag
	flags.Var(&codexHomes, "codex-home", "Codex 数据目录，可重复指定")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := dorasqlite.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("初始化 SQLite: %w", err)
	}
	defer store.Close()
	settingsStore := settings.New(filepath.Join(filepath.Dir(*dbPath), "settings.json"))
	service := quota.NewService(codex.NewQuotaClient(codexHomes), store, settingsStore)
	view, err := service.Refresh(ctx, true)
	if err != nil {
		return fmt.Errorf("刷新 Codex 订阅配额: %w", err)
	}
	if !view.Enabled {
		return errors.New("Codex 订阅配额尚未授权，请先在 Dora Diagnostics 中启用")
	}
	fmt.Printf("Codex 配额刷新完成：%d 个窗口\n", len(view.Items))
	return nil
}

func scanUsage(args []string, defaultDBPath string) error {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	dbPath := flags.String("db", defaultDBPath, "SQLite 数据库路径")
	full := flags.Bool("full", false, "强制执行全量扫描")
	var codexHomes stringListFlag
	flags.Var(&codexHomes, "codex-home", "Codex 数据目录，可重复指定")
	if err := flags.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := dorasqlite.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("初始化 SQLite: %w", err)
	}
	defer store.Close()

	report, err := scan.New(store, codexHomes).Scan(ctx, *full)
	if err != nil {
		return fmt.Errorf("扫描 Codex 本地用量: %w", err)
	}
	fmt.Printf(
		"Codex 扫描完成：模式 %s，文件 %d，新增解析事件 %d，去重后事件 %d\n",
		report.Mode,
		report.FilesSeen,
		report.EventsSeen,
		report.EventsStored,
	)
	for _, warning := range report.Warnings {
		fmt.Printf("扫描警告：%s\n", warning)
	}
	return nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return fmt.Sprint([]string(*values))
}

func (values *stringListFlag) Set(value string) error {
	if value == "" {
		return errors.New("Codex home 不能为空")
	}
	*values = append(*values, value)
	return nil
}

func databasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("读取用户应用目录: %w", err)
	}
	return filepath.Join(configDir, "Dora", "dora.db"), nil
}
