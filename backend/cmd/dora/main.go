package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wubh576/dora/backend/internal/httpapi"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

const (
	usageScanInterval    = 5 * time.Minute
	defaultServerAddress = "127.0.0.1:8080"
	frontendOrigin       = "http://127.0.0.1:5173"
	loopbackHost         = "127.0.0.1"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("Dora 启动失败: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: dora <serve|scan|quota> [选项]")
	}

	defaultDBPath, err := databasePath()
	if err != nil {
		return err
	}

	switch args[0] {
	case "serve":
		return serve(args[1:], defaultDBPath)
	case "scan":
		return scanUsage(args[1:], defaultDBPath)
	case "quota":
		return quotaCommand(args[1:], defaultDBPath)
	default:
		return fmt.Errorf("未知命令 %q；用法: dora <serve|scan|quota> [选项]", args[0])
	}
}

func serve(args []string, defaultDBPath string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", defaultServerAddress, "HTTP 监听地址")
	dbPath := flags.String("db", defaultDBPath, "SQLite 数据库路径")
	var codexHomes stringListFlag
	flags.Var(&codexHomes, "codex-home", "Codex 数据目录，可重复指定")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateLoopbackAddress(*addr); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := dorasqlite.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("初始化 SQLite: %w", err)
	}
	defer store.Close()

	usageScanner := scan.New(store, codexHomes)
	settingsStore := settings.New(filepath.Join(filepath.Dir(*dbPath), "settings.json"))
	quotaService := quota.NewService(codex.NewQuotaClient(codexHomes), store, settingsStore)
	controlToken, err := newControlToken()
	if err != nil {
		return err
	}

	initializedAt, err := store.InitializedAt(ctx)
	if err != nil {
		return fmt.Errorf("读取 Dora 初始化状态: %w", err)
	}

	server := &http.Server{
		Addr: *addr,
		Handler: httpapi.NewHandler(store, httpapi.Options{
			Scanner:        usageScanner,
			ControlToken:   controlToken,
			AllowedOrigins: []string{"http://" + *addr, frontendOrigin},
			QuotaService:   quotaService,
			Settings:       settingsStore,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("监听 HTTP 地址: %w", err)
	}
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Dora 后端已启动: http://%s（初始化时间 %s）", *addr, initializedAt.Format(time.RFC3339))
		serverErr <- server.Serve(listener)
	}()
	go runUsageScanLoop(ctx, usageScanner, usageScanInterval)
	go runQuotaRefreshLoop(ctx, quotaService, usageScanInterval)

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("运行 HTTP 服务: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("关闭 HTTP 服务: %w", err)
		}
		return nil
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

type usageScanRunner interface {
	Scan(context.Context, bool) (scan.Report, error)
}

type quotaRefreshRunner interface {
	Refresh(context.Context, bool) (quota.View, error)
}

func runUsageScanLoop(ctx context.Context, scanner usageScanRunner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := scanner.Scan(ctx, false)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Codex 用量自动扫描失败，请运行 dora scan 查看详情")
			}
		} else {
			log.Printf(
				"Codex 用量扫描完成: mode=%s files=%d events=%d stored=%d",
				report.Mode,
				report.FilesSeen,
				report.EventsSeen,
				report.EventsStored,
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runQuotaRefreshLoop(ctx context.Context, refresher quotaRefreshRunner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		view, err := refresher.Refresh(ctx, false)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Codex 配额自动刷新失败，本地 token 统计不受影响")
			}
		} else if view.Enabled {
			log.Printf("Codex 配额刷新完成: windows=%d status=%s", len(view.Items), view.Status)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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

func newControlToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("生成 control token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func databasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("读取用户应用目录: %w", err)
	}
	return filepath.Join(configDir, "Dora", "dora.db"), nil
}

func validateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("解析 HTTP 监听地址 %q: %w", addr, err)
	}
	if host != loopbackHost {
		return fmt.Errorf("HTTP 只能监听 %s，当前地址为 %q", loopbackHost, addr)
	}
	return nil
}
