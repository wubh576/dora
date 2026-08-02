package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/analytics"
	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/buildinfo"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/pricing"
	"github.com/wubh576/dora/backend/internal/provider/claudecode"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type server struct {
	store          *dorasqlite.Store
	scanner        *scan.Scanner
	controlToken   string
	allowedOrigins map[string]struct{}
	location       *time.Location
	now            func() time.Time
	quotaService   *quota.Service
	settings       *settings.Store
	buildInfo      buildinfo.Info
	logger         Logger
}

type Logger interface {
	Printf(string, ...any)
}

type healthResponse struct {
	Backend       bool           `json:"backend"`
	SQLite        bool           `json:"sqlite"`
	InitializedAt string         `json:"initializedAt"`
	ControlToken  string         `json:"controlToken,omitempty"`
	BuildInfo     buildinfo.Info `json:"buildInfo"`
}

type Options struct {
	Scanner        *scan.Scanner
	ControlToken   string
	AllowedOrigins []string
	Location       *time.Location
	Now            func() time.Time
	QuotaService   *quota.Service
	Settings       *settings.Store
	StaticFS       fs.FS
	BuildInfo      buildinfo.Info
	Logger         Logger
}

type scanResponse struct {
	RunID        string                 `json:"runId"`
	Mode         string                 `json:"mode"`
	FilesSeen    int                    `json:"filesSeen"`
	EventsSeen   int                    `json:"eventsSeen"`
	EventsStored int                    `json:"eventsStored"`
	Warnings     []string               `json:"warnings"`
	FinishedAt   string                 `json:"finishedAt"`
	Providers    []scanProviderResponse `json:"providers"`
}

type scanProviderResponse struct {
	Source       string   `json:"source"`
	Mode         string   `json:"mode"`
	FilesSeen    int      `json:"filesSeen"`
	SessionCount int      `json:"sessionCount"`
	EventsSeen   int      `json:"eventsSeen"`
	EventsStored int      `json:"eventsStored"`
	Warnings     []string `json:"warnings"`
}

type diagnosticsResponse struct {
	Usage          usageDiagnostics   `json:"usage"`
	UsageProviders []usageDiagnostics `json:"usageProviders"`
	Quota          quotaDiagnostics   `json:"quota"`
}

type usageDiagnostics struct {
	Source        string  `json:"source"`
	Status        string  `json:"status"`
	LastScanAt    *string `json:"lastScanAt"`
	LastRunMode   string  `json:"lastRunMode"`
	FilesSeen     int     `json:"filesSeen"`
	EventsSeen    int     `json:"eventsSeen"`
	StoredEvents  int     `json:"storedEvents"`
	ParserVersion int     `json:"parserVersion"`
	ConfigFound   bool    `json:"configFound"`
	SessionCount  int     `json:"sessionCount"`
	Message       string  `json:"message"`
	Advice        string  `json:"advice"`
}

type summaryResponse struct {
	Range     string                  `json:"range"`
	StartUTC  string                  `json:"startUtc"`
	EndUTC    string                  `json:"endUtc"`
	Cost      pricing.Estimate        `json:"cost"`
	Providers []providerUsageResponse `json:"providers"`
	analytics.TokenTotals
}

type providerUsageResponse struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	analytics.TokenTotals
	Models []analytics.BreakdownItem `json:"models"`
}

type timelineResponse struct {
	Range       string                    `json:"range"`
	Granularity string                    `json:"granularity"`
	Points      []analytics.TimelinePoint `json:"points"`
}

type breakdownResponse struct {
	Range     string                    `json:"range"`
	Dimension string                    `json:"dimension"`
	Items     []analytics.BreakdownItem `json:"items"`
}

type dashboardResponse struct {
	Summary             summaryResponse           `json:"summary"`
	Timeline            []analytics.TimelinePoint `json:"timeline"`
	Models              []analytics.BreakdownItem `json:"models"`
	Projects            []analytics.BreakdownItem `json:"projects"`
	Activity            activityResponse          `json:"activity"`
	Diagnostics         usageDiagnostics          `json:"diagnostics"`
	ProviderDiagnostics []usageDiagnostics        `json:"providerDiagnostics"`
}

type activityResponse struct {
	StartDate string                    `json:"startDate"`
	EndDate   string                    `json:"endDate"`
	Days      []analytics.TimelinePoint `json:"days"`
}

type snapshotResponse struct {
	GeneratedAt string        `json:"generatedAt"`
	Usage       snapshotUsage `json:"usage"`
	Quotas      []quotaItem   `json:"quotas"`
	Errors      []string      `json:"errors"`
}

type snapshotUsage struct {
	TodayTokens    int64                   `json:"todayTokens"`
	SevenDayTokens int64                   `json:"sevenDayTokens"`
	AllTimeTokens  int64                   `json:"allTimeTokens"`
	TopModel       string                  `json:"topModel"`
	LastScanAt     *string                 `json:"lastScanAt"`
	Stale          bool                    `json:"stale"`
	Providers      []snapshotProviderUsage `json:"providers"`
}

type snapshotProviderUsage struct {
	Source string `json:"source"`
	Tokens int64  `json:"tokens"`
}

type attentionResponse struct {
	GeneratedAt  string                     `json:"generatedAt"`
	WaitingCount int                        `json:"waitingCount"`
	Sessions     []attentionSessionResponse `json:"sessions"`
}

type attentionSessionResponse struct {
	ID           int64  `json:"id"`
	Provider     string `json:"provider"`
	Surface      string `json:"surface"`
	TerminalKind string `json:"terminalKind,omitempty"`
	CWDBasename  string `json:"cwdBasename"`
	Model        string `json:"model,omitempty"`
	Summary      string `json:"summary"`
	Kind         string `json:"kind"`
	WaitingSince string `json:"waitingSince"`
	WaitSeconds  int64  `json:"waitSeconds"`
	RequestCount int    `json:"requestCount"`
}

func NewHandler(store *dorasqlite.Store, options ...Options) http.Handler {
	s := &server{
		store:          store,
		allowedOrigins: make(map[string]struct{}),
		location:       time.Local,
		now:            time.Now,
		logger:         log.Default(),
	}
	if len(options) > 0 {
		s.scanner = options[0].Scanner
		s.controlToken = options[0].ControlToken
		for _, origin := range options[0].AllowedOrigins {
			s.allowedOrigins[origin] = struct{}{}
		}
		if options[0].Location != nil {
			s.location = options[0].Location
		}
		if options[0].Now != nil {
			s.now = options[0].Now
		}
		s.quotaService = options[0].QuotaService
		s.settings = options[0].Settings
		s.buildInfo = options[0].BuildInfo
		if options[0].Logger != nil {
			s.logger = options[0].Logger
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/summary", s.summary)
	mux.HandleFunc("/api/v1/timeline", s.timeline)
	mux.HandleFunc("/api/v1/breakdown", s.breakdown)
	mux.HandleFunc("/api/v1/dashboard", s.dashboard)
	mux.HandleFunc("/api/v1/snapshot", s.snapshot)
	mux.HandleFunc("/api/v1/diagnostics", s.diagnostics)
	mux.HandleFunc("/api/v1/scan", s.scan)
	mux.HandleFunc("/api/v1/quotas", s.quotas)
	mux.HandleFunc("/api/v1/quota/refresh", s.refreshQuota)
	mux.HandleFunc("/api/v1/settings", s.localSettings)
	mux.HandleFunc("/api/v1/attention", s.attention)
	mux.HandleFunc("/api/v1/hooks/codex", s.codexHook)
	if len(options) == 0 || options[0].StaticFS == nil {
		return mux
	}
	return newApplicationHandler(mux, options[0].StaticFS)
}

func (s *server) attention(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	now := s.now().UTC()
	waiting, err := s.store.WaitingSessions(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取实时等待状态", "请检查本地数据库")
		return
	}
	sessions := make([]attentionSessionResponse, 0, len(waiting))
	for _, item := range waiting {
		waitSeconds := int64(now.Sub(item.WaitingSince).Seconds())
		if waitSeconds < 0 {
			waitSeconds = 0
		}
		sessions = append(sessions, attentionSessionResponse{
			ID:           item.Session.ID,
			Provider:     item.Session.Provider,
			Surface:      item.Session.Surface,
			TerminalKind: item.Session.TerminalKind,
			CWDBasename:  item.Session.CWDBasename,
			Model:        item.Session.Model,
			Summary:      item.Latest.Summary,
			Kind:         item.Latest.Kind,
			WaitingSince: item.WaitingSince.Format(time.RFC3339Nano),
			WaitSeconds:  waitSeconds,
			RequestCount: item.RequestCount,
		})
	}
	writeNoStoreJSON(w, attentionResponse{
		GeneratedAt:  now.Format(time.RFC3339Nano),
		WaitingCount: len(sessions),
		Sessions:     sessions,
	})
}

func (s *server) codexHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.logCodexHookRejected("content_type")
		writeAPIError(w, http.StatusUnsupportedMediaType, domain.CodexSource, "接收实时事件", "请求必须使用 JSON")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var event attention.Event
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			s.logCodexHookRejected("size_limit")
			writeAPIError(w, http.StatusRequestEntityTooLarge, domain.CodexSource, "接收实时事件", "事件超过大小限制")
			return
		}
		s.logCodexHookRejected("invalid_json")
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "接收实时事件", "事件格式无效")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.logCodexHookRejected("trailing_content")
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "接收实时事件", "事件格式无效")
		return
	}
	domainEvent, err := event.Domain(s.now())
	if err != nil {
		s.logCodexHookRejected("invalid_fields")
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "接收实时事件", "事件字段无效")
		return
	}
	created, err := s.store.ApplyCodexHookEvent(r.Context(), domainEvent)
	if err != nil {
		s.logger.Printf(
			"Codex Hook 失败: provider=%s session=%s event=%s reason=storage_error",
			domain.CodexSource, attention.SessionLabel(domainEvent.ExternalSessionID), domainEvent.EventName,
		)
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "保存实时事件", "请检查本地数据库")
		return
	}
	state := "ended"
	if domainEvent.EventName != "SessionEnd" {
		state, err = s.store.RuntimeSessionState(r.Context(), domainEvent.ExternalSessionID)
		if errors.Is(err, sql.ErrNoRows) {
			state, err = "absent", nil
		}
		if err != nil {
			state = "unknown"
			s.logger.Printf(
				"Codex Hook 状态读取失败: provider=%s session=%s event=%s reason=storage_error",
				domain.CodexSource, attention.SessionLabel(domainEvent.ExternalSessionID), domainEvent.EventName,
			)
		}
	}
	requestStatus := ""
	if domainEvent.EventName == "PermissionRequest" || domainEvent.EventName == "PreToolUse" {
		requestStatus, err = s.store.AttentionRequestStatus(r.Context(), domainEvent.EventKey)
		if err != nil {
			requestStatus = "unknown"
			s.logger.Printf(
				"Codex Hook 请求状态读取失败: provider=%s session=%s event=%s reason=storage_error",
				domain.CodexSource, attention.SessionLabel(domainEvent.ExternalSessionID), domainEvent.EventName,
			)
		}
	}
	attentionResult := codexHookOutcome(domainEvent, created, requestStatus)
	toolName := domainEvent.ToolName
	if toolName == "" {
		toolName = "-"
	}
	s.logger.Printf(
		"Codex Hook: provider=%s session=%s event=%s surface=%s tool=%q state=%s attention=%s",
		domain.CodexSource,
		attention.SessionLabel(domainEvent.ExternalSessionID),
		domainEvent.EventName,
		domainEvent.Surface,
		toolName,
		state,
		attentionResult,
	)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) logCodexHookRejected(reason string) {
	s.logger.Printf("Codex Hook 拒绝: provider=%s reason=%s", domain.CodexSource, reason)
}

func codexHookOutcome(event domain.CodexHookEvent, created bool, requestStatus string) string {
	switch event.EventName {
	case "PermissionRequest", "PreToolUse":
		if created {
			return "created"
		}
		switch requestStatus {
		case dorasqlite.AttentionRequestActive:
			return "deduplicated"
		case dorasqlite.AttentionRequestResolved:
			return "ignored_resolved_replay"
		default:
			return "unknown"
		}
	case "Stop":
		return "resolved_by_stop"
	case "SessionEnd":
		return "resolved_by_session_end"
	case "PostToolUse":
		return "reconciled_by_tool_completion"
	case "UserPromptSubmit":
		return "reconciled_by_activity"
	case "SessionStart":
		return "reconciled_by_session_start"
	default:
		return "none"
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	initializedAt, err := s.store.InitializedAt(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, healthResponse{Backend: true, BuildInfo: s.buildInfo})
		return
	}

	writeJSON(w, healthResponse{
		Backend:       true,
		SQLite:        true,
		InitializedAt: initializedAt.Format("2006-01-02T15:04:05.000Z07:00"),
		ControlToken:  s.controlToken,
		BuildInfo:     s.buildInfo,
	})
}

func (s *server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	usage, usageProviders, err := s.loadAllUsageDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.local", "读取扫描状态", "请检查本地数据库后重试")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	quotaState, err := s.loadQuotaDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取配额状态", "请检查本地数据库后重试")
		return
	}
	writeJSON(w, diagnosticsResponse{Usage: usage, UsageProviders: usageProviders, Quota: quotaState})
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.scanner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.local", "触发扫描", "扫描器尚未配置")
		return
	}
	if !s.validWriteRequest(r) {
		writeAPIError(w, http.StatusForbidden, "provider.local", "触发扫描", "请从 Dora 本地页面重试")
		return
	}

	forceFull, valid := parseBooleanQuery(r.URL.Query().Get("full"))
	if !valid {
		writeAPIError(w, http.StatusBadRequest, "provider.local", "触发扫描", "full 只支持 true 或 false")
		return
	}
	report, err := s.scanner.Scan(r.Context(), forceFull)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "扫描本地用量", "请查看各 provider 诊断后重试")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, scanResponse{
		RunID:        report.RunID,
		Mode:         report.Mode,
		FilesSeen:    report.FilesSeen,
		EventsSeen:   report.EventsSeen,
		EventsStored: report.EventsStored,
		Warnings:     report.Warnings,
		FinishedAt:   report.FinishedAt.Format(time.RFC3339Nano),
		Providers:    scanProviderResponses(report.Providers),
	})
}

func (s *server) summary(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	window, events, ok := s.usageWindow(w, r)
	if !ok {
		return
	}
	totals, err := analytics.Summarize(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "汇总 token", "请重新扫描本地用量")
		return
	}
	cost, err := pricing.Default.Estimate(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "估算费用", "请重新扫描本地用量")
		return
	}
	providers, err := providerUsageResponses(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "汇总 provider token", "请重新扫描本地用量")
		return
	}
	writeNoStoreJSON(w, summaryResponse{
		Range:       window.Range,
		StartUTC:    window.StartUTC.Format(time.RFC3339Nano),
		EndUTC:      window.EndUTC.Format(time.RFC3339Nano),
		Cost:        cost,
		Providers:   providers,
		TokenTotals: totals,
	})
}

func (s *server) timeline(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	if granularity != "day" {
		writeAPIError(w, http.StatusBadRequest, "provider.local", "生成趋势", "granularity 只支持 day")
		return
	}
	window, events, ok := s.usageWindow(w, r)
	if !ok {
		return
	}
	points, err := analytics.DailyTimeline(events, window)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成趋势", "请重新扫描本地用量")
		return
	}
	if points == nil {
		points = []analytics.TimelinePoint{}
	}
	writeNoStoreJSON(w, timelineResponse{Range: window.Range, Granularity: granularity, Points: points})
}

func (s *server) breakdown(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	dimension := r.URL.Query().Get("dimension")
	window, events, ok := s.usageWindow(w, r)
	if !ok {
		return
	}
	items, err := analytics.Breakdown(events, dimension)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "provider.local", "生成分布", "dimension 只支持 model、project、provider 或 provider_model")
		return
	}
	if items == nil {
		items = []analytics.BreakdownItem{}
	}
	writeNoStoreJSON(w, breakdownResponse{Range: window.Range, Dimension: dimension, Items: items})
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	now := s.now()
	window, err := analytics.NewTimeWindow(now, s.location, r.URL.Query().Get("range"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "provider.local", "解析时间范围", "range 只支持 1D、7D、30D 或 ALL")
		return
	}
	activityWindow, err := analytics.NewTimeWindow(now, s.location, "ALL")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成仪表盘", "请重试")
		return
	}
	activityEvents, err := s.store.AllUsageEventsInWindow(
		r.Context(),
		activityWindow.StartUTC,
		activityWindow.EndUTC,
	)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.local", "读取 token", "请检查本地数据库")
		return
	}
	events := eventsInWindow(activityEvents, window)
	summary, err := analytics.Summarize(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成仪表盘", "请重新扫描本地用量")
		return
	}
	cost, err := pricing.Default.Estimate(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成费用估算", "请重新扫描本地用量")
		return
	}
	timeline, err := analytics.DailyTimeline(events, window)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成仪表盘", "请重新扫描本地用量")
		return
	}
	models, err := analytics.Breakdown(events, "model")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成仪表盘", "请重新扫描本地用量")
		return
	}
	projects, err := analytics.Breakdown(events, "project")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成仪表盘", "请重新扫描本地用量")
		return
	}
	providers, err := providerUsageResponses(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成 provider 分布", "请重新扫描本地用量")
		return
	}
	activity, err := analytics.DailyTimeline(activityEvents, activityWindow)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成热力图", "请重新扫描本地用量")
		return
	}
	diagnostics, providerDiagnostics, err := s.loadAllUsageDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.local", "生成仪表盘", "请检查本地数据库")
		return
	}
	if timeline == nil {
		timeline = []analytics.TimelinePoint{}
	}
	if models == nil {
		models = []analytics.BreakdownItem{}
	}
	if projects == nil {
		projects = []analytics.BreakdownItem{}
	}
	if activity == nil {
		activity = []analytics.TimelinePoint{}
	}
	writeNoStoreJSON(w, dashboardResponse{
		Summary: summaryResponse{
			Range:       window.Range,
			StartUTC:    window.StartUTC.Format(time.RFC3339Nano),
			EndUTC:      window.EndUTC.Format(time.RFC3339Nano),
			Cost:        cost,
			Providers:   providers,
			TokenTotals: summary,
		},
		Timeline: timeline,
		Models:   models,
		Projects: projects,
		Activity: activityResponse{
			StartDate: activityWindow.StartUTC.In(activityWindow.Location).Format(time.DateOnly),
			EndDate:   activityWindow.EndUTC.In(activityWindow.Location).Format(time.DateOnly),
			Days:      activity,
		},
		Diagnostics:         diagnostics,
		ProviderDiagnostics: providerDiagnostics,
	})
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	now := s.now()
	allWindow, err := analytics.NewTimeWindow(now, s.location, "ALL")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重试")
		return
	}
	allEvents, err := s.store.AllUsageEventsInWindow(r.Context(), allWindow.StartUTC, allWindow.EndUTC)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请检查本地数据库")
		return
	}
	todayWindow, err := analytics.NewTimeWindow(now, s.location, "1D")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重试")
		return
	}
	sevenDayWindow, err := analytics.NewTimeWindow(now, s.location, "7D")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重试")
		return
	}
	today, err := analytics.Summarize(eventsInWindow(allEvents, todayWindow))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重新扫描本地用量")
		return
	}
	sevenDays, err := analytics.Summarize(eventsInWindow(allEvents, sevenDayWindow))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重新扫描本地用量")
		return
	}
	allTotals, err := analytics.Summarize(allEvents)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重新扫描本地用量")
		return
	}
	models, err := analytics.Breakdown(allEvents, "model")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重新扫描本地用量")
		return
	}
	topModel := ""
	if len(models) > 0 {
		topModel = models[0].Name
	}
	providerStates := make(map[string]domain.UsageProviderState, 2)
	for _, source := range []string{domain.CodexSource, domain.ClaudeCodeSource} {
		state, err := s.store.UsageProviderState(r.Context(), source)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, source, "生成快照", "请检查本地数据库")
			return
		}
		providerStates[source] = state
	}
	providerUsage, err := providerUsageResponses(allEvents)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.local", "生成快照", "请重新扫描本地用量")
		return
	}
	snapshotProviders := make([]snapshotProviderUsage, 0, len(providerUsage))
	for _, provider := range providerUsage {
		snapshotProviders = append(snapshotProviders, snapshotProviderUsage{Source: provider.Source, Tokens: provider.TotalTokens})
	}

	response := snapshotResponse{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Usage: snapshotUsage{
			TodayTokens:    today.TotalTokens,
			SevenDayTokens: sevenDays.TotalTokens,
			AllTimeTokens:  allTotals.TotalTokens,
			TopModel:       topModel,
			Stale:          true,
			Providers:      snapshotProviders,
		},
		Quotas: []quotaItem{},
		Errors: []string{},
	}
	var oldestActiveScan, latestAvailableScan *time.Time
	for _, provider := range providerUsage {
		state := providerStates[provider.Source]
		if state.LastScanAt == nil {
			continue
		}
		if latestAvailableScan == nil || state.LastScanAt.After(*latestAvailableScan) {
			value := *state.LastScanAt
			latestAvailableScan = &value
		}
		if provider.EventCount > 0 && (oldestActiveScan == nil || state.LastScanAt.Before(*oldestActiveScan)) {
			value := *state.LastScanAt
			oldestActiveScan = &value
		}
	}
	snapshotScan := oldestActiveScan
	if snapshotScan == nil {
		snapshotScan = latestAvailableScan
	}
	if snapshotScan != nil {
		value := snapshotScan.Format(time.RFC3339Nano)
		response.Usage.LastScanAt = &value
		response.Usage.Stale = now.Sub(*snapshotScan) > 10*time.Minute
	}
	if providerStates[domain.CodexSource].Status == "error" {
		response.Errors = append(response.Errors, "Codex 本地用量扫描失败")
	}
	if providerStates[domain.ClaudeCodeSource].Status == "error" {
		response.Errors = append(response.Errors, "Claude Code 本地用量扫描失败")
	}
	if s.quotaService != nil {
		quotaView, err := s.quotaService.Snapshot(r.Context())
		if err != nil {
			response.Errors = append(response.Errors, "Codex 配额状态读取失败")
		} else {
			response.Quotas = quotaItems(quotaView.Items)
			if quotaView.Enabled && quotaView.Status == "error" {
				response.Errors = append(response.Errors, quotaView.Message)
			}
		}
	}
	writeNoStoreJSON(w, response)
}

func (s *server) usageWindow(w http.ResponseWriter, r *http.Request) (analytics.TimeWindow, []domain.UsageEvent, bool) {
	window, err := analytics.NewTimeWindow(s.now(), s.location, r.URL.Query().Get("range"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "provider.local", "解析时间范围", "range 只支持 1D、7D、30D 或 ALL")
		return analytics.TimeWindow{}, nil, false
	}
	events, err := s.store.AllUsageEventsInWindow(r.Context(), window.StartUTC, window.EndUTC)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.local", "读取 token", "请检查本地数据库")
		return analytics.TimeWindow{}, nil, false
	}
	return window, events, true
}

func eventsInWindow(events []domain.UsageEvent, window analytics.TimeWindow) []domain.UsageEvent {
	result := make([]domain.UsageEvent, 0, len(events))
	for _, event := range events {
		if !event.OccurredAt.Before(window.StartUTC) && event.OccurredAt.Before(window.EndUTC) {
			result = append(result, event)
		}
	}
	return result
}

func (s *server) loadAllUsageDiagnostics(r *http.Request) (usageDiagnostics, []usageDiagnostics, error) {
	providers := make([]usageDiagnostics, 0, 2)
	for _, source := range []string{domain.CodexSource, domain.ClaudeCodeSource} {
		diagnostics, err := s.loadUsageDiagnostics(r, source)
		if err != nil {
			return usageDiagnostics{}, nil, err
		}
		providers = append(providers, diagnostics)
	}
	return aggregateUsageDiagnostics(providers), providers, nil
}

func (s *server) loadUsageDiagnostics(r *http.Request, source string) (usageDiagnostics, error) {
	state, err := s.store.UsageProviderState(r.Context(), source)
	if err != nil {
		return usageDiagnostics{}, err
	}
	parserVersion := state.ParserVersion
	if parserVersion == 0 {
		switch source {
		case domain.CodexSource:
			parserVersion = codex.ParserVersion
		case domain.ClaudeCodeSource:
			parserVersion = claudecode.ParserVersion
		}
	}
	response := usageDiagnostics{
		Source:        source,
		Status:        state.Status,
		LastRunMode:   state.LastRunMode,
		FilesSeen:     state.FilesSeen,
		EventsSeen:    state.EventsSeen,
		StoredEvents:  state.StoredEvents,
		ParserVersion: parserVersion,
		ConfigFound:   state.ConfigFound,
		SessionCount:  state.SessionCount,
	}
	if state.LastScanAt != nil {
		value := state.LastScanAt.Format(time.RFC3339Nano)
		response.LastScanAt = &value
	}
	label := providerLabel(source)
	switch state.Status {
	case "error":
		response.Message = label + " 本地用量扫描失败"
		response.Advice = "请检查本地数据目录权限，并运行 dora scan 重试"
	case "not_found":
		response.Message = "未发现 " + label + " 本地数据目录"
		response.Advice = "产生本地会话后刷新；已有成功统计会继续保留"
	case "not_scanned":
		response.Message = "尚未扫描 " + label + " 本地用量"
		response.Advice = "启动 Dora 或运行 dora scan"
	case "degraded":
		response.Message = label + " 用量已更新，但跳过了无法安全去重的记录"
		response.Advice = "可运行 dora scan 查看扫描警告"
	default:
		response.Message = label + " 本地用量已就绪"
	}
	return response, nil
}

func aggregateUsageDiagnostics(providers []usageDiagnostics) usageDiagnostics {
	result := usageDiagnostics{Source: "provider.local", Status: "not_scanned", Message: "本地用量尚未完整扫描"}
	var latest *string
	hasReady, hasDegraded, hasError, allNotFound := false, false, false, true
	parserVersion := 0
	for _, provider := range providers {
		result.FilesSeen += provider.FilesSeen
		result.SessionCount += provider.SessionCount
		result.EventsSeen += provider.EventsSeen
		result.StoredEvents += provider.StoredEvents
		result.ConfigFound = result.ConfigFound || provider.ConfigFound
		if provider.StoredEvents > 0 || provider.FilesSeen > 0 {
			if parserVersion == 0 {
				parserVersion = provider.ParserVersion
			} else if parserVersion != provider.ParserVersion {
				parserVersion = -1
			}
		}
		if provider.LastScanAt != nil && (latest == nil || *provider.LastScanAt > *latest) {
			value := *provider.LastScanAt
			latest = &value
		}
		hasError = hasError || provider.Status == "error"
		hasDegraded = hasDegraded || provider.Status == "degraded"
		hasReady = hasReady || provider.Status == "ready"
		allNotFound = allNotFound && provider.Status == "not_found"
	}
	result.LastScanAt = latest
	if parserVersion > 0 {
		result.ParserVersion = parserVersion
	}
	switch {
	case hasError:
		result.Status = "error"
	case hasDegraded:
		result.Status = "degraded"
	case hasReady:
		result.Status = "ready"
	case allNotFound:
		result.Status = "not_found"
	}
	switch result.Status {
	case "error":
		result.Message = "部分本地用量扫描失败"
		result.Advice = "另一 provider 的最后成功数据仍可使用；请在下方查看具体状态"
	case "degraded":
		result.Message = "本地用量已更新，但存在跳过记录"
		result.Advice = "请在下方查看具体 provider 状态"
	case "not_scanned":
		result.Message = "本地用量尚未完整扫描"
		result.Advice = "启动 Dora 或运行 dora scan"
	case "not_found":
		result.Message = "尚未发现本地 Agent 数据"
		result.Advice = "产生 Codex 或 Claude Code 本地会话后刷新"
	default:
		result.Message = "本地用量已就绪"
	}
	return result
}

func providerUsageResponses(events []domain.UsageEvent) ([]providerUsageResponse, error) {
	result := make([]providerUsageResponse, 0, 2)
	for _, source := range []string{domain.CodexSource, domain.ClaudeCodeSource} {
		providerEvents := make([]domain.UsageEvent, 0)
		for _, event := range events {
			if event.Source == source {
				providerEvents = append(providerEvents, event)
			}
		}
		totals, err := analytics.Summarize(providerEvents)
		if err != nil {
			return nil, err
		}
		models, err := analytics.Breakdown(providerEvents, "model")
		if err != nil {
			return nil, err
		}
		if models == nil {
			models = []analytics.BreakdownItem{}
		}
		result = append(result, providerUsageResponse{
			Source: source, Label: providerLabel(source), TokenTotals: totals, Models: models,
		})
	}
	return result, nil
}

func providerLabel(source string) string {
	switch source {
	case domain.CodexSource:
		return "Codex"
	case domain.ClaudeCodeSource:
		return "Claude Code"
	default:
		return source
	}
}

func scanProviderResponses(reports []scan.ProviderReport) []scanProviderResponse {
	result := make([]scanProviderResponse, 0, len(reports))
	for _, report := range reports {
		result = append(result, scanProviderResponse{
			Source: report.Source, Mode: report.Mode, FilesSeen: report.FilesSeen,
			SessionCount: report.SessionCount, EventsSeen: report.EventsSeen,
			EventsStored: report.EventsStored, Warnings: append([]string(nil), report.Warnings...),
		})
	}
	return result
}

func requireGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func parseBooleanQuery(value string) (bool, bool) {
	switch strings.ToLower(value) {
	case "", "false":
		return false, true
	case "true":
		return true, true
	default:
		return false, false
	}
}

func writeNoStoreJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, value)
}

func (s *server) validWriteRequest(r *http.Request) bool {
	if s.controlToken == "" || r.Header.Get("X-Dora-Control-Token") != s.controlToken {
		return false
	}
	_, allowed := s.allowedOrigins[r.Header.Get("Origin")]
	return allowed
}

func writeAPIError(w http.ResponseWriter, status int, provider, operation, advice string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{
		"provider":  provider,
		"operation": operation,
		"advice":    advice,
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}
