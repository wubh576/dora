package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	AttentionRequestMissing  = "missing"
	AttentionRequestActive   = "active"
	AttentionRequestResolved = "resolved"
)

func (s *Store) ApplyCodexHookEvent(ctx context.Context, event domain.CodexHookEvent) (bool, error) {
	if err := validateCodexHookEvent(event); err != nil {
		return false, err
	}

	created := false
	err := s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if event.SubagentScope != "" {
			var err error
			created, err = applySubagentHookEvent(ctx, conn, event)
			return err
		}
		if event.EventName == "SessionEnd" {
			return endRuntimeSession(ctx, conn, event)
		}
		if waitingEvent(event) {
			exists, resolved, err := attentionRequestState(ctx, conn, event.EventKey)
			if err != nil {
				return err
			}
			if exists && resolved {
				return nil
			}
		}

		sessionID, err := upsertRuntimeSession(ctx, conn, event)
		if err != nil {
			return err
		}

		switch event.EventName {
		case "PermissionRequest":
			kind, summary := domain.AttentionPermission, "Codex 等待授权"
			if event.ToolName == "Bash" {
				kind, summary = domain.AttentionDangerousCommand, "命令等待授权"
			}
			created, err = createAttentionRequest(ctx, conn, sessionID, event, kind, summary)
			if err != nil {
				return err
			}
			return markWaitingIfActive(ctx, conn, sessionID, event.EventKey, event.ReceivedAt, true)
		case "PreToolUse":
			if event.ToolName != "request_user_input" {
				return nil
			}
			created, err = createAttentionRequest(
				ctx, conn, sessionID, event, domain.AttentionUserQuestion, "Codex 等待回答",
			)
			if err != nil {
				return err
			}
			return markWaitingIfActive(ctx, conn, sessionID, event.EventKey, event.ReceivedAt, true)
		case "SessionStart":
			if event.SessionStartSource == "compact" {
				return nil
			}
			return resolveSessionRequests(ctx, conn, sessionID, event.ReceivedAt, "session_started", domain.RuntimeStateIdle)
		case "UserPromptSubmit":
			return resolveSessionRequests(ctx, conn, sessionID, event.ReceivedAt, "new_prompt", domain.RuntimeStateRunning)
		case "PostToolUse":
			return resolveCompletedToolRequests(ctx, conn, sessionID, event)
		case "Stop":
			return resolveSessionRequests(ctx, conn, sessionID, event.ReceivedAt, "turn_stopped", domain.RuntimeStateIdle)
		default:
			return nil
		}
	})
	if err != nil {
		return false, fmt.Errorf("保存 Codex 实时事件: %w", err)
	}
	return created, nil
}

func applySubagentHookEvent(ctx context.Context, conn *sql.Conn, event domain.CodexHookEvent) (bool, error) {
	switch event.EventName {
	case "PermissionRequest":
		exists, resolved, err := attentionRequestState(ctx, conn, event.EventKey)
		if err != nil || exists && resolved {
			return false, err
		}
		sessionID, err := ensureSubagentParentSession(ctx, conn, event)
		if err != nil {
			return false, err
		}
		kind := domain.AttentionPermission
		if event.ToolName == "Bash" {
			kind = domain.AttentionDangerousCommand
		}
		created, err := createAttentionRequest(
			ctx, conn, sessionID, event, kind, "Subagent 等待授权",
		)
		if err != nil {
			return false, err
		}
		return created, markWaitingIfActive(
			ctx, conn, sessionID, event.EventKey, event.ReceivedAt, false,
		)
	case "PostToolUse":
		sessionID, found, err := runtimeSessionID(ctx, conn, event.ExternalSessionID)
		if err != nil || !found {
			return false, err
		}
		return false, resolveScopedToolRequest(ctx, conn, sessionID, event)
	case "Stop", "SubagentStop":
		sessionID, found, err := runtimeSessionID(ctx, conn, event.ExternalSessionID)
		if err != nil || !found {
			return false, err
		}
		return false, resolveSubagentRequests(ctx, conn, sessionID, event)
	default:
		return false, nil
	}
}

func (s *Store) RuntimeSessions(ctx context.Context) ([]domain.ActiveSession, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		WITH active AS (
			SELECT runtime_session_id, MIN(created_at_ms) AS waiting_since,
				COUNT(id) AS request_count
			FROM attention_requests
			WHERE resolved_at_ms IS NULL
			GROUP BY runtime_session_id
		)
		SELECT
			s.id, s.provider, s.external_session_id, s.cwd_basename, s.session_name, s.model,
			s.surface, s.terminal_kind, s.tty, s.state, s.base_state,
			s.prompt_preview, s.last_seen_at_ms,
			r.id, r.event_key, r.kind, r.summary, r.turn_id, r.created_at_ms,
			r.notified_at_ms, active.waiting_since, active.request_count
		FROM runtime_sessions s
		LEFT JOIN active ON active.runtime_session_id = s.id
		LEFT JOIN attention_requests r ON r.id = (
			SELECT latest.id
			FROM attention_requests latest
			WHERE latest.runtime_session_id = s.id AND latest.resolved_at_ms IS NULL
			ORDER BY latest.created_at_ms DESC, latest.id DESC
			LIMIT 1
		)
		WHERE s.state IN (?, ?)
		ORDER BY CASE s.state WHEN ? THEN 0 ELSE 1 END,
			CASE WHEN s.state = ? THEN active.waiting_since END ASC,
			CASE WHEN s.state = ? THEN s.last_seen_at_ms END DESC,
			s.id ASC
	`, domain.RuntimeStateWaiting, domain.RuntimeStateRunning,
		domain.RuntimeStateWaiting, domain.RuntimeStateWaiting, domain.RuntimeStateRunning)
	if err != nil {
		return nil, fmt.Errorf("读取 Codex 运行态 session: %w", err)
	}
	defer rows.Close()

	result := make([]domain.ActiveSession, 0)
	for rows.Next() {
		var item domain.ActiveSession
		var sessionSeen int64
		var requestID, requestCreated, waitingSince, requestCount sql.NullInt64
		var eventKey, kind, summary, turnID sql.NullString
		var notified sql.NullInt64
		if err := rows.Scan(
			&item.Session.ID,
			&item.Session.Provider,
			&item.Session.ExternalSessionID,
			&item.Session.CWDBasename,
			&item.Session.SessionName,
			&item.Session.Model,
			&item.Session.Surface,
			&item.Session.TerminalKind,
			&item.Session.TTY,
			&item.Session.State,
			&item.Session.BaseState,
			&item.Session.PromptPreview,
			&sessionSeen,
			&requestID,
			&eventKey,
			&kind,
			&summary,
			&turnID,
			&requestCreated,
			&notified,
			&waitingSince,
			&requestCount,
		); err != nil {
			return nil, fmt.Errorf("解析 Codex 运行态 session: %w", err)
		}
		item.Session.LastSeenAt = time.UnixMilli(sessionSeen).UTC()
		if requestID.Valid {
			latest := &domain.AttentionRequest{
				ID: requestID.Int64, RuntimeSessionID: item.Session.ID,
				EventKey: eventKey.String, Kind: kind.String, Summary: summary.String,
				TurnID: turnID.String, CreatedAt: time.UnixMilli(requestCreated.Int64).UTC(),
			}
			if notified.Valid {
				value := time.UnixMilli(notified.Int64).UTC()
				latest.NotifiedAt = &value
			}
			item.Latest = latest
			item.WaitingSince = time.UnixMilli(waitingSince.Int64).UTC()
			item.RequestCount = int(requestCount.Int64)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Codex 运行态 session: %w", err)
	}
	return result, nil
}

func (s *Store) WaitingSessions(ctx context.Context) ([]domain.WaitingSession, error) {
	active, err := s.RuntimeSessions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.WaitingSession, 0, len(active))
	for _, item := range active {
		if item.Session.State != domain.RuntimeStateWaiting || item.Latest == nil {
			continue
		}
		result = append(result, domain.WaitingSession{
			Session: item.Session, Latest: *item.Latest,
			WaitingSince: item.WaitingSince, RequestCount: item.RequestCount,
		})
	}
	return result, nil
}

func (s *Store) UnnotifiedAttention(ctx context.Context) ([]domain.AttentionRequest, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT id, runtime_session_id, event_key, kind, summary, turn_id, created_at_ms
		FROM attention_requests
		WHERE resolved_at_ms IS NULL AND notified_at_ms IS NULL
		ORDER BY created_at_ms ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("读取未提醒的 Codex 请求: %w", err)
	}
	defer rows.Close()

	result := make([]domain.AttentionRequest, 0)
	for rows.Next() {
		var request domain.AttentionRequest
		var createdAt int64
		if err := rows.Scan(
			&request.ID,
			&request.RuntimeSessionID,
			&request.EventKey,
			&request.Kind,
			&request.Summary,
			&request.TurnID,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("解析未提醒的 Codex 请求: %w", err)
		}
		request.CreatedAt = time.UnixMilli(createdAt).UTC()
		result = append(result, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历未提醒的 Codex 请求: %w", err)
	}
	return result, nil
}

func (s *Store) MarkHistoricalAttentionNotified(ctx context.Context, cutoff, at time.Time) error {
	if cutoff.IsZero() || at.IsZero() {
		return errors.New("Codex 历史提醒时间无效")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE attention_requests
		SET notified_at_ms = ?
		WHERE resolved_at_ms IS NULL AND notified_at_ms IS NULL AND created_at_ms < ?
	`, at.UTC().UnixMilli(), cutoff.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("标记既有 Codex 等待请求: %w", err)
	}
	return nil
}

func (s *Store) MarkAttentionNotified(ctx context.Context, id int64, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE attention_requests
		SET notified_at_ms = ?
		WHERE id = ? AND resolved_at_ms IS NULL AND notified_at_ms IS NULL
	`, at.UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("记录 Codex 请求提醒: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("检查 Codex 请求提醒结果: %w", err)
	} else if affected > 1 {
		return errors.New("Codex 请求提醒更新了多条记录")
	}
	return nil
}

func (s *Store) ClaimUnnotifiedAttention(ctx context.Context, at time.Time) ([]domain.AttentionRequest, error) {
	if at.IsZero() {
		return nil, errors.New("Codex 提醒 claim 时间无效")
	}
	claimed := make([]domain.AttentionRequest, 0)
	err := s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `
			SELECT id, runtime_session_id, event_key, kind, summary, turn_id, created_at_ms
			FROM attention_requests
			WHERE resolved_at_ms IS NULL AND notified_at_ms IS NULL
			ORDER BY created_at_ms ASC, id ASC
		`)
		if err != nil {
			return fmt.Errorf("读取待 claim 的 Codex 请求: %w", err)
		}
		for rows.Next() {
			var request domain.AttentionRequest
			var createdAt int64
			if err := rows.Scan(
				&request.ID, &request.RuntimeSessionID, &request.EventKey,
				&request.Kind, &request.Summary, &request.TurnID, &createdAt,
			); err != nil {
				rows.Close()
				return fmt.Errorf("解析待 claim 的 Codex 请求: %w", err)
			}
			request.CreatedAt = time.UnixMilli(createdAt).UTC()
			claimed = append(claimed, request)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("关闭 Codex claim 查询: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("遍历待 claim 的 Codex 请求: %w", err)
		}
		for _, request := range claimed {
			result, err := conn.ExecContext(ctx, `
				UPDATE attention_requests SET notified_at_ms = ?
				WHERE id = ? AND resolved_at_ms IS NULL AND notified_at_ms IS NULL
			`, at.UTC().UnixMilli(), request.ID)
			if err != nil {
				return fmt.Errorf("claim Codex 请求提醒: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return fmt.Errorf("检查 Codex 请求 claim 结果: affected=%d, err=%v", affected, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) RuntimeSession(ctx context.Context, id int64) (domain.RuntimeSession, error) {
	var session domain.RuntimeSession
	var lastSeen int64
	err := s.readDB.QueryRowContext(ctx, `
		SELECT id, provider, external_session_id, cwd_basename, session_name, model,
			surface, terminal_kind, tty, state, base_state, prompt_preview, last_seen_at_ms
		FROM runtime_sessions
		WHERE id = ?
	`, id).Scan(
		&session.ID,
		&session.Provider,
		&session.ExternalSessionID,
		&session.CWDBasename,
		&session.SessionName,
		&session.Model,
		&session.Surface,
		&session.TerminalKind,
		&session.TTY,
		&session.State,
		&session.BaseState,
		&session.PromptPreview,
		&lastSeen,
	)
	if err != nil {
		return domain.RuntimeSession{}, fmt.Errorf("读取 Codex runtime session: %w", err)
	}
	session.LastSeenAt = time.UnixMilli(lastSeen).UTC()
	return session, nil
}

func (s *Store) UpdateRuntimeSessionNames(ctx context.Context, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	err := s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		for externalSessionID, name := range names {
			if externalSessionID == "" || name == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, `
				UPDATE runtime_sessions SET session_name = ?
				WHERE provider = ? AND external_session_id = ? AND session_name != ?
			`, name, domain.CodexSource, externalSessionID, name); err != nil {
				return fmt.Errorf("更新 Codex runtime session 名称: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("保存 Codex runtime session 名称: %w", err)
	}
	return nil
}

func (s *Store) RuntimeSessionState(ctx context.Context, externalSessionID string) (string, error) {
	if externalSessionID == "" {
		return "", errors.New("Codex external session ID 为空")
	}
	var state string
	err := s.readDB.QueryRowContext(ctx, `
		SELECT state FROM runtime_sessions WHERE provider = ? AND external_session_id = ?
	`, domain.CodexSource, externalSessionID).Scan(&state)
	if err != nil {
		return "", err
	}
	return state, nil
}

func (s *Store) AttentionRequestStatus(ctx context.Context, eventKey string) (string, error) {
	if eventKey == "" {
		return "", errors.New("Codex event key 为空")
	}
	var resolvedAt sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `
		SELECT resolved_at_ms FROM attention_requests WHERE event_key = ?
	`, eventKey).Scan(&resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AttentionRequestMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 Codex 等待请求状态: %w", err)
	}
	if resolvedAt.Valid {
		return AttentionRequestResolved, nil
	}
	return AttentionRequestActive, nil
}

func (s *Store) ResolveRuntimeSession(ctx context.Context, id int64, at time.Time, reason string) error {
	if id <= 0 || reason == "" || at.IsZero() {
		return errors.New("Codex runtime session 解决参数无效")
	}
	return s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `
			UPDATE attention_requests
			SET resolved_at_ms = ?, resolution_reason = ?
			WHERE runtime_session_id = ? AND resolved_at_ms IS NULL
		`, at.UTC().UnixMilli(), reason, id); err != nil {
			return fmt.Errorf("解决 Codex 等待请求: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM runtime_sessions WHERE id = ?", id); err != nil {
			return fmt.Errorf("移除 Codex runtime session: %w", err)
		}
		return nil
	})
}

func (s *Store) ResolveStaleRuntimeSessions(ctx context.Context, cutoff, at time.Time) (int64, error) {
	if cutoff.IsZero() || at.IsZero() || !cutoff.Before(at) {
		return 0, errors.New("Codex stale session 清理时间无效")
	}
	var resolved int64
	err := s.withImmediateTransaction(ctx, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `
			UPDATE attention_requests
			SET resolved_at_ms = ?, resolution_reason = 'stale_session'
			WHERE resolved_at_ms IS NULL AND runtime_session_id IN (
				SELECT id FROM runtime_sessions WHERE last_seen_at_ms < ?
			)
		`, at.UTC().UnixMilli(), cutoff.UTC().UnixMilli()); err != nil {
			return fmt.Errorf("解决过期 Codex 等待请求: %w", err)
		}
		result, err := conn.ExecContext(ctx, `
			DELETE FROM runtime_sessions WHERE last_seen_at_ms < ?
		`, cutoff.UTC().UnixMilli())
		if err != nil {
			return fmt.Errorf("移除过期 Codex runtime session: %w", err)
		}
		resolved, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("检查过期 Codex session 清理结果: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return resolved, nil
}

// RestoreRunningSessions 把上一次进程遗留的瞬时 running 恢复为 idle；未解决的 waiting 必须保留。
func (s *Store) RestoreRunningSessions(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE runtime_sessions
		SET state = ?, base_state = ?, prompt_preview = ''
		WHERE state = ?
	`, domain.RuntimeStateIdle, domain.RuntimeStateIdle, domain.RuntimeStateRunning)
	if err != nil {
		return 0, fmt.Errorf("恢复 Codex 历史运行态: %w", err)
	}
	restored, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("检查 Codex 历史运行态恢复结果: %w", err)
	}
	return restored, nil
}

func validateCodexHookEvent(event domain.CodexHookEvent) error {
	if event.ExternalSessionID == "" || event.ReceivedAt.IsZero() {
		return errors.New("Codex Hook 事件缺少 session 或时间")
	}
	switch event.EventName {
	case "SessionStart", "SessionEnd", "UserPromptSubmit", "PermissionRequest", "PreToolUse", "PostToolUse", "Stop", "SubagentStop":
	default:
		return fmt.Errorf("不支持的 Codex Hook 事件 %q", event.EventName)
	}
	if event.EventName == "SubagentStop" && event.SubagentScope == "" {
		return errors.New("SubagentStop 缺少 child 范围")
	}
	if event.Surface != domain.CodexSurfaceApp && event.Surface != domain.CodexSurfaceCLI && event.Surface != domain.CodexSurfaceUnknown {
		return fmt.Errorf("不支持的 Codex surface %q", event.Surface)
	}
	if event.TerminalKind != domain.TerminalITerm2 && event.TerminalKind != domain.TerminalTerminal && event.TerminalKind != domain.TerminalUnknown {
		return fmt.Errorf("不支持的 terminal kind %q", event.TerminalKind)
	}
	if event.EventName == "PreToolUse" && event.ToolName != "request_user_input" {
		return errors.New("仅接收 request_user_input 的 PreToolUse")
	}
	if event.SubagentScope != "" && event.EventName == "PreToolUse" {
		return errors.New("不接收 Subagent PreToolUse")
	}
	if (event.EventName == "PermissionRequest" || (event.EventName == "PreToolUse" && event.ToolName == "request_user_input")) && event.EventKey == "" {
		return errors.New("Codex 等待事件缺少稳定 key")
	}
	return nil
}

func waitingEvent(event domain.CodexHookEvent) bool {
	return event.EventName == "PermissionRequest" || (event.EventName == "PreToolUse" && event.ToolName == "request_user_input")
}

func attentionRequestState(ctx context.Context, conn *sql.Conn, eventKey string) (exists, resolved bool, err error) {
	var resolvedAt sql.NullInt64
	err = conn.QueryRowContext(ctx, `
		SELECT resolved_at_ms FROM attention_requests WHERE event_key = ?
	`, eventKey).Scan(&resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("读取 Codex 等待请求状态: %w", err)
	}
	return true, resolvedAt.Valid, nil
}

func runtimeSessionID(ctx context.Context, conn *sql.Conn, externalSessionID string) (int64, bool, error) {
	var id int64
	err := conn.QueryRowContext(ctx, `
		SELECT id FROM runtime_sessions WHERE provider = ? AND external_session_id = ?
	`, domain.CodexSource, externalSessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("读取 Codex runtime session ID: %w", err)
	}
	return id, true, nil
}

func ensureSubagentParentSession(ctx context.Context, conn *sql.Conn, event domain.CodexHookEvent) (int64, error) {
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO runtime_sessions (
			provider, external_session_id, cwd_basename, model,
			surface, terminal_kind, tty, state, base_state,
			prompt_preview, last_seen_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT(provider, external_session_id) DO UPDATE SET
			last_seen_at_ms = excluded.last_seen_at_ms
	`,
		domain.CodexSource, event.ExternalSessionID, event.CWDBasename, event.Model,
		event.Surface, event.TerminalKind, event.TTY,
		domain.RuntimeStateWaiting, domain.RuntimeStateRunning,
		event.ReceivedAt.UTC().UnixMilli(),
	); err != nil {
		return 0, fmt.Errorf("建立 Subagent 父 runtime session: %w", err)
	}
	id, _, err := runtimeSessionID(ctx, conn, event.ExternalSessionID)
	return id, err
}

func upsertRuntimeSession(ctx context.Context, conn *sql.Conn, event domain.CodexHookEvent) (int64, error) {
	state := domain.RuntimeStateIdle
	baseState := domain.RuntimeStateIdle
	promptPreview := ""
	runtimeTransition := event.EventName
	if event.EventName == "SessionStart" && event.SessionStartSource == "compact" {
		// Compaction 发生在 turn 内部，只刷新定位元数据和最近活动时间。
		runtimeTransition = ""
	}
	if event.EventName == "UserPromptSubmit" {
		state = domain.RuntimeStateRunning
		baseState = domain.RuntimeStateRunning
		promptPreview = event.PromptPreview
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO runtime_sessions (
			provider, external_session_id, cwd_basename, model,
			surface, terminal_kind, tty, state, base_state, prompt_preview, last_seen_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, external_session_id) DO UPDATE SET
			cwd_basename = CASE WHEN excluded.cwd_basename != '' THEN excluded.cwd_basename ELSE runtime_sessions.cwd_basename END,
			model = CASE WHEN excluded.model != '' THEN excluded.model ELSE runtime_sessions.model END,
			surface = CASE WHEN excluded.surface != 'unknown' THEN excluded.surface ELSE runtime_sessions.surface END,
			terminal_kind = CASE WHEN excluded.terminal_kind != '' THEN excluded.terminal_kind ELSE runtime_sessions.terminal_kind END,
			tty = CASE WHEN excluded.tty != '' THEN excluded.tty ELSE runtime_sessions.tty END,
				prompt_preview = CASE
					WHEN ? = 'UserPromptSubmit' THEN excluded.prompt_preview
					WHEN ? IN ('Stop', 'SessionStart') THEN ''
					ELSE runtime_sessions.prompt_preview
				END,
				state = CASE
					WHEN ? IN ('UserPromptSubmit', 'Stop', 'SessionStart') THEN excluded.state
					ELSE runtime_sessions.state
				END,
				base_state = CASE
					WHEN ? IN ('UserPromptSubmit', 'Stop', 'SessionStart') THEN excluded.base_state
					ELSE runtime_sessions.base_state
				END,
				last_seen_at_ms = excluded.last_seen_at_ms
	`,
		domain.CodexSource,
		event.ExternalSessionID,
		event.CWDBasename,
		event.Model,
		event.Surface,
		event.TerminalKind,
		event.TTY,
		state,
		baseState,
		promptPreview,
		event.ReceivedAt.UTC().UnixMilli(),
		runtimeTransition,
		runtimeTransition,
		runtimeTransition,
		runtimeTransition,
	); err != nil {
		return 0, fmt.Errorf("更新 Codex runtime session: %w", err)
	}
	var id int64
	if err := conn.QueryRowContext(ctx, `
		SELECT id FROM runtime_sessions WHERE provider = ? AND external_session_id = ?
	`, domain.CodexSource, event.ExternalSessionID).Scan(&id); err != nil {
		return 0, fmt.Errorf("读取 Codex runtime session ID: %w", err)
	}
	return id, nil
}

func createAttentionRequest(
	ctx context.Context,
	conn *sql.Conn,
	sessionID int64,
	event domain.CodexHookEvent,
	kind string,
	summary string,
) (bool, error) {
	result, err := conn.ExecContext(ctx, `
		INSERT INTO attention_requests (
			runtime_session_id, event_key, kind, summary, turn_id,
			subagent_scope, tool_use_key, tool_name, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_key) DO NOTHING
	`,
		sessionID, event.EventKey, kind, summary, event.TurnID,
		event.SubagentScope, event.ToolUseKey, event.ToolName,
		event.ReceivedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return false, fmt.Errorf("创建 Codex 等待请求: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("检查 Codex 等待请求结果: %w", err)
	}
	return affected == 1, nil
}

func markWaitingIfActive(
	ctx context.Context,
	conn *sql.Conn,
	sessionID int64,
	eventKey string,
	at time.Time,
	rootRequest bool,
) error {
	var active bool
	if err := conn.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM attention_requests
			WHERE event_key = ? AND runtime_session_id = ? AND resolved_at_ms IS NULL
		)
	`, eventKey, sessionID).Scan(&active); err != nil {
		return fmt.Errorf("检查 Codex 等待请求状态: %w", err)
	}
	if !active {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE runtime_sessions
		SET state = ?,
			base_state = CASE WHEN ? THEN ? ELSE base_state END,
			last_seen_at_ms = ?
		WHERE id = ?
	`,
		domain.RuntimeStateWaiting, rootRequest, domain.RuntimeStateRunning,
		at.UTC().UnixMilli(), sessionID,
	); err != nil {
		return fmt.Errorf("标记 Codex session 等待中: %w", err)
	}
	return nil
}

func resolveSessionRequests(
	ctx context.Context,
	conn *sql.Conn,
	sessionID int64,
	at time.Time,
	reason string,
	state string,
) error {
	if _, err := conn.ExecContext(ctx, `
		UPDATE attention_requests
		SET resolved_at_ms = ?, resolution_reason = ?
		WHERE runtime_session_id = ? AND resolved_at_ms IS NULL
	`, at.UTC().UnixMilli(), reason, sessionID); err != nil {
		return fmt.Errorf("解决 Codex 等待请求: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE runtime_sessions
		SET state = ?, base_state = ?, last_seen_at_ms = ?
		WHERE id = ?
	`, state, state, at.UTC().UnixMilli(), sessionID); err != nil {
		return fmt.Errorf("更新 Codex runtime session 状态: %w", err)
	}
	return nil
}

func resolveCompletedToolRequests(ctx context.Context, conn *sql.Conn, sessionID int64, event domain.CodexHookEvent) error {
	return resolveToolRequest(ctx, conn, sessionID, event, "")
}

func resolveScopedToolRequest(ctx context.Context, conn *sql.Conn, sessionID int64, event domain.CodexHookEvent) error {
	return resolveToolRequest(ctx, conn, sessionID, event, event.SubagentScope)
}

func resolveToolRequest(
	ctx context.Context,
	conn *sql.Conn,
	sessionID int64,
	event domain.CodexHookEvent,
	scope string,
) error {
	firstKind, secondKind := domain.AttentionPermission, domain.AttentionDangerousCommand
	reason := "tool_completed"
	if event.ToolName == "request_user_input" {
		firstKind, secondKind = domain.AttentionUserQuestion, domain.AttentionUserQuestion
		reason = "question_completed"
	}
	resolved := int64(0)
	if event.ToolUseKey != "" {
		result, err := conn.ExecContext(ctx, `
			UPDATE attention_requests
			SET resolved_at_ms = ?, resolution_reason = ?
			WHERE runtime_session_id = ? AND resolved_at_ms IS NULL
				AND subagent_scope = ? AND tool_use_key = ?
				AND kind IN (?, ?)
		`,
			event.ReceivedAt.UTC().UnixMilli(), reason,
			sessionID, scope, event.ToolUseKey, firstKind, secondKind,
		)
		if err != nil {
			return fmt.Errorf("按工具调用解决 Codex 等待请求: %w", err)
		}
		resolved, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("检查 Codex 工具调用解决结果: %w", err)
		}
	}
	if resolved == 0 {
		if err := resolveToolRequestFallback(
			ctx, conn, sessionID, event, scope, firstKind, secondKind, reason,
		); err != nil {
			return err
		}
	}
	return refreshRuntimeState(ctx, conn, sessionID, event.ReceivedAt)
}

func resolveToolRequestFallback(
	ctx context.Context,
	conn *sql.Conn,
	sessionID int64,
	event domain.CodexHookEvent,
	scope string,
	firstKind string,
	secondKind string,
	reason string,
) error {
	query := `
		UPDATE attention_requests
		SET resolved_at_ms = ?, resolution_reason = ?
		WHERE runtime_session_id = ? AND resolved_at_ms IS NULL
			AND subagent_scope = ?
			AND kind IN (?, ?)
			AND (? = '' OR turn_id = '' OR turn_id = ?)
			AND (? = '' OR tool_name = '' OR tool_name = ?)
	`
	arguments := []any{
		event.ReceivedAt.UTC().UnixMilli(), reason,
		sessionID, scope, firstKind, secondKind,
		event.TurnID, event.TurnID,
		event.ToolName, event.ToolName,
	}
	if event.ToolUseKey != "" {
		// completion 提供了不匹配的 key 时，只能降级匹配没有 key 的旧请求。
		query += " AND tool_use_key = ''"
	}
	if _, err := conn.ExecContext(ctx, query, arguments...); err != nil {
		return fmt.Errorf("按 child 范围解决 Codex 等待请求: %w", err)
	}
	return nil
}

func resolveSubagentRequests(ctx context.Context, conn *sql.Conn, sessionID int64, event domain.CodexHookEvent) error {
	if _, err := conn.ExecContext(ctx, `
		UPDATE attention_requests
		SET resolved_at_ms = ?, resolution_reason = 'subagent_stopped'
		WHERE runtime_session_id = ? AND resolved_at_ms IS NULL AND subagent_scope = ?
	`, event.ReceivedAt.UTC().UnixMilli(), sessionID, event.SubagentScope); err != nil {
		return fmt.Errorf("解决 Subagent 等待请求: %w", err)
	}
	return refreshRuntimeState(ctx, conn, sessionID, event.ReceivedAt)
}

func refreshRuntimeState(ctx context.Context, conn *sql.Conn, sessionID int64, at time.Time) error {
	if _, err := conn.ExecContext(ctx, `
		UPDATE runtime_sessions
		SET state = CASE
				WHEN EXISTS(
					SELECT 1 FROM attention_requests
					WHERE runtime_session_id = ? AND resolved_at_ms IS NULL
				) THEN ?
				ELSE base_state
			END,
			last_seen_at_ms = ?
		WHERE id = ?
	`, sessionID, domain.RuntimeStateWaiting, at.UTC().UnixMilli(), sessionID); err != nil {
		return fmt.Errorf("重算 Codex runtime session 状态: %w", err)
	}
	return nil
}

func endRuntimeSession(ctx context.Context, conn *sql.Conn, event domain.CodexHookEvent) error {
	var sessionID int64
	err := conn.QueryRowContext(ctx, `
		SELECT id FROM runtime_sessions WHERE provider = ? AND external_session_id = ?
	`, domain.CodexSource, event.ExternalSessionID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取结束中的 Codex runtime session: %w", err)
	}
	if err := resolveSessionRequests(ctx, conn, sessionID, event.ReceivedAt, "session_ended", domain.RuntimeStateIdle); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM runtime_sessions WHERE id = ?", sessionID); err != nil {
		return fmt.Errorf("移除已结束的 Codex runtime session: %w", err)
	}
	return nil
}
