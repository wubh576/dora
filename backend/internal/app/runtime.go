package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/httpapi"
	"github.com/wubh576/dora/backend/internal/jump"
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
	attentionInterval   = time.Second
	threadTitleInterval = time.Second
	staleCheckInterval  = time.Hour
	runtimeSessionStale = 7 * 24 * time.Hour
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
	JumpRunner    jump.Runner
}

// Runtime 统一持有 HTTP、SQLite、扫描器和配额服务，serve 与 menubar 共用它。
type Runtime struct {
	address        string
	initializedAt  time.Time
	server         *http.Server
	listener       net.Listener
	store          *dorasqlite.Store
	scanner        *scan.Scanner
	quota          *quota.Service
	jump           *jump.Service
	permission     *attention.PermissionBroker
	threadTitles   *codex.ThreadTitleReader
	logger         Logger
	ctx            context.Context
	cancel         context.CancelFunc
	errors         chan error
	wg             sync.WaitGroup
	closeOnce      sync.Once
	attentionOnce  sync.Once
	titleErrorOnce sync.Once
	closeErr       error
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
	threadTitles, titleErr := codex.OpenThreadTitleReader(ctx, config.CodexHomes)
	if titleErr != nil {
		config.Logger.Printf("Codex 任务标题读取不可用；运行列表将回退为项目名")
		threadTitles = nil
	}
	cleanup := func() {
		cancel()
		_ = threadTitles.Close()
		_ = store.Close()
	}

	scanner := scan.New(store, config.CodexHomes)
	if config.ClaudeEnabled {
		scanner = scan.NewWithClaude(store, config.CodexHomes, config.ClaudeHomes)
	}
	settingsStore := settings.New(filepath.Join(filepath.Dir(config.DBPath), "settings.json"))
	quotaService := quota.NewService(codex.NewQuotaClient(config.CodexHomes), store, settingsStore)
	jumpRunner := config.JumpRunner
	if jumpRunner == nil {
		jumpRunner = osCommandRunner{}
	}
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
	resolvedStale, err := store.ResolveStaleRuntimeSessions(ctx, startedAt.Add(-runtimeSessionStale), time.Now().UTC())
	if err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("清理过期 Codex 实时状态: %w", err)
	}
	if resolvedStale > 0 {
		config.Logger.Printf("Codex stale reconciliation: sessions=%d reason=stale_session", resolvedStale)
	}
	restoredRunning, err := store.RestoreRunningSessions(ctx)
	if err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("恢复 Codex 历史运行态: %w", err)
	}
	if restoredRunning > 0 {
		config.Logger.Printf("Codex runtime recovery: sessions=%d state=idle", restoredRunning)
	}
	// 进程启动前遗留的 waiting 仍展示，但不作为本次启动的新提醒再次发声。
	if err := store.MarkHistoricalAttentionNotified(ctx, startedAt, time.Now().UTC()); err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("恢复 Codex 实时提醒状态: %w", err)
	}
	actualAddress := listener.Addr().String()
	permissionBroker := attention.NewPermissionBroker(attention.PermissionWaitTimeout)
	server := &http.Server{
		Addr: actualAddress,
		Handler: httpapi.NewHandler(store, httpapi.Options{
			Scanner:          scanner,
			ControlToken:     controlToken,
			AllowedOrigins:   []string{"http://" + actualAddress, FrontendOrigin},
			QuotaService:     quotaService,
			Settings:         settingsStore,
			StaticFS:         config.StaticFS,
			BuildInfo:        config.BuildInfo,
			Logger:           config.Logger,
			PermissionBroker: permissionBroker,
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
		jump:          jump.New(jumpRunner),
		permission:    permissionBroker,
		threadTitles:  threadTitles,
		logger:        config.Logger,
		ctx:           ctx,
		cancel:        cancel,
		errors:        make(chan error, 1),
	}

	if config.LogRotator != nil {
		config.LogRotator.Check()
	}
	runtime.wg.Add(4)
	go runtime.serve()
	go runtime.scanLoop(ctx, config.ScanInterval)
	go runtime.quotaLoop(ctx, config.ScanInterval)
	go runtime.staleReconciliationLoop(ctx)
	if threadTitles != nil {
		runtime.wg.Add(1)
		go runtime.threadTitleLoop(ctx)
	}
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

type AttentionNotifier interface {
	Notify(context.Context, domain.AttentionRequest) error
}

func (r *Runtime) StartAttentionNotifications(notifier AttentionNotifier) {
	if notifier == nil {
		return
	}
	r.attentionOnce.Do(func() {
		r.wg.Add(1)
		go r.attentionLoop(r.ctx, notifier)
	})
}

func (r *Runtime) JumpAttentionSession(ctx context.Context, sessionID int64) error {
	session, err := r.store.RuntimeSession(ctx, sessionID)
	if err != nil {
		r.logf("Codex 回跳失败: session=unknown reason=session_unavailable")
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("这个 Codex 会话已经结束")
		}
		return err
	}
	label := attention.SessionLabel(session.ExternalSessionID)
	r.logf(
		"Codex 回跳开始: provider=%s session=%s surface=%s terminal=%s",
		session.Provider, label, session.Surface, terminalLabel(session.TerminalKind),
	)
	if err := r.jump.Jump(ctx, session); err != nil {
		if errors.Is(err, jump.ErrTargetGone) {
			if resolveErr := r.store.ResolveRuntimeSession(ctx, sessionID, time.Now().UTC(), "target_gone"); resolveErr != nil {
				r.logf("Codex 回跳失败: provider=%s session=%s reason=target_gone_reconcile_failed", session.Provider, label)
				return errors.Join(err, resolveErr)
			}
		}
		r.logf(
			"Codex 回跳失败: provider=%s session=%s surface=%s terminal=%s reason=%s",
			session.Provider, label, session.Surface, terminalLabel(session.TerminalKind), jumpFailureReason(err),
		)
		return err
	}
	r.logf(
		"Codex 回跳完成: provider=%s session=%s surface=%s terminal=%s result=success",
		session.Provider, label, session.Surface, terminalLabel(session.TerminalKind),
	)
	return nil
}

func (r *Runtime) Submit(ctx context.Context, interactionID string, action attention.PermissionAction) error {
	return r.permission.Submit(ctx, interactionID, action)
}

// Refresh 先更新本地 token，再刷新配额；配额失败不会回滚已完成的扫描。
func (r *Runtime) Refresh(ctx context.Context) (usageErr, quotaErr error) {
	_, usageErr = r.scanner.Scan(ctx, false)
	_, quotaErr = r.quota.Refresh(ctx, true)
	return usageErr, quotaErr
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.permission.Close()
		r.cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := r.server.Shutdown(shutdownCtx)
		r.wg.Wait()
		r.closeErr = errors.Join(shutdownErr, r.threadTitles.Close(), r.store.Close())
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

func (r *Runtime) attentionLoop(ctx context.Context, notifier AttentionNotifier) {
	defer r.wg.Done()
	attentionTicker := time.NewTicker(attentionInterval)
	defer attentionTicker.Stop()
	notify := func() {
		attempted, err := notifyAttentionOnce(ctx, r.store, notifier, time.Now().UTC())
		if err != nil && !backgroundStopped(ctx, err) {
			r.logger.Printf("Codex 实时提醒失败: requests=%d reason=%s", attempted, singleLineError(err))
		} else if attempted > 0 {
			r.logger.Printf("Codex 实时提醒完成: requests=%d result=success", attempted)
		}
	}
	notify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-attentionTicker.C:
			notify()
		}
	}
}

func (r *Runtime) staleReconciliationLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(staleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			resolved, err := r.store.ResolveStaleRuntimeSessions(ctx, at.Add(-runtimeSessionStale), at.UTC())
			if err != nil && !backgroundStopped(ctx, err) {
				r.logger.Printf("Codex stale reconciliation 失败: reason=%s", singleLineError(err))
			} else if resolved > 0 {
				r.logger.Printf("Codex stale reconciliation: sessions=%d reason=stale_session", resolved)
			}
		}
	}
}

type threadTitleSource interface {
	Titles(context.Context, []string) (map[string]string, error)
}

func (r *Runtime) threadTitleLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(threadTitleInterval)
	defer ticker.Stop()
	for {
		if err := syncRuntimeSessionTitles(ctx, r.store, r.threadTitles); err != nil && !backgroundStopped(ctx, err) {
			r.titleErrorOnce.Do(func() {
				r.logger.Printf("Codex 任务标题同步失败；运行列表将使用缓存标题或项目名")
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncRuntimeSessionTitles(ctx context.Context, store *dorasqlite.Store, source threadTitleSource) error {
	active, err := store.RuntimeSessions(ctx)
	if err != nil {
		return err
	}
	sessionIDs := make([]string, 0, len(active))
	for _, item := range active {
		sessionIDs = append(sessionIDs, item.Session.ExternalSessionID)
	}
	titles, err := source.Titles(ctx, sessionIDs)
	if err != nil {
		return err
	}
	updates := make(map[string]string)
	for _, item := range active {
		if title := titles[item.Session.ExternalSessionID]; title != "" && title != item.Session.SessionName {
			updates[item.Session.ExternalSessionID] = title
		}
	}
	return store.UpdateRuntimeSessionNames(ctx, updates)
}

type attentionStore interface {
	ClaimUnnotifiedAttention(context.Context, time.Time) ([]domain.AttentionRequest, error)
}

func notifyAttentionOnce(ctx context.Context, store attentionStore, notifier AttentionNotifier, at time.Time) (int, error) {
	requests, err := store.ClaimUnnotifiedAttention(ctx, at)
	if err != nil {
		return 0, err
	}
	var notifyErrors []error
	for _, request := range requests {
		if err := notifier.Notify(ctx, request); err != nil {
			notifyErrors = append(notifyErrors, err)
		}
	}
	return len(requests), errors.Join(notifyErrors...)
}

func (r *Runtime) logf(format string, args ...any) {
	if r.logger != nil {
		r.logger.Printf(format, args...)
	}
}

func terminalLabel(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func jumpFailureReason(err error) string {
	switch {
	case errors.Is(err, jump.ErrTargetGone):
		return "target_gone"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "execution_error"
	}
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
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
