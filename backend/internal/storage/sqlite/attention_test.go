package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestCodexAttentionTransitionsDeduplicateAndResolve(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	start := attentionEvent("SessionStart", now)
	if created, err := store.ApplyCodexHookEvent(ctx, start); err != nil || created {
		t.Fatalf("SessionStart = created %t, err %v", created, err)
	}

	permission := attentionEvent("PermissionRequest", now.Add(time.Second))
	permission.TurnID = "turn-1"
	permission.ToolName = "Bash"
	permission.EventKey = "codex:permission-1"
	if created, err := store.ApplyCodexHookEvent(ctx, permission); err != nil || !created {
		t.Fatalf("首次 PermissionRequest = created %t, err %v", created, err)
	}
	if created, err := store.ApplyCodexHookEvent(ctx, permission); err != nil || created {
		t.Fatalf("重复 PermissionRequest = created %t, err %v", created, err)
	}

	waiting, err := store.WaitingSessions(ctx)
	if err != nil {
		t.Fatalf("WaitingSessions() 失败: %v", err)
	}
	if len(waiting) != 1 || waiting[0].RequestCount != 1 || waiting[0].Latest.Kind != domain.AttentionDangerousCommand {
		t.Fatalf("等待状态错误: %+v", waiting)
	}
	unnotified, err := store.UnnotifiedAttention(ctx)
	if err != nil || len(unnotified) != 1 {
		t.Fatalf("UnnotifiedAttention() = %+v, %v", unnotified, err)
	}
	if err := store.MarkAttentionNotified(ctx, unnotified[0].ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("MarkAttentionNotified() 失败: %v", err)
	}
	if unnotified, err = store.UnnotifiedAttention(ctx); err != nil || len(unnotified) != 0 {
		t.Fatalf("提醒后仍有未提醒请求: %+v, %v", unnotified, err)
	}

	completed := attentionEvent("PostToolUse", now.Add(3*time.Second))
	completed.TurnID = "turn-1"
	completed.ToolName = "Bash"
	if _, err := store.ApplyCodexHookEvent(ctx, completed); err != nil {
		t.Fatalf("PostToolUse 失败: %v", err)
	}
	if waiting, err = store.WaitingSessions(ctx); err != nil || len(waiting) != 0 {
		t.Fatalf("PostToolUse 未解决等待: %+v, %v", waiting, err)
	}

	// 已解决的同一事件不能重新打开等待状态或重复提醒。
	permission.ReceivedAt = now.Add(4 * time.Second)
	if created, err := store.ApplyCodexHookEvent(ctx, permission); err != nil || created {
		t.Fatalf("重放 PermissionRequest = created %t, err %v", created, err)
	}
	if waiting, err = store.WaitingSessions(ctx); err != nil || len(waiting) != 0 {
		t.Fatalf("重放事件重新打开等待: %+v, %v", waiting, err)
	}

	question := attentionEvent("PreToolUse", now.Add(5*time.Second))
	question.TurnID = "turn-2"
	question.ToolName = "request_user_input"
	question.EventKey = "codex:question-1"
	if created, err := store.ApplyCodexHookEvent(ctx, question); err != nil || !created {
		t.Fatalf("request_user_input = created %t, err %v", created, err)
	}
	if waiting, err = store.WaitingSessions(ctx); err != nil || len(waiting) != 1 || waiting[0].Latest.Kind != domain.AttentionUserQuestion {
		t.Fatalf("回答等待状态错误: %+v, %v", waiting, err)
	}
	stop := attentionEvent("Stop", now.Add(6*time.Second))
	stop.TurnID = "turn-2"
	if _, err := store.ApplyCodexHookEvent(ctx, stop); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if waiting, err = store.WaitingSessions(ctx); err != nil || len(waiting) != 0 {
		t.Fatalf("Stop 未解决等待: %+v, %v", waiting, err)
	}
	session, err := store.RuntimeSession(ctx, 1)
	if err != nil || session.State != domain.RuntimeStateIdle {
		t.Fatalf("Stop 后 session 状态错误: %+v, %v", session, err)
	}
	permission.ReceivedAt = now.Add(7 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, permission); err != nil {
		t.Fatalf("Stop 后重放失败: %v", err)
	}
	session, err = store.RuntimeSession(ctx, 1)
	if err != nil || session.State != domain.RuntimeStateIdle {
		t.Fatalf("已解决事件重放篡改 idle 状态: %+v, %v", session, err)
	}
}

func TestCodexAttentionRestartDoesNotRenotifyHistoricalWaiting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dora.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	event := attentionEvent("PermissionRequest", now)
	event.TurnID = "turn-restart"
	event.ToolName = "apply_patch"
	event.EventKey = "codex:restart"
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatalf("保存等待事件失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() 失败: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("重开数据库失败: %v", err)
	}
	defer store.Close()
	cutoff := now.Add(time.Minute)
	newEvent := attentionEvent("PermissionRequest", cutoff)
	newEvent.TurnID = "turn-new"
	newEvent.ToolName = "apply_patch"
	newEvent.EventKey = "codex:new"
	if _, err := store.ApplyCodexHookEvent(ctx, newEvent); err != nil {
		t.Fatalf("保存启动边界后的请求失败: %v", err)
	}
	if err := store.MarkHistoricalAttentionNotified(ctx, cutoff, cutoff.Add(time.Second)); err != nil {
		t.Fatalf("MarkHistoricalAttentionNotified() 失败: %v", err)
	}
	if requests, err := store.UnnotifiedAttention(ctx); err != nil || len(requests) != 1 || requests[0].EventKey != "codex:new" {
		t.Fatalf("启动 cutoff 错误标记了新请求: %+v, %v", requests, err)
	}
	if waiting, err := store.WaitingSessions(ctx); err != nil || len(waiting) != 1 || waiting[0].RequestCount != 2 {
		t.Fatalf("重启后等待状态未保留: %+v, %v", waiting, err)
	}

	end := attentionEvent("SessionEnd", now.Add(2*time.Minute))
	if _, err := store.ApplyCodexHookEvent(ctx, end); err != nil {
		t.Fatalf("SessionEnd 失败: %v", err)
	}
	if _, err := store.RuntimeSession(ctx, 1); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SessionEnd 未移除 runtime session: %v", err)
	}
}

func TestWaitingSessionUsesOldestActiveRequestTime(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 9, 30, 0, 0, time.UTC)
	for index, key := range []string{"codex:first", "codex:second"} {
		event := attentionEvent("PermissionRequest", now.Add(time.Duration(index)*time.Minute))
		event.TurnID = "turn-multiple"
		event.ToolName = "apply_patch"
		event.EventKey = key
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatalf("保存等待事件 %d 失败: %v", index, err)
		}
	}
	waiting, err := store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("WaitingSessions() = %+v, %v", waiting, err)
	}
	if !waiting[0].WaitingSince.Equal(now) || waiting[0].Latest.EventKey != "codex:second" || waiting[0].RequestCount != 2 {
		t.Fatalf("多请求等待聚合错误: %+v", waiting[0])
	}
}

func attentionEvent(name string, at time.Time) domain.CodexHookEvent {
	return domain.CodexHookEvent{
		ExternalSessionID: "019-test-session",
		EventName:         name,
		CWDBasename:       "dora",
		Model:             "gpt-test",
		Surface:           domain.CodexSurfaceCLI,
		TerminalKind:      domain.TerminalITerm2,
		TTY:               "/dev/ttys001",
		ReceivedAt:        at,
	}
}
