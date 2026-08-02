package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/httpapi"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

const (
	DefaultAddress      = "127.0.0.1:8080"
	DefaultScanInterval = 5 * time.Minute
	FrontendOrigin      = "http://127.0.0.1:5173"
	loopbackHost        = "127.0.0.1"
)

type Logger interface {
	Printf(string, ...any)
}

type LogRotator interface {
	Check()
	Run(context.Context)
}

type Config struct {
	Address       string
	DBPath        string
	CodexHomes    []string
	ClaudeHomes   []string
	ClaudeEnabled bool
	StaticFS      fs.FS
	ScanInterval  time.Duration
	Logger        Logger
	BuildInfo     buildinfo.Info
	LogRotator    LogRotator
}

// Runtime 统一持有 HTTP、SQLite、扫描器和配额服务，serve 与 menubar 共用它。
type Runtime struct {
	address       string
	initializedAt time.Time
	server        *http.Server
	listener      net.Listener
	store         *dorasqlite.Store
	scanner       *scan.Scanner
	quota         *quota.Service
	logger        Logger
	cancel        context.CancelFunc
	errors        chan error
	wg            sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
}

func Start(parent context.Context, config Config) (*Runtime, error) {
	startedAt := time.Now().UTC()
	if config.Address == "" {
		config.Address = DefaultAddress
	}
	if config.ScanInterval <= 0 {
		config.ScanInterval = DefaultScanInterval
	}
	if config.Logger == nil {
		config.Logger = log.Default()
	}
	if config.BuildInfo == (buildinfo.Info{}) {
		config.BuildInfo = buildinfo.Current()
	}
	if err := ValidateLoopbackAddress(config.Address); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	store, err := dorasqlite.Open(ctx, config.DBPath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("初始化 SQLite: %w", err)
	}
	cleanup := func() {
		cancel()
		_ = store.Close()
	}

	scanner := scan.New(store, config.CodexHomes)
	if config.ClaudeEnabled {
		scanner = scan.NewWithClaude(store, config.CodexHomes, config.ClaudeHomes)
	}
	settingsStore := settings.New(filepath.Join(filepath.Dir(config.DBPath), "settings.json"))
	quotaService := quota.NewService(codex.NewQuotaClient(config.CodexHomes), store, settingsStore)
	controlToken, err := newControlToken()
	if err != nil {
		cleanup()
		return nil, err
	}
	initializedAt, err := store.InitializedAt(ctx)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("读取 Dora 初始化状态: %w", err)
	}
	// 先完成端口绑定，避免启动失败时留下半运行的后台任务或菜单。
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("监听 HTTP 地址 %s: %w", config.Address, err)
	}
	// 进程启动前遗留的 waiting 仍展示，但不作为本次启动的新提醒再次发声。
	if err := store.MarkHistoricalAttentionNotified(ctx, startedAt, time.Now().UTC()); err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("恢复 Codex 实时提醒状态: %w", err)
	}
	actualAddress := listener.Addr().String()
	server := &http.Server{
		Addr: actualAddress,
		Handler: httpapi.NewHandler(store, httpapi.Options{
			Scanner:        scanner,
			ControlToken:   controlToken,
			AllowedOrigins: []string{"http://" + actualAddress, FrontendOrigin},
			QuotaService:   quotaService,
			Settings:       settingsStore,
			StaticFS:       config.StaticFS,
			BuildInfo:      config.BuildInfo,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	runtime := &Runtime{
		address:       actualAddress,
		initializedAt: initializedAt,
		server:        server,
		listener:      listener,
		store:         store,
		scanner:       scanner,
		quota:         quotaService,
		logger:        config.Logger,
		cancel:        cancel,
		errors:        make(chan error, 1),
	}

	if config.LogRotator != nil {
		config.LogRotator.Check()
	}
	runtime.wg.Add(3)
	go runtime.serve()
	go runtime.scanLoop(ctx, config.ScanInterval)
	go runtime.quotaLoop(ctx, config.ScanInterval)
	if config.LogRotator != nil {
		runtime.wg.Add(1)
		go runtime.logRotationLoop(ctx, config.LogRotator)
	}
	config.Logger.Printf("Dora 构建信息: %s", config.BuildInfo.LogString())
	config.Logger.Printf("Dora 已启动：http://%s（初始化时间 %s）", actualAddress, initializedAt.Format(time.RFC3339))
	return runtime, nil
}

func (r *Runtime) Address() string { return r.address }

func (r *Runtime) DashboardURL() string { return "http://" + r.address }

func (r *Runtime) InitializedAt() time.Time { return r.initializedAt }

func (r *Runtime) Errors() <-chan error { return r.errors }

// Refresh 先更新本地 token，再刷新配额；配额失败不会回滚已完成的扫描。
func (r *Runtime) Refresh(ctx context.Context) (usageErr, quotaErr error) {
	_, usageErr = r.scanner.Scan(ctx, false)
	_, quotaErr = r.quota.Refresh(ctx, true)
	return usageErr, quotaErr
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := r.server.Shutdown(shutdownCtx)
		r.wg.Wait()
		r.closeErr = errors.Join(shutdownErr, r.store.Close())
	})
	return r.closeErr
}

func (r *Runtime) serve() {
	defer r.wg.Done()
	if err := r.server.Serve(r.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		select {
		case r.errors <- fmt.Errorf("运行 HTTP 服务: %w", err):
		default:
		}
		r.cancel()
	}
}

func (r *Runtime) scanLoop(ctx context.Context, interval time.Duration) {
	defer r.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := r.scanner.Scan(ctx, false)
		logUsageScanResult(ctx, r.logger, report, err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) quotaLoop(ctx context.Context, interval time.Duration) {
	defer r.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		view, err := r.quota.Refresh(ctx, false)
		logQuotaRefreshResult(ctx, r.logger, view, err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) logRotationLoop(ctx context.Context, rotator LogRotator) {
	defer r.wg.Done()
	rotator.Run(ctx)
}

func logUsageScanResult(ctx context.Context, logger Logger, report scan.Report, err error) {
	if len(report.Providers) > 1 {
		if err != nil && backgroundStopped(ctx, err) {
			return
		}
		for _, provider := range report.Providers {
			label := provider.Source
			switch provider.Source {
			case "provider.codex":
				label = "Codex"
			case "provider.claude-code":
				label = "Claude Code"
			}
			if provider.Error != "" {
				logger.Printf("%s 用量自动扫描失败: %s；已保留该 provider 上次成功数据", label, singleLineError(errors.New(provider.Error)))
				continue
			}
			logger.Printf(
				"%s 用量扫描完成: mode=%s files=%d sessions=%d events=%d stored=%d",
				label, provider.Mode, provider.FilesSeen, provider.SessionCount,
				provider.EventsSeen, provider.EventsStored,
			)
		}
		return
	}
	if err != nil {
		if backgroundStopped(ctx, err) {
			return
		}
		logger.Printf("Codex 用量自动扫描失败: %s；可运行 dora scan 重试并查看终端输出", singleLineError(err))
		return
	}
	logger.Printf("Codex 用量扫描完成: mode=%s files=%d events=%d stored=%d", report.Mode, report.FilesSeen, report.EventsSeen, report.EventsStored)
}

func logQuotaRefreshResult(ctx context.Context, logger Logger, view quota.View, err error) {
	if err != nil {
		if backgroundStopped(ctx, err) {
			return
		}
		logger.Printf("Codex 配额自动刷新失败: %s；本地 token 统计不受影响", singleLineError(err))
		return
	}
	if view.Enabled {
		logger.Printf("Codex 配额刷新完成: windows=%d status=%s", len(view.Items), view.Status)
	}
}

func backgroundStopped(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func singleLineError(err error) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
}

func ValidateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("解析 HTTP 监听地址 %q: %w", address, err)
	}
	if host != loopbackHost {
		return fmt.Errorf("HTTP 只能监听 %s，当前地址为 %q", loopbackHost, address)
	}
	return nil
}

func newControlToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("生成 control token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
