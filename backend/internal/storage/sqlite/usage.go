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

func (s *Store) BeginUsageScan(ctx context.Context, runID, mode string, startedAt time.Time) error {
	return s.BeginProviderUsageScan(ctx, domain.CodexSource, runID, mode, startedAt)
}

func (s *Store) BeginProviderUsageScan(ctx context.Context, source, runID, mode string, startedAt time.Time) error {
	if !domain.IsUsageSource(source) {
		return fmt.Errorf("不支持的 usage provider %q", source)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scan_runs (
			run_id, source, mode, started_at_ms, status
		) VALUES (?, ?, ?, ?, 'running')
	`, runID, source, mode, startedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("创建 %s 扫描记录: %w", source, err)
	}
	return nil
}

func (s *Store) UpdateUsageScanMode(ctx context.Context, runID, mode string) error {
	if _, err := s.db.ExecContext(
		ctx,
		"UPDATE scan_runs SET mode = ? WHERE run_id = ?",
		mode,
		runID,
	); err != nil {
		return fmt.Errorf("更新 Codex 扫描模式: %w", err)
	}
	return nil
}

func (s *Store) FailUsageScan(ctx context.Context, runID string, finishedAt time.Time, filesSeen, eventsSeen int, scanErr error) error {
	return s.FailProviderUsageScan(ctx, domain.CodexSource, runID, finishedAt, filesSeen, eventsSeen, scanErr)
}

func (s *Store) FailProviderUsageScan(ctx context.Context, source, runID string, finishedAt time.Time, filesSeen, eventsSeen int, scanErr error) error {
	if !domain.IsUsageSource(source) {
		return fmt.Errorf("不支持的 usage provider %q", source)
	}
	message := scanErr.Error()
	return s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if err := requireRunningScan(ctx, conn, source, runID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE scan_runs
			SET finished_at_ms = ?, status = 'failed', files_seen = ?, events_seen = ?, error_message = ?
			WHERE run_id = ? AND source = ? AND status = 'running'
		`, finishedAt.UTC().UnixMilli(), filesSeen, eventsSeen, message, runID, source); err != nil {
			return fmt.Errorf("记录 %s 扫描失败: %w", source, err)
		}
		if _, err := conn.ExecContext(
			ctx,
			"DELETE FROM usage_events_staging WHERE run_id = ? AND source = ?",
			runID,
			source,
		); err != nil {
			return fmt.Errorf("清理失败扫描 staging: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO provider_state (
				provider, usage_status, last_usage_error
			) VALUES (?, 'error', ?)
			ON CONFLICT(provider) DO UPDATE SET
				usage_status = excluded.usage_status,
				last_usage_error = excluded.last_usage_error
		`, source, message); err != nil {
			return fmt.Errorf("更新 %s provider 状态: %w", source, err)
		}
		return nil
	})
}

func (s *Store) CompleteUsageScan(
	ctx context.Context,
	runID string,
	finishedAt time.Time,
	events []domain.UsageEvent,
	files []domain.SourceFileState,
	filesSeen int,
	eventsSeen int,
	warning string,
) error {
	return s.CompleteProviderUsageScan(
		ctx, domain.CodexSource, runID, finishedAt, events, files, filesSeen, eventsSeen, warning,
	)
}

func (s *Store) CompleteProviderUsageScan(
	ctx context.Context,
	source string,
	runID string,
	finishedAt time.Time,
	events []domain.UsageEvent,
	files []domain.SourceFileState,
	filesSeen int,
	eventsSeen int,
	warning string,
) error {
	status := "ready"
	if warning != "" {
		status = "degraded"
	}
	return s.CompleteProviderUsageScanWithMetrics(
		ctx, source, runID, finishedAt, events, files, filesSeen, eventsSeen, warning,
		domain.UsageScanMetrics{Status: status},
	)
}

func (s *Store) CompleteProviderUsageScanWithMetrics(
	ctx context.Context,
	source string,
	runID string,
	finishedAt time.Time,
	events []domain.UsageEvent,
	files []domain.SourceFileState,
	filesSeen int,
	eventsSeen int,
	warning string,
	metrics domain.UsageScanMetrics,
) error {
	if !domain.IsUsageSource(source) {
		return fmt.Errorf("不支持的 usage provider %q", source)
	}
	if metrics.Status != "ready" && metrics.Status != "not_found" && metrics.Status != "degraded" {
		return fmt.Errorf("无效的 %s usage 状态 %q", source, metrics.Status)
	}
	if metrics.SessionCount < 0 || metrics.ParserVersion < 0 {
		return fmt.Errorf("%s usage 聚合指标无效", source)
	}
	for _, file := range files {
		if err := validateSourceFile(source, file); err != nil {
			return err
		}
	}
	if err := s.stageUsageEvents(ctx, source, runID, events); err != nil {
		return err
	}
	return s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if err := requireRunningScan(ctx, conn, source, runID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM usage_events WHERE source = ?", source); err != nil {
			return fmt.Errorf("清理旧 %s usage: %w", source, err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO usage_events (
				source, dedup_key, occurred_at_ms, model, project,
				input_tokens, output_tokens, cached_input_tokens,
				cache_creation_input_tokens, reasoning_output_tokens,
				reported_total_tokens, total_tokens, rollout_key,
				parent_rollout_key, replay_fingerprint, inherited_replay, updated_at_ms
			)
			SELECT
				source, dedup_key, occurred_at_ms, model, project,
				input_tokens, output_tokens, cached_input_tokens,
				cache_creation_input_tokens, reasoning_output_tokens,
				reported_total_tokens, total_tokens, rollout_key,
				parent_rollout_key, replay_fingerprint, inherited_replay, ?
			FROM usage_events_staging
			WHERE run_id = ? AND source = ?
		`, finishedAt.UTC().UnixMilli(), runID, source); err != nil {
			return fmt.Errorf("切换 %s usage generation: %w", source, err)
		}

		if _, err := conn.ExecContext(ctx, "DELETE FROM source_files WHERE source = ?", source); err != nil {
			return fmt.Errorf("清理旧 %s 文件状态: %w", source, err)
		}
		for _, file := range files {
			var lastSuccess any
			if !file.LastSuccessAt.IsZero() {
				lastSuccess = file.LastSuccessAt.UTC().UnixMilli()
			}
			if _, err := conn.ExecContext(ctx, `
				INSERT INTO source_files (
					source, path, file_identity, size_bytes, mtime_ns,
					parsed_offset, complete_line_end, head_hash, tail_hash,
					parser_version, parser_state_json, last_success_at_ms, last_error
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				file.Source,
				file.Path,
				file.FileIdentity,
				file.SizeBytes,
				file.MtimeNS,
				file.ParsedOffset,
				file.CompleteLineEnd,
				file.HeadHash,
				file.TailHash,
				file.ParserVersion,
				file.ParserStateJSON,
				lastSuccess,
				file.LastError,
			); err != nil {
				return fmt.Errorf("保存 %s 文件状态: %w", source, err)
			}
		}

		if _, err := conn.ExecContext(ctx, `
			UPDATE scan_runs
			SET finished_at_ms = ?, status = 'succeeded', files_seen = ?, events_seen = ?, error_message = ?
			WHERE run_id = ? AND source = ? AND status = 'running'
		`, finishedAt.UTC().UnixMilli(), filesSeen, eventsSeen, warning, runID, source); err != nil {
			return fmt.Errorf("完成 %s 扫描记录: %w", source, err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO provider_state (
				provider, usage_status, last_usage_at_ms, last_usage_error,
				config_found, session_count, parser_version
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(provider) DO UPDATE SET
				usage_status = excluded.usage_status,
				last_usage_at_ms = excluded.last_usage_at_ms,
				last_usage_error = excluded.last_usage_error,
				config_found = excluded.config_found,
				session_count = excluded.session_count,
				parser_version = excluded.parser_version
		`, source, metrics.Status, finishedAt.UTC().UnixMilli(), warning, boolToInteger(metrics.ConfigFound), metrics.SessionCount, metrics.ParserVersion); err != nil {
			return fmt.Errorf("更新 %s provider 状态: %w", source, err)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM usage_events_staging WHERE run_id = ? AND source = ?", runID, source); err != nil {
			return fmt.Errorf("完成 usage staging 清理: %w", err)
		}
		return nil
	})
}

// CompleteProviderUsageScanWithoutReplacement 只更新扫描状态，保留上次成功 generation。
func (s *Store) CompleteProviderUsageScanWithoutReplacement(
	ctx context.Context,
	source string,
	runID string,
	finishedAt time.Time,
	filesSeen int,
	eventsSeen int,
	warning string,
	metrics domain.UsageScanMetrics,
) error {
	if !domain.IsUsageSource(source) {
		return fmt.Errorf("不支持的 usage provider %q", source)
	}
	if metrics.Status != "ready" && metrics.Status != "not_found" && metrics.Status != "degraded" {
		return fmt.Errorf("无效的 %s usage 状态 %q", source, metrics.Status)
	}
	return s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if err := requireRunningScan(ctx, conn, source, runID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE scan_runs
			SET finished_at_ms = ?, status = 'succeeded', files_seen = ?, events_seen = ?, error_message = ?
			WHERE run_id = ? AND source = ? AND status = 'running'
		`, finishedAt.UTC().UnixMilli(), filesSeen, eventsSeen, warning, runID, source); err != nil {
			return fmt.Errorf("完成 %s 扫描记录: %w", source, err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO provider_state (
				provider, usage_status, last_usage_at_ms, last_usage_error,
				config_found, session_count, parser_version
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(provider) DO UPDATE SET
				usage_status = excluded.usage_status,
				last_usage_at_ms = excluded.last_usage_at_ms,
				last_usage_error = excluded.last_usage_error,
				config_found = excluded.config_found,
				session_count = excluded.session_count,
				parser_version = excluded.parser_version
		`, source, metrics.Status, finishedAt.UTC().UnixMilli(), warning, boolToInteger(metrics.ConfigFound), metrics.SessionCount, metrics.ParserVersion); err != nil {
			return fmt.Errorf("更新 %s provider 状态: %w", source, err)
		}
		return nil
	})
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireRunningScan(ctx context.Context, db rowQueryer, source, runID string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM scan_runs
			WHERE run_id = ? AND source = ? AND status = 'running'
		)
	`, runID, source).Scan(&exists); err != nil {
		return fmt.Errorf("校验 %s 扫描记录: %w", source, err)
	}
	if !exists {
		return fmt.Errorf("%s 扫描记录 %q 不存在或状态无效", source, runID)
	}
	return nil
}

func (s *Store) stageUsageEvents(ctx context.Context, source, runID string, events []domain.UsageEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 usage staging: %w", err)
	}
	defer tx.Rollback()
	if err := requireRunningScan(ctx, tx, source, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM usage_events_staging WHERE run_id = ? AND source = ?", runID, source); err != nil {
		return fmt.Errorf("清理 usage staging: %w", err)
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_events_staging (
			run_id, source, dedup_key, occurred_at_ms, model, project,
			input_tokens, output_tokens, cached_input_tokens,
			cache_creation_input_tokens, reasoning_output_tokens,
			reported_total_tokens, total_tokens, rollout_key,
			parent_rollout_key, replay_fingerprint, inherited_replay
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("准备 usage staging: %w", err)
	}
	defer statement.Close()
	for _, event := range events {
		if err := validateUsageEvent(source, event); err != nil {
			return err
		}
		if _, err := statement.ExecContext(
			ctx,
			runID,
			event.Source,
			event.DedupKey,
			event.OccurredAt.UTC().UnixMilli(),
			event.Model,
			event.Project,
			event.InputTokens,
			event.OutputTokens,
			event.CachedInputTokens,
			event.CacheCreationInputTokens,
			event.ReasoningOutputTokens,
			event.ReportedTotalTokens,
			event.TotalTokens,
			event.RolloutKey,
			event.ParentRolloutKey,
			event.ReplayFingerprint,
			boolToInteger(event.InheritedReplay),
		); err != nil {
			return fmt.Errorf("写入 usage staging: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 usage staging: %w", err)
	}
	return nil
}

func (s *Store) LoadSourceFiles(ctx context.Context, source string) ([]domain.SourceFileState, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT
			source, path, file_identity, size_bytes, mtime_ns,
			parsed_offset, complete_line_end, head_hash, tail_hash,
			parser_version, parser_state_json, last_success_at_ms, last_error
		FROM source_files
		WHERE source = ?
		ORDER BY path
	`, source)
	if err != nil {
		return nil, fmt.Errorf("读取 Codex 文件状态: %w", err)
	}
	defer rows.Close()

	var result []domain.SourceFileState
	for rows.Next() {
		var file domain.SourceFileState
		var lastSuccess sql.NullInt64
		if err := rows.Scan(
			&file.Source,
			&file.Path,
			&file.FileIdentity,
			&file.SizeBytes,
			&file.MtimeNS,
			&file.ParsedOffset,
			&file.CompleteLineEnd,
			&file.HeadHash,
			&file.TailHash,
			&file.ParserVersion,
			&file.ParserStateJSON,
			&lastSuccess,
			&file.LastError,
		); err != nil {
			return nil, fmt.Errorf("解析 Codex 文件状态: %w", err)
		}
		if lastSuccess.Valid {
			file.LastSuccessAt = time.UnixMilli(lastSuccess.Int64).UTC()
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

func (s *Store) LoadUsageEvents(ctx context.Context, source string) ([]domain.UsageEvent, error) {
	return s.queryUsageEvents(ctx, `
		SELECT
			source, dedup_key, occurred_at_ms, model, project,
			input_tokens, output_tokens, cached_input_tokens,
			cache_creation_input_tokens, reasoning_output_tokens,
			reported_total_tokens, total_tokens, rollout_key,
			parent_rollout_key, replay_fingerprint, inherited_replay
		FROM usage_events
		WHERE source = ?
		ORDER BY occurred_at_ms, dedup_key
	`, source)
}

func (s *Store) UsageEventsInWindow(ctx context.Context, source string, start, end time.Time) ([]domain.UsageEvent, error) {
	return s.queryUsageEvents(ctx, `
		SELECT
			source, dedup_key, occurred_at_ms, model, project,
			input_tokens, output_tokens, cached_input_tokens,
			cache_creation_input_tokens, reasoning_output_tokens,
			reported_total_tokens, total_tokens, rollout_key,
			parent_rollout_key, replay_fingerprint, inherited_replay
		FROM usage_events
		WHERE source = ? AND occurred_at_ms >= ? AND occurred_at_ms < ?
		ORDER BY occurred_at_ms, dedup_key
	`, source, start.UTC().UnixMilli(), end.UTC().UnixMilli())
}

func (s *Store) AllUsageEventsInWindow(ctx context.Context, start, end time.Time) ([]domain.UsageEvent, error) {
	return s.queryUsageEvents(ctx, `
		SELECT
			source, dedup_key, occurred_at_ms, model, project,
			input_tokens, output_tokens, cached_input_tokens,
			cache_creation_input_tokens, reasoning_output_tokens,
			reported_total_tokens, total_tokens, rollout_key,
			parent_rollout_key, replay_fingerprint, inherited_replay
		FROM usage_events
		WHERE source IN (?, ?) AND occurred_at_ms >= ? AND occurred_at_ms < ?
		ORDER BY occurred_at_ms, dedup_key
	`, domain.CodexSource, domain.ClaudeCodeSource, start.UTC().UnixMilli(), end.UTC().UnixMilli())
}

func (s *Store) queryUsageEvents(ctx context.Context, query string, arguments ...any) ([]domain.UsageEvent, error) {
	rows, err := s.readDB.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("读取 usage: %w", err)
	}
	defer rows.Close()

	var result []domain.UsageEvent
	for rows.Next() {
		var event domain.UsageEvent
		var occurredAtMS int64
		var inherited int
		if err := rows.Scan(
			&event.Source,
			&event.DedupKey,
			&occurredAtMS,
			&event.Model,
			&event.Project,
			&event.InputTokens,
			&event.OutputTokens,
			&event.CachedInputTokens,
			&event.CacheCreationInputTokens,
			&event.ReasoningOutputTokens,
			&event.ReportedTotalTokens,
			&event.TotalTokens,
			&event.RolloutKey,
			&event.ParentRolloutKey,
			&event.ReplayFingerprint,
			&inherited,
		); err != nil {
			return nil, fmt.Errorf("解析 Codex usage: %w", err)
		}
		event.OccurredAt = time.UnixMilli(occurredAtMS).UTC()
		event.InheritedReplay = inherited != 0
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) LastSuccessfulFullScan(ctx context.Context, source string) (*time.Time, error) {
	var value sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `
		SELECT MAX(finished_at_ms)
		FROM scan_runs
		WHERE source = ? AND mode = 'full' AND status = 'succeeded'
	`, source).Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("读取最近全量扫描时间: %w", err)
	}
	if !value.Valid {
		return nil, nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result, nil
}

func (s *Store) UsageProviderState(ctx context.Context, source string) (domain.UsageProviderState, error) {
	var result domain.UsageProviderState
	var lastUsage sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `
		SELECT
			p.usage_status,
			(
				SELECT MAX(finished_at_ms)
				FROM scan_runs
				WHERE source = p.provider AND status = 'succeeded'
			),
			p.last_usage_error,
			COALESCE(r.run_id, ''), COALESCE(r.mode, ''),
			COALESCE(r.files_seen, 0), COALESCE(r.events_seen, 0),
			(SELECT COUNT(*) FROM usage_events WHERE source = p.provider),
			p.config_found, p.session_count, p.parser_version
		FROM provider_state p
		LEFT JOIN scan_runs r ON r.run_id = (
			SELECT run_id
			FROM scan_runs
			WHERE source = p.provider
			ORDER BY started_at_ms DESC
			LIMIT 1
		)
		WHERE p.provider = ?
	`, source).Scan(
		&result.Status,
		&lastUsage,
		&result.LastError,
		&result.LastRunID,
		&result.LastRunMode,
		&result.FilesSeen,
		&result.EventsSeen,
		&result.StoredEvents,
		&result.ConfigFound,
		&result.SessionCount,
		&result.ParserVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UsageProviderState{Status: "not_scanned"}, nil
	}
	if err != nil {
		return domain.UsageProviderState{}, fmt.Errorf("读取 Codex provider 状态: %w", err)
	}
	if lastUsage.Valid {
		value := time.UnixMilli(lastUsage.Int64).UTC()
		result.LastScanAt = &value
	}
	return result, nil
}

func (s *Store) withImmediateTransaction(ctx context.Context, operation func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := operation(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func boolToInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateUsageEvent(source string, event domain.UsageEvent) error {
	if event.Source != source || event.DedupKey == "" || event.OccurredAt.IsZero() || event.Model == "" || event.Project == "" {
		return errors.New("usage event 缺少必要字段")
	}
	values := []int64{
		event.InputTokens,
		event.OutputTokens,
		event.CachedInputTokens,
		event.CacheCreationInputTokens,
		event.ReasoningOutputTokens,
		event.ReportedTotalTokens,
		event.TotalTokens,
	}
	var detail int64
	for index, value := range values {
		if value < 0 {
			return errors.New("usage event token 不能为负数")
		}
		if index < 5 {
			if value > math.MaxInt64-detail {
				return errors.New("usage event token 超出 int64")
			}
			detail += value
		}
	}
	if event.TotalTokens < detail || event.TotalTokens < event.ReportedTotalTokens {
		return errors.New("usage event total 小于 token 明细")
	}
	return nil
}

func validateSourceFile(source string, file domain.SourceFileState) error {
	if file.Source != source || file.Path == "" || file.ParserVersion <= 0 {
		return errors.New("source file 缺少必要字段")
	}
	if file.SizeBytes < 0 ||
		file.ParsedOffset < 0 ||
		file.CompleteLineEnd < 0 ||
		file.ParsedOffset > file.SizeBytes ||
		file.CompleteLineEnd > file.SizeBytes {
		return errors.New("source file checkpoint 无效")
	}
	return nil
}
