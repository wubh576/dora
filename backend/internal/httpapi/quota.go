package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/settings"
)

type settingsResponse struct {
	CodexQuotaConsent bool `json:"codexQuotaConsent"`
}

type settingsUpdateRequest struct {
	CodexQuotaConsent *bool `json:"codexQuotaConsent"`
}

type quotaResponse struct {
	Enabled       bool        `json:"enabled"`
	Status        string      `json:"status"`
	LastSuccessAt *string     `json:"lastSuccessAt"`
	Items         []quotaItem `json:"items"`
	Message       string      `json:"message"`
	Advice        string      `json:"advice"`
}

type quotaItem struct {
	Provider         string  `json:"provider"`
	WindowKey        string  `json:"windowKey"`
	Label            string  `json:"label"`
	UsedPercent      float64 `json:"usedPercent"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetsAt         *string `json:"resetsAt"`
	FetchedAt        string  `json:"fetchedAt"`
	SourceState      string  `json:"sourceState"`
	Plan             string  `json:"plan"`
	AccountLabel     string  `json:"accountLabel"`
}

type quotaDiagnostics struct {
	Enabled       bool    `json:"enabled"`
	Status        string  `json:"status"`
	LastSuccessAt *string `json:"lastSuccessAt"`
	Message       string  `json:"message"`
	Advice        string  `json:"advice"`
}

func (s *server) quotas(w http.ResponseWriter, r *http.Request) {
	if !requireGet(w, r) {
		return
	}
	if s.quotaService == nil {
		writeNoStoreJSON(w, quotaResponse{
			Status:  "not_configured",
			Items:   []quotaItem{},
			Message: "Codex 订阅配额服务尚未配置",
		})
		return
	}
	view, err := s.quotaService.Snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取订阅配额", "请检查本地数据库")
		return
	}
	writeNoStoreJSON(w, quotaViewResponse(view))
}

func (s *server) refreshQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.quotaService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "刷新订阅配额", "配额服务尚未配置")
		return
	}
	if !s.validWriteRequest(r) {
		writeAPIError(w, http.StatusForbidden, domain.CodexSource, "刷新订阅配额", "请从 Dora 本地页面重试")
		return
	}

	view, err := s.quotaService.Refresh(r.Context(), true)
	if err != nil {
		advice := "请稍后重试，本地 token 统计不受影响"
		var quotaErr *codex.QuotaError
		if errors.As(err, &quotaErr) {
			advice = quotaErr.Advice
		}
		writeAPIError(w, http.StatusBadGateway, domain.CodexSource, "刷新订阅配额", advice)
		return
	}
	writeNoStoreJSON(w, quotaViewResponse(view))
}

func (s *server) localSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取本地设置", "设置存储尚未配置")
		return
	}
	switch r.Method {
	case http.MethodGet:
		values, err := s.settings.Load()
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, domain.CodexSource, "读取本地设置", "请检查 settings.json")
			return
		}
		writeNoStoreJSON(w, settingsResponse{CodexQuotaConsent: values.CodexQuotaConsent})
	case http.MethodPut:
		if !s.validWriteRequest(r) {
			writeAPIError(w, http.StatusForbidden, domain.CodexSource, "更新本地设置", "请从 Dora 本地页面重试")
			return
		}
		var request settingsUpdateRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "更新本地设置", "请求只支持 codexQuotaConsent 布尔值")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "更新本地设置", "请求只能包含一个 JSON 对象")
			return
		}
		if request.CodexQuotaConsent == nil {
			writeAPIError(w, http.StatusBadRequest, domain.CodexSource, "更新本地设置", "必须提供 codexQuotaConsent 布尔值")
			return
		}
		consent := *request.CodexQuotaConsent
		if err := s.settings.Save(settings.Values{CodexQuotaConsent: consent}); err != nil {
			writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "更新本地设置", "请检查 Dora 应用目录权限")
			return
		}
		if !consent && s.quotaService != nil {
			if err := s.quotaService.Disable(r.Context()); err != nil {
				writeAPIError(w, http.StatusInternalServerError, domain.CodexSource, "关闭订阅配额", "请检查本地数据库")
				return
			}
		}
		writeNoStoreJSON(w, settingsResponse{CodexQuotaConsent: consent})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) loadQuotaDiagnostics(r *http.Request) (quotaDiagnostics, error) {
	if s.quotaService == nil {
		return quotaDiagnostics{
			Status:  "not_configured",
			Message: "Codex 订阅配额服务尚未配置",
		}, nil
	}
	view, err := s.quotaService.Snapshot(r.Context())
	if err != nil {
		return quotaDiagnostics{}, err
	}
	response := quotaDiagnostics{
		Enabled: view.Enabled,
		Status:  view.Status,
		Message: view.Message,
		Advice:  view.Advice,
	}
	if view.LastSuccessAt != nil {
		value := view.LastSuccessAt.UTC().Format(time.RFC3339Nano)
		response.LastSuccessAt = &value
	}
	return response, nil
}

func quotaViewResponse(view quota.View) quotaResponse {
	response := quotaResponse{
		Enabled: view.Enabled,
		Status:  view.Status,
		Items:   quotaItems(view.Items),
		Message: view.Message,
		Advice:  view.Advice,
	}
	if view.LastSuccessAt != nil {
		value := view.LastSuccessAt.UTC().Format(time.RFC3339Nano)
		response.LastSuccessAt = &value
	}
	return response
}

func quotaItems(snapshots []domain.QuotaSnapshot) []quotaItem {
	result := make([]quotaItem, 0, len(snapshots))
	for _, snapshot := range snapshots {
		item := quotaItem{
			Provider:         snapshot.Provider,
			WindowKey:        snapshot.WindowKey,
			Label:            snapshot.Label,
			UsedPercent:      snapshot.UsedPercent,
			RemainingPercent: snapshot.RemainingPercent,
			FetchedAt:        snapshot.FetchedAt.UTC().Format(time.RFC3339Nano),
			SourceState:      snapshot.SourceState,
			Plan:             snapshot.Plan,
			AccountLabel:     snapshot.AccountLabel,
		}
		if snapshot.ResetsAt != nil {
			value := snapshot.ResetsAt.UTC().Format(time.RFC3339Nano)
			item.ResetsAt = &value
		}
		result = append(result, item)
	}
	return result
}
