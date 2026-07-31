package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func (s *Store) SaveQuotaSuccess(ctx context.Context, snapshots []domain.QuotaSnapshot) error {
	if len(snapshots) == 0 {
		return errors.New("quota 快照不能为空")
	}
	fetchedAt := snapshots[0].FetchedAt.UTC()
	provider := snapshots[0].Provider
	windows := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if err := validateQuotaSnapshot(snapshot, fetchedAt); err != nil {
			return err
		}
		if snapshot.Provider != provider {
			return errors.New("同批 quota 必须来自同一 provider")
		}
		if _, exists := windows[snapshot.WindowKey]; exists {
			return errors.New("同批 quota 窗口不能重复")
		}
		windows[snapshot.WindowKey] = struct{}{}
	}

	return s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		for _, snapshot := range snapshots {
			var resetsAt any
			if snapshot.ResetsAt != nil {
				resetsAt = snapshot.ResetsAt.UTC().UnixMilli()
			}
			if _, err := conn.ExecContext(ctx, `
				INSERT INTO quota_snapshots (
					provider, window_key, label, used_percent, remaining_percent,
					resets_at_ms, fetched_at_ms, source, source_state, plan, account_label
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(provider, window_key, fetched_at_ms) DO UPDATE SET
					label = excluded.label,
					used_percent = excluded.used_percent,
					remaining_percent = excluded.remaining_percent,
					resets_at_ms = excluded.resets_at_ms,
					source = excluded.source,
					source_state = excluded.source_state,
					plan = excluded.plan,
					account_label = excluded.account_label
			`,
				snapshot.Provider,
				snapshot.WindowKey,
				snapshot.Label,
				snapshot.UsedPercent,
				snapshot.RemainingPercent,
				resetsAt,
				fetchedAt.UnixMilli(),
				snapshot.Source,
				snapshot.SourceState,
				snapshot.Plan,
				snapshot.AccountLabel,
			); err != nil {
				return fmt.Errorf("保存 quota 快照: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO provider_state (
				provider, quota_status, last_quota_at_ms, last_quota_error
			) VALUES (?, 'ready', ?, '')
			ON CONFLICT(provider) DO UPDATE SET
				quota_status = excluded.quota_status,
				last_quota_at_ms = excluded.last_quota_at_ms,
				last_quota_error = excluded.last_quota_error
		`, snapshots[0].Provider, fetchedAt.UnixMilli()); err != nil {
			return fmt.Errorf("更新 quota provider 状态: %w", err)
		}
		return nil
	})
}

func (s *Store) SetQuotaStatus(ctx context.Context, provider, status, message string) error {
	if provider == "" || status == "" {
		return errors.New("quota provider 和状态不能为空")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO provider_state (
			provider, quota_status, last_quota_error
		) VALUES (?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			quota_status = excluded.quota_status,
			last_quota_error = excluded.last_quota_error
	`, provider, status, message); err != nil {
		return fmt.Errorf("更新 quota 状态: %w", err)
	}
	return nil
}

func (s *Store) LatestQuotaSnapshots(ctx context.Context, provider string) ([]domain.QuotaSnapshot, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT
			provider, window_key, label, used_percent, remaining_percent,
			resets_at_ms, fetched_at_ms, source, source_state, plan, account_label
		FROM quota_snapshots
		WHERE provider = ?
		  AND fetched_at_ms = (
			SELECT MAX(fetched_at_ms) FROM quota_snapshots WHERE provider = ?
		  )
		ORDER BY window_key
	`, provider, provider)
	if err != nil {
		return nil, fmt.Errorf("读取 quota 快照: %w", err)
	}
	defer rows.Close()

	var result []domain.QuotaSnapshot
	for rows.Next() {
		var snapshot domain.QuotaSnapshot
		var resetsAt sql.NullInt64
		var fetchedAt int64
		if err := rows.Scan(
			&snapshot.Provider,
			&snapshot.WindowKey,
			&snapshot.Label,
			&snapshot.UsedPercent,
			&snapshot.RemainingPercent,
			&resetsAt,
			&fetchedAt,
			&snapshot.Source,
			&snapshot.SourceState,
			&snapshot.Plan,
			&snapshot.AccountLabel,
		); err != nil {
			return nil, fmt.Errorf("解析 quota 快照: %w", err)
		}
		snapshot.FetchedAt = time.UnixMilli(fetchedAt).UTC()
		if resetsAt.Valid {
			value := time.UnixMilli(resetsAt.Int64).UTC()
			snapshot.ResetsAt = &value
		}
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

func (s *Store) QuotaProviderState(ctx context.Context, provider string) (domain.QuotaProviderState, error) {
	var result domain.QuotaProviderState
	var lastQuota sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `
		SELECT quota_status, last_quota_at_ms, last_quota_error
		FROM provider_state
		WHERE provider = ?
	`, provider).Scan(&result.Status, &lastQuota, &result.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.QuotaProviderState{Status: "not_configured"}, nil
	}
	if err != nil {
		return domain.QuotaProviderState{}, fmt.Errorf("读取 quota provider 状态: %w", err)
	}
	if lastQuota.Valid {
		value := time.UnixMilli(lastQuota.Int64).UTC()
		result.LastQuotaAt = &value
	}
	return result, nil
}

func validateQuotaSnapshot(snapshot domain.QuotaSnapshot, fetchedAt time.Time) error {
	if snapshot.Provider == "" || snapshot.WindowKey == "" || snapshot.Label == "" {
		return errors.New("quota 标识不能为空")
	}
	if snapshot.FetchedAt.IsZero() || !snapshot.FetchedAt.Equal(fetchedAt) {
		return errors.New("同批 quota 必须使用同一获取时间")
	}
	for _, value := range []float64{snapshot.UsedPercent, snapshot.RemainingPercent} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return errors.New("quota 百分比必须在 0 到 100 之间")
		}
	}
	if math.Abs(snapshot.UsedPercent+snapshot.RemainingPercent-100) > 0.001 {
		return errors.New("quota 已用和剩余百分比必须合计为 100")
	}
	if snapshot.SourceState != "confirmed" {
		return errors.New("只有确认成功的 quota 才能保存")
	}
	return nil
}
