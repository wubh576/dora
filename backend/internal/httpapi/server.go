package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/scan"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type server struct {
	store          *dorasqlite.Store
	scanner        *scan.Scanner
	controlToken   string
	allowedOrigins map[string]struct{}
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
}

type usageDiagnostics struct {
	Source      string  `json:"source"`
	Status      string  `json:"status"`
	LastScanAt  *string `json:"lastScanAt"`
	LastRunMode string  `json:"lastRunMode"`
	FilesSeen   int     `json:"filesSeen"`
	EventsSeen  int     `json:"eventsSeen"`
	Message     string  `json:"message"`
	Advice      string  `json:"advice"`
}

func NewHandler(store *dorasqlite.Store, options ...Options) http.Handler {
	s := &server{store: store, allowedOrigins: make(map[string]struct{})}
	if len(options) > 0 {
		s.scanner = options[0].Scanner
		s.controlToken = options[0].ControlToken
		for _, origin := range options[0].AllowedOrigins {
			s.allowedOrigins[origin] = struct{}{}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/diagnostics", s.diagnostics)
	mux.HandleFunc("/api/v1/scan", s.scan)
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

	state, err := s.store.UsageProviderState(r.Context(), domain.CodexSource)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "provider.codex", "读取扫描状态", "请检查本地数据库后重试")
		return
	}
	response := diagnosticsResponse{Usage: usageDiagnostics{
		Source:      domain.CodexSource,
		Status:      state.Status,
		LastRunMode: state.LastRunMode,
		FilesSeen:   state.FilesSeen,
		EventsSeen:  state.EventsSeen,
	}}
	if state.LastScanAt != nil {
		value := state.LastScanAt.Format(time.RFC3339Nano)
		response.Usage.LastScanAt = &value
	}
	switch state.Status {
	case "error":
		response.Usage.Message = "Codex 本地用量扫描失败"
		response.Usage.Advice = "请检查 Codex 数据目录权限，并运行 dora scan 重试"
	case "not_scanned":
		response.Usage.Message = "尚未扫描 Codex 本地用量"
		response.Usage.Advice = "启动 Dora 或运行 dora scan"
	case "degraded":
		response.Usage.Message = "Codex 用量已更新，但存在可忽略记录"
		response.Usage.Advice = "可查看本地日志了解扫描警告"
	default:
		response.Usage.Message = "Codex 本地用量已就绪"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
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

	report, err := s.scanner.Scan(r.Context(), false)
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
