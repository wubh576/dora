package quota

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	"github.com/wubh576/dora/backend/internal/settings"
)

const (
	refreshInterval = 5 * time.Minute
	staleAfter      = 10 * time.Minute
)

type Provider interface {
	Fetch(context.Context) ([]domain.QuotaSnapshot, error)
}

type Repository interface {
	SaveQuotaSuccess(context.Context, []domain.QuotaSnapshot) error
	SetQuotaStatus(context.Context, string, string, string) error
	LatestQuotaSnapshots(context.Context, string) ([]domain.QuotaSnapshot, error)
	QuotaProviderState(context.Context, string) (domain.QuotaProviderState, error)
}

type Settings interface {
	Load() (settings.Values, error)
}

type View struct {
	Enabled       bool
	Status        string
	LastSuccessAt *time.Time
	Items         []domain.QuotaSnapshot
	Message       string
	Advice        string
}

type Service struct {
	provider Provider
	store    Repository
	settings Settings
	now      func() time.Time

	mu          sync.Mutex
	current     *refreshCall
	lastAttempt time.Time
}

type refreshCall struct {
	done chan struct{}
	view View
	err  error
}

func NewService(provider Provider, store Repository, settingsStore Settings) *Service {
	return &Service{
		provider: provider,
		store:    store,
		settings: settingsStore,
		now:      time.Now,
	}
}

func (s *Service) Snapshot(ctx context.Context) (View, error) {
	values, err := s.settings.Load()
	if err != nil {
		return View{}, err
	}
	if !values.CodexQuotaConsent {
		return View{
			Status:  "not_configured",
			Items:   []domain.QuotaSnapshot{},
			Message: "Codex subscription quota is not enabled",
			Advice:  "Enable it in Diagnostics when you want Dora to contact ChatGPT",
		}, nil
	}

	state, err := s.store.QuotaProviderState(ctx, domain.CodexSource)
	if err != nil {
		return View{}, err
	}
	items, err := s.store.LatestQuotaSnapshots(ctx, domain.CodexSource)
	if err != nil {
		return View{}, err
	}
	now := s.now().UTC()
	for index := range items {
		if now.Sub(items[index].FetchedAt) > staleAfter {
			items[index].SourceState = "stale"
		}
	}
	view := View{
		Enabled:       true,
		Status:        state.Status,
		LastSuccessAt: state.LastQuotaAt,
		Items:         items,
	}
	if view.Items == nil {
		view.Items = []domain.QuotaSnapshot{}
	}
	switch state.Status {
	case "ready":
		view.Message = "Codex subscription quota is ready"
	case "unsupported":
		view.Message = "API key auth does not expose subscription quota"
		view.Advice = "Run codex login with a ChatGPT subscription account"
	case "not_configured":
		view.Message = "No Codex OAuth login was found"
		view.Advice = "Run codex login"
	case "error":
		view.Message = state.LastError
		view.Advice = quotaAdvice(state.LastError)
	default:
		view.Message = "Codex quota has not been refreshed"
	}
	return view, nil
}

func (s *Service) Refresh(ctx context.Context, force bool) (View, error) {
	values, err := s.settings.Load()
	if err != nil {
		return View{}, err
	}
	if !values.CodexQuotaConsent {
		if err := s.store.SetQuotaStatus(ctx, domain.CodexSource, "not_configured", ""); err != nil {
			return View{}, err
		}
		return s.Snapshot(ctx)
	}

	s.mu.Lock()
	if s.current != nil {
		call := s.current
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return View{}, ctx.Err()
		case <-call.done:
			return call.view, call.err
		}
	}
	now := s.now().UTC()
	if !force && !s.lastAttempt.IsZero() && now.Sub(s.lastAttempt) < refreshInterval {
		s.mu.Unlock()
		return s.Snapshot(ctx)
	}
	call := &refreshCall{done: make(chan struct{})}
	s.current = call
	s.lastAttempt = now
	s.mu.Unlock()

	call.view, call.err = s.refresh(ctx)
	s.mu.Lock()
	s.current = nil
	close(call.done)
	s.mu.Unlock()
	return call.view, call.err
}

func (s *Service) Disable(ctx context.Context) error {
	return s.store.SetQuotaStatus(ctx, domain.CodexSource, "not_configured", "")
}

func (s *Service) refresh(ctx context.Context) (View, error) {
	snapshots, fetchErr := s.provider.Fetch(ctx)
	if fetchErr != nil {
		status := "error"
		message := "Codex quota refresh failed"
		var quotaErr *codex.QuotaError
		if errors.As(fetchErr, &quotaErr) {
			status = quotaErr.State
			message = quotaErr.Message
		}
		if err := s.store.SetQuotaStatus(ctx, domain.CodexSource, status, message); err != nil {
			return View{}, errors.Join(fetchErr, err)
		}
		view, err := s.Snapshot(ctx)
		if err != nil {
			return View{}, errors.Join(fetchErr, err)
		}
		return view, fetchErr
	}
	if err := s.store.SaveQuotaSuccess(ctx, snapshots); err != nil {
		return View{}, err
	}
	return s.Snapshot(ctx)
}

func quotaAdvice(message string) string {
	switch message {
	case "Codex 登录已过期或无权读取配额":
		return "Run codex login again"
	case "无法连接 Codex 配额服务":
		return "Check the network and retry; the last successful quota is preserved"
	default:
		return "Retry later; local token usage remains available"
	}
}
