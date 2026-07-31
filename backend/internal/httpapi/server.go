package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/analytics"
	"github.com/wubh576/dora/backend/internal/domain"
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
}

type healthResponse struct {
	Backend       bool   `json:"backend"`
	SQLite        bool   `json:"sqlite"`
	InitializedAt string `json:"initializedAt"`
	ControlToken  string `json:"controlToken,omitempty"`
}

type Options struct {
	Scanner        *scan.Scanner
	ControlToken   string
	AllowedOrigins []string
	Location       *time.Location
	Now            func() time.Time
	QuotaService   *quota.Service
	Settings       *settings.Store
}

type scanResponse struct {
	RunID        string   `json:"runId"`
	Mode         string   `json:"mode"`
	FilesSeen    int      `json:"filesSeen"`
	EventsSeen   int      `json:"eventsSeen"`
	EventsStored int      `json:"eventsStored"`
	Warnings     []string `json:"warnings"`
	FinishedAt   string   `json:"finishedAt"`
}

type diagnosticsResponse struct {
	Usage usageDiagnostics `json:"usage"`
	Quota quotaDiagnostics `json:"quota"`
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
	Message       string  `json:"message"`
	Advice        string  `json:"advice"`
}

type summaryResponse struct {
	Range    string `json:"range"`
	StartUTC string `json:"startUtc"`
	EndUTC   string `json:"endUtc"`
	analytics.TokenTotals
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
	Summary     summaryResponse           `json:"summary"`
	Timeline    []analytics.TimelinePoint `json:"timeline"`
	Models      []analytics.BreakdownItem `json:"models"`
	Projects    []analytics.BreakdownItem `json:"projects"`
	Activity    activityResponse          `json:"activity"`
	Diagnostics usageDiagnostics          `json:"diagnostics"`
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
	TodayTokens    int64   `json:"todayTokens"`
	SevenDayTokens int64   `json:"sevenDayTokens"`
	AllTimeTokens  int64   `json:"allTimeTokens"`
	TopModel       string  `json:"topModel"`
	LastScanAt     *string `json:"lastScanAt"`
	Stale          bool    `json:"stale"`
}

func NewHandler(store *dorasqlite.Store, options ...Options) http.Handler {
	s := &server{
		store:          store,
		allowedOrigins: make(map[string]struct{}),
		location:       time.Local,
		now:            time.Now,
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
	return mux
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
		writeJSON(w, healthResponse{Backend: true})
		return
	}

	writeJSON(w, healthResponse{
		Backend:       true,
		SQLite:        true,
		InitializedAt: initializedAt.Format("2006-01-02T15:04:05.000Z07:00"),
		ControlToken:  s.controlToken,
	})
}

func (s *server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	usage, err := s.loadUsageDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.codex", "读取扫描状态", "请检查本地数据库后重试")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	quotaState, err := s.loadQuotaDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取配额状态", "请检查本地数据库后重试")
		return
	}
	writeJSON(w, diagnosticsResponse{Usage: usage, Quota: quotaState})
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.scanner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.codex", "触发扫描", "扫描器尚未配置")
		return
	}
	if !s.validWriteRequest(r) {
		writeAPIError(w, http.StatusForbidden, "provider.codex", "触发扫描", "请从 Dora 本地页面重试")
		return
	}

	forceFull, valid := parseBooleanQuery(r.URL.Query().Get("full"))
	if !valid {
		writeAPIError(w, http.StatusBadRequest, "provider.codex", "触发扫描", "full 只支持 true 或 false")
		return
	}
	report, err := s.scanner.Scan(r.Context(), forceFull)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "provider.codex", "扫描本地用量", "请检查 Codex 数据目录权限后重试")
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
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "汇总 token", "请重新扫描 Codex 用量")
		return
	}
	writeNoStoreJSON(w, summaryResponse{
		Range:       window.Range,
		StartUTC:    window.StartUTC.Format(time.RFC3339Nano),
		EndUTC:      window.EndUTC.Format(time.RFC3339Nano),
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
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "生成趋势", "granularity 只支持 day")
		return
	}
	window, events, ok := s.usageWindow(w, r)
	if !ok {
		return
	}
	points, err := analytics.DailyTimeline(events, window)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成趋势", "请重新扫描 Codex 用量")
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
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "生成分布", "dimension 只支持 model 或 project")
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
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "解析时间范围", "range 只支持 1D、7D、30D 或 ALL")
		return
	}
	activityWindow, err := analytics.NewTimeWindow(now, s.location, "ALL")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成仪表盘", "请重试")
		return
	}
	activityEvents, err := s.store.UsageEventsInWindow(
		r.Context(),
		domain.CodexSource,
		activityWindow.StartUTC,
		activityWindow.EndUTC,
	)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取 token", "请检查本地数据库")
		return
	}
	events := eventsInWindow(activityEvents, window)
	summary, err := analytics.Summarize(events)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成仪表盘", "请重新扫描 Codex 用量")
		return
	}
	timeline, err := analytics.DailyTimeline(events, window)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成仪表盘", "请重新扫描 Codex 用量")
		return
	}
	models, err := analytics.Breakdown(events, "model")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成仪表盘", "请重新扫描 Codex 用量")
		return
	}
	projects, err := analytics.Breakdown(events, "project")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成仪表盘", "请重新扫描 Codex 用量")
		return
	}
	activity, err := analytics.DailyTimeline(activityEvents, activityWindow)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成热力图", "请重新扫描 Codex 用量")
		return
	}
	diagnostics, err := s.loadUsageDiagnostics(r)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "生成仪表盘", "请检查本地数据库")
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
		Diagnostics: diagnostics,
	})
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	now := s.now()
	allWindow, err := analytics.NewTimeWindow(now, s.location, "ALL")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重试")
		return
	}
	allEvents, err := s.store.UsageEventsInWindow(r.Context(), domain.CodexSource, allWindow.StartUTC, allWindow.EndUTC)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请检查本地数据库")
		return
	}
	todayWindow, err := analytics.NewTimeWindow(now, s.location, "1D")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重试")
		return
	}
	sevenDayWindow, err := analytics.NewTimeWindow(now, s.location, "7D")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重试")
		return
	}
	today, err := analytics.Summarize(eventsInWindow(allEvents, todayWindow))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重新扫描 Codex 用量")
		return
	}
	sevenDays, err := analytics.Summarize(eventsInWindow(allEvents, sevenDayWindow))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重新扫描 Codex 用量")
		return
	}
	allTotals, err := analytics.Summarize(allEvents)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重新扫描 Codex 用量")
		return
	}
	models, err := analytics.Breakdown(allEvents, "model")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请重新扫描 Codex 用量")
		return
	}
	topModel := ""
	if len(models) > 0 {
		topModel = models[0].Name
	}
	providerState, err := s.store.UsageProviderState(r.Context(), domain.CodexSource)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "生成快照", "请检查本地数据库")
		return
	}

	response := snapshotResponse{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Usage: snapshotUsage{
			TodayTokens:    today.TotalTokens,
			SevenDayTokens: sevenDays.TotalTokens,
			AllTimeTokens:  allTotals.TotalTokens,
			TopModel:       topModel,
			Stale:          true,
		},
		Quotas: []quotaItem{},
		Errors: []string{},
	}
	if providerState.LastScanAt != nil {
		value := providerState.LastScanAt.Format(time.RFC3339Nano)
		response.Usage.LastScanAt = &value
		response.Usage.Stale = now.Sub(*providerState.LastScanAt) > 10*time.Minute
	}
	if providerState.Status == "error" {
		response.Errors = append(response.Errors, "Codex 本地用量扫描失败")
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
		writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "解析时间范围", "range 只支持 1D、7D、30D 或 ALL")
		return analytics.TimeWindow{}, nil, false
	}
	events, err := s.store.UsageEventsInWindow(r.Context(), domain.CodexSource, window.StartUTC, window.EndUTC)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取 token", "请检查本地数据库")
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

func (s *server) loadUsageDiagnostics(r *http.Request) (usageDiagnostics, error) {
	state, err := s.store.UsageProviderState(r.Context(), domain.CodexSource)
	if err != nil {
		return usageDiagnostics{}, err
	}
	response := usageDiagnostics{
		Source:        domain.CodexSource,
		Status:        state.Status,
		LastRunMode:   state.LastRunMode,
		FilesSeen:     state.FilesSeen,
		EventsSeen:    state.EventsSeen,
		StoredEvents:  state.StoredEvents,
		ParserVersion: codex.ParserVersion,
	}
	if state.LastScanAt != nil {
		value := state.LastScanAt.Format(time.RFC3339Nano)
		response.LastScanAt = &value
	}
	switch state.Status {
	case "error":
		response.Message = "Codex 本地用量扫描失败"
		response.Advice = "请检查 Codex 数据目录权限，并运行 dora scan 重试"
	case "not_scanned":
		response.Message = "尚未扫描 Codex 本地用量"
		response.Advice = "启动 Dora 或运行 dora scan"
	case "degraded":
		response.Message = "Codex 用量已更新，但存在可忽略记录"
		response.Advice = "可运行 dora scan 查看扫描警告"
	default:
		response.Message = "Codex 本地用量已就绪"
	}
	return response, nil
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
