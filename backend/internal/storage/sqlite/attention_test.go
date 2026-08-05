package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

func TestRuntimeSessionsCombineRunningAndWaitingWithSafePreview(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 8, 30, 0, 0, time.UTC)
	running := attentionEvent("UserPromptSubmit", now)
	running.ExternalSessionID = "running-session"
	running.PromptPreview = "实现灵动岛"
	if _, err := store.ApplyCodexHookEvent(ctx, running); err != nil {
		t.Fatal(err)
	}
	waiting := attentionEvent("PermissionRequest", now.Add(time.Second))
	waiting.ExternalSessionID = "waiting-session"
	waiting.ToolName, waiting.EventKey = "Bash", "codex:runtime-waiting"
	if _, err := store.ApplyCodexHookEvent(ctx, waiting); err != nil {
		t.Fatal(err)
	}

	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 2 {
		t.Fatalf("RuntimeSessions() = %+v, %v", active, err)
	}
	if active[0].Session.State != domain.RuntimeStateWaiting || active[0].Latest == nil || active[0].Latest.ID <= 0 {
		t.Fatalf("waiting 未排在首位或缺少请求: %+v", active[0])
	}
	if active[1].Session.State != domain.RuntimeStateRunning || active[1].Latest != nil || active[1].Session.PromptPreview != "实现灵动岛" {
		t.Fatalf("running 状态或 preview 错误: %+v", active[1])
	}

	// 新 SessionStart 只注册定位信息，并清除上一次 turn 的瞬时摘要。
	running.EventName = "SessionStart"
	running.PromptPreview = "不应覆盖"
	running.ReceivedAt = now.Add(2 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, running); err != nil {
		t.Fatal(err)
	}
	session, err := store.RuntimeSession(ctx, active[1].Session.ID)
	if err != nil || session.PromptPreview != "" || session.State != domain.RuntimeStateIdle {
		t.Fatalf("SessionStart 未恢复为 idle 或清除 preview: %+v, %v", session, err)
	}
	running.EventName = "Stop"
	running.ReceivedAt = now.Add(3 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, running); err != nil {
		t.Fatal(err)
	}
	session, err = store.RuntimeSession(ctx, active[1].Session.ID)
	if err != nil || session.PromptPreview != "" || session.State != domain.RuntimeStateIdle {
		t.Fatalf("Stop 未清除 runtime preview: %+v, %v", session, err)
	}
}

func TestCompactionPreservesRunningAndWaitingState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 3, 5, 0, 0, 0, time.UTC)
	start := attentionEvent("SessionStart", now)
	start.SessionStartSource = "startup"
	if _, err := store.ApplyCodexHookEvent(ctx, start); err != nil {
		t.Fatal(err)
	}
	running := attentionEvent("UserPromptSubmit", now.Add(time.Second))
	running.PromptPreview = "实现 Goal 7 修复"
	if _, err := store.ApplyCodexHookEvent(ctx, running); err != nil {
		t.Fatal(err)
	}
	compact := attentionEvent("SessionStart", now.Add(2*time.Second))
	compact.SessionStartSource = "compact"
	compact.CWDBasename, compact.Model = "updated-project", "gpt-updated"
	if _, err := store.ApplyCodexHookEvent(ctx, compact); err != nil {
		t.Fatal(err)
	}
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateRunning ||
		active[0].Session.PromptPreview != "实现 Goal 7 修复" || active[0].Session.CWDBasename != "updated-project" ||
		active[0].Session.Model != "gpt-updated" || !active[0].Session.LastSeenAt.Equal(compact.ReceivedAt) {
		t.Fatalf("compact 破坏 running 状态: %+v, %v", active, err)
	}

	waiting := attentionEvent("PermissionRequest", now.Add(3*time.Second))
	waiting.TurnID, waiting.ToolName, waiting.EventKey = "turn-1", "Bash", "codex:compact-waiting"
	if _, err := store.ApplyCodexHookEvent(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	before, err := store.RuntimeSessions(ctx)
	if err != nil || len(before) != 1 || before[0].Latest == nil {
		t.Fatalf("建立 waiting fixture 失败: %+v, %v", before, err)
	}
	if err := store.MarkAttentionNotified(ctx, before[0].Latest.ID, now.Add(3500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	before, err = store.RuntimeSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compact.ReceivedAt = now.Add(4 * time.Second)
	compact.CWDBasename = "compacted-project"
	if _, err := store.ApplyCodexHookEvent(ctx, compact); err != nil {
		t.Fatal(err)
	}
	after, err := store.RuntimeSessions(ctx)
	if err != nil || len(after) != 1 || after[0].Session.State != domain.RuntimeStateWaiting || after[0].Latest == nil ||
		after[0].Session.PromptPreview != "实现 Goal 7 修复" || after[0].RequestCount != before[0].RequestCount ||
		after[0].WaitingSince != before[0].WaitingSince || after[0].Latest.ID != before[0].Latest.ID ||
		after[0].Latest.EventKey != before[0].Latest.EventKey || after[0].Latest.CreatedAt != before[0].Latest.CreatedAt ||
		after[0].Latest.NotifiedAt == nil || !after[0].Session.LastSeenAt.Equal(compact.ReceivedAt) {
		t.Fatalf("compact 破坏 waiting 状态: before=%+v after=%+v err=%v", before, after, err)
	}
	if unnotified, err := store.UnnotifiedAttention(ctx); err != nil || len(unnotified) != 0 {
		t.Fatalf("compact 产生了重复提醒: %+v, %v", unnotified, err)
	}

	first := compact
	first.ExternalSessionID = "first-seen-compact"
	first.ReceivedAt = now.Add(5 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, first); err != nil {
		t.Fatal(err)
	}
	if state, err := store.RuntimeSessionState(ctx, first.ExternalSessionID); err != nil || state != domain.RuntimeStateIdle {
		t.Fatalf("首次 compact 未创建 idle session: %q, %v", state, err)
	}
}

func TestRegularSessionStartSourcesResetPreviousTurn(t *testing.T) {
	for _, source := range []string{"startup", "resume", "clear", "", "future-source"} {
		name := source
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
			running := attentionEvent("UserPromptSubmit", now)
			running.PromptPreview = "上一轮任务"
			waiting := attentionEvent("PermissionRequest", now.Add(time.Second))
			waiting.ToolName, waiting.EventKey = "Bash", "codex:reset-"+name
			for _, event := range []domain.CodexHookEvent{running, waiting} {
				if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			active, err := store.RuntimeSessions(ctx)
			if err != nil || len(active) != 1 {
				t.Fatalf("建立 active fixture 失败: %+v, %v", active, err)
			}
			start := attentionEvent("SessionStart", now.Add(2*time.Second))
			start.SessionStartSource = source
			if _, err := store.ApplyCodexHookEvent(ctx, start); err != nil {
				t.Fatal(err)
			}
			session, err := store.RuntimeSession(ctx, active[0].Session.ID)
			if err != nil || session.State != domain.RuntimeStateIdle || session.PromptPreview != "" {
				t.Fatalf("普通 SessionStart 未 reset: %+v, %v", session, err)
			}
			if status, err := store.AttentionRequestStatus(ctx, waiting.EventKey); err != nil || status != AttentionRequestResolved {
				t.Fatalf("普通 SessionStart 未解决旧 request: %q, %v", status, err)
			}
		})
	}
}

func TestRuntimeSessionNameCacheFollowsSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := attentionEvent("UserPromptSubmit", time.Now().UTC())
	event.ExternalSessionID = "named-session"
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRuntimeSessionNames(ctx, map[string]string{
		"named-session": "用户重命名的任务",
		"missing":       "不应创建 session",
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.SessionName != "用户重命名的任务" {
		t.Fatalf("runtime 标题缓存错误: %+v, %v", active, err)
	}
	event.EventName = "SessionEnd"
	event.ReceivedAt = event.ReceivedAt.Add(time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeSession(ctx, active[0].Session.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SessionEnd 后仍保留标题缓存: %v", err)
	}
}

func TestRuntimeLifecycleOnlyShowsActiveTurns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	event := attentionEvent("SessionStart", now)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if active, err := store.RuntimeSessions(ctx); err != nil || len(active) != 0 {
		t.Fatalf("单独 SessionStart 出现在活跃列表: %+v, %v", active, err)
	}
	if state, err := store.RuntimeSessionState(ctx, event.ExternalSessionID); err != nil || state != domain.RuntimeStateIdle {
		t.Fatalf("SessionStart state = %q, %v", state, err)
	}
	event.EventName, event.ToolName, event.ReceivedAt = "PostToolUse", "Bash", now.Add(500*time.Millisecond)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if active, err := store.RuntimeSessions(ctx); err != nil || len(active) != 0 {
		t.Fatalf("没有进行中 turn 的 PostToolUse 错误恢复 running: %+v, %v", active, err)
	}

	event.EventName, event.PromptPreview, event.ReceivedAt = "UserPromptSubmit", "继续修补", now.Add(time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if active, err := store.RuntimeSessions(ctx); err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateRunning {
		t.Fatalf("UserPromptSubmit 未进入 running: %+v, %v", active, err)
	}

	event.EventName, event.ReceivedAt = "Stop", now.Add(2*time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if active, err := store.RuntimeSessions(ctx); err != nil || len(active) != 0 {
		t.Fatalf("Stop 后仍在活跃列表: %+v, %v", active, err)
	}

	event.EventName, event.ReceivedAt = "SessionEnd", now.Add(3*time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeSession(ctx, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SessionEnd 未删除 runtime session: %v", err)
	}
}

func TestRestoreRunningSessionsPreservesWaiting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 9, 15, 0, 0, time.UTC)
	running := attentionEvent("UserPromptSubmit", now)
	running.ExternalSessionID, running.PromptPreview = "running-before-restart", "不会跨重启保留"
	waiting := attentionEvent("PermissionRequest", now.Add(time.Second))
	waiting.ExternalSessionID, waiting.ToolName, waiting.EventKey = "waiting-before-restart", "Bash", "codex:restart-waiting"
	for _, event := range []domain.CodexHookEvent{running, waiting} {
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := store.RestoreRunningSessions(ctx)
	if err != nil || restored != 1 {
		t.Fatalf("RestoreRunningSessions() = %d, %v", restored, err)
	}
	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.ExternalSessionID != "waiting-before-restart" || active[0].Session.State != domain.RuntimeStateWaiting {
		t.Fatalf("启动恢复破坏 waiting 或保留 running: %+v, %v", active, err)
	}
	session, err := store.RuntimeSession(ctx, 1)
	if err != nil || session.State != domain.RuntimeStateIdle || session.PromptPreview != "" {
		t.Fatalf("历史 running 恢复结果错误: %+v, %v", session, err)
	}
}

func TestRuntimeSessionsSortWaitingThenRecentRunning(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 8, 45, 0, 0, time.UTC)
	events := []domain.CodexHookEvent{
		attentionEvent("PermissionRequest", now.Add(2*time.Second)),
		attentionEvent("PermissionRequest", now.Add(time.Second)),
		attentionEvent("UserPromptSubmit", now),
		attentionEvent("UserPromptSubmit", now.Add(3*time.Second)),
	}
	events[0].ExternalSessionID, events[0].ToolName, events[0].EventKey = "waiting-late", "Bash", "codex:waiting-late"
	events[1].ExternalSessionID, events[1].ToolName, events[1].EventKey = "waiting-early", "Bash", "codex:waiting-early"
	events[2].ExternalSessionID = "running-old"
	events[3].ExternalSessionID = "running-new"
	for _, event := range events {
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	active, err := store.RuntimeSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"waiting-early", "waiting-late", "running-new", "running-old"}
	if len(active) != len(want) {
		t.Fatalf("RuntimeSessions 数量 = %d", len(active))
	}
	for index, expected := range want {
		if active[index].Session.ExternalSessionID != expected {
			t.Fatalf("RuntimeSessions[%d] = %q, want %q", index, active[index].Session.ExternalSessionID, expected)
		}
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

func TestClaimUnnotifiedAttentionIsOneShot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	for index, key := range []string{"codex:claim-1", "codex:claim-2"} {
		event := attentionEvent("PermissionRequest", now.Add(time.Duration(index)*time.Second))
		event.TurnID = "turn-claim"
		event.ToolName = "Bash"
		event.EventKey = key
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimUnnotifiedAttention(ctx, now.Add(time.Minute))
	if err != nil || len(claimed) != 2 {
		t.Fatalf("首次 claim = %+v, %v", claimed, err)
	}
	claimed, err = store.ClaimUnnotifiedAttention(ctx, now.Add(2*time.Minute))
	if err != nil || len(claimed) != 0 {
		t.Fatalf("重复 claim = %+v, %v", claimed, err)
	}
}

func TestResolveStaleRuntimeSessionsKeepsRecentWaiting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	old := attentionEvent("PermissionRequest", now.Add(-8*24*time.Hour))
	old.ExternalSessionID, old.EventKey = "old-session", "codex:old-stale"
	recent := attentionEvent("PermissionRequest", now.Add(-time.Hour))
	recent.ExternalSessionID, recent.EventKey = "recent-session", "codex:recent"
	for _, event := range []domain.CodexHookEvent{old, recent} {
		event.TurnID, event.ToolName = "turn", "Bash"
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := store.ResolveStaleRuntimeSessions(ctx, now.Add(-7*24*time.Hour), now)
	if err != nil || resolved != 1 {
		t.Fatalf("ResolveStaleRuntimeSessions() = %d, %v", resolved, err)
	}
	waiting, err := store.WaitingSessions(ctx)
	if err != nil || len(waiting) != 1 || waiting[0].Session.ExternalSessionID != "recent-session" {
		t.Fatalf("stale 清理破坏近期 waiting: %+v, %v", waiting, err)
	}
	var reason string
	if err := store.readDB.QueryRowContext(ctx, `
		SELECT resolution_reason FROM attention_requests WHERE event_key = 'codex:old-stale'
	`).Scan(&reason); err != nil || reason != "stale_session" {
		t.Fatalf("过期请求解决原因 = %q, %v", reason, err)
	}
}

func TestPostToolUseOnlyResolvesMatchingRequestKindAndTurn(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)

	permissionTurn1 := attentionEvent("PermissionRequest", now)
	permissionTurn1.TurnID, permissionTurn1.ToolName, permissionTurn1.EventKey = "turn-1", "Bash", "codex:permission-turn-1"
	permissionTurn2 := attentionEvent("PermissionRequest", now.Add(time.Second))
	permissionTurn2.TurnID, permissionTurn2.ToolName, permissionTurn2.EventKey = "turn-2", "Bash", "codex:permission-turn-2"
	questionTurn1 := attentionEvent("PreToolUse", now.Add(2*time.Second))
	questionTurn1.TurnID, questionTurn1.ToolName, questionTurn1.EventKey = "turn-1", "request_user_input", "codex:question-turn-1"
	for _, event := range []domain.CodexHookEvent{permissionTurn1, permissionTurn2, questionTurn1} {
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟 migration 9 前没有 tool_name 的活跃请求，root fallback 仍须按请求类型隔离。
	if _, err := store.db.ExecContext(ctx, "UPDATE attention_requests SET tool_name = ''"); err != nil {
		t.Fatal(err)
	}

	completedPermission := attentionEvent("PostToolUse", now.Add(3*time.Second))
	completedPermission.TurnID, completedPermission.ToolName = "turn-1", "Bash"
	if _, err := store.ApplyCodexHookEvent(ctx, completedPermission); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		permissionTurn1.EventKey: AttentionRequestResolved,
		permissionTurn2.EventKey: AttentionRequestActive,
		questionTurn1.EventKey:   AttentionRequestActive,
	} {
		if got, err := store.AttentionRequestStatus(ctx, key); err != nil || got != want {
			t.Fatalf("AttentionRequestStatus(%s) = %q, %v; 期望 %q", key, got, err, want)
		}
	}

	completedQuestion := attentionEvent("PostToolUse", now.Add(4*time.Second))
	completedQuestion.TurnID, completedQuestion.ToolName = "turn-1", "request_user_input"
	if _, err := store.ApplyCodexHookEvent(ctx, completedQuestion); err != nil {
		t.Fatal(err)
	}
	if status, err := store.AttentionRequestStatus(ctx, questionTurn1.EventKey); err != nil || status != AttentionRequestResolved {
		t.Fatalf("问题请求状态 = %q, %v", status, err)
	}
	if state, err := store.RuntimeSessionState(ctx, permissionTurn1.ExternalSessionID); err != nil || state != domain.RuntimeStateWaiting {
		t.Fatalf("仍有其他 turn 请求时 state = %q, %v", state, err)
	}

	completedPermission.TurnID, completedPermission.ReceivedAt = "turn-2", now.Add(5*time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, completedPermission); err != nil {
		t.Fatal(err)
	}
	if state, err := store.RuntimeSessionState(ctx, permissionTurn1.ExternalSessionID); err != nil || state != domain.RuntimeStateRunning {
		t.Fatalf("所有等待解决后 state = %q, %v", state, err)
	}
}

func TestSubagentAttentionStaysScopedAndPreservesParentRuntime(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)

	root := attentionEvent("UserPromptSubmit", now)
	root.ExternalSessionID = "parent-session"
	root.CWDBasename = "parent-project"
	root.Model = "parent-model"
	root.Surface = domain.CodexSurfaceApp
	root.TerminalKind = domain.TerminalUnknown
	root.TTY = ""
	root.PromptPreview = "保留父任务 prompt"
	if _, err := store.ApplyCodexHookEvent(ctx, root); err != nil {
		t.Fatal(err)
	}

	rootPermission := root
	rootPermission.EventName = "PermissionRequest"
	rootPermission.TurnID = "root-turn"
	rootPermission.ToolName = "Bash"
	rootPermission.EventKey = "codex:root-request"
	rootPermission.PromptPreview = ""
	rootPermission.ReceivedAt = now.Add(time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, rootPermission); err != nil {
		t.Fatal(err)
	}

	childRequest := func(scope, toolKey, eventKey string, at time.Time) domain.CodexHookEvent {
		return domain.CodexHookEvent{
			ExternalSessionID: "parent-session",
			EventName:         "PermissionRequest",
			TurnID:            "shared-turn",
			SubagentScope:     scope,
			CWDBasename:       "child-project",
			Model:             "child-model",
			Surface:           domain.CodexSurfaceCLI,
			TerminalKind:      domain.TerminalITerm2,
			TTY:               "/dev/child-tty",
			ToolName:          "Bash",
			ToolUseKey:        toolKey,
			EventKey:          eventKey,
			ReceivedAt:        at,
		}
	}
	scopeA := "sha256:" + strings.Repeat("a", 64)
	scopeB := "sha256:" + strings.Repeat("b", 64)
	toolA := "sha256:" + strings.Repeat("c", 64)
	toolB := "sha256:" + strings.Repeat("d", 64)
	childA := childRequest(scopeA, toolA, "codex:child-a", now.Add(2*time.Second))
	childB := childRequest(scopeB, toolB, "codex:child-b", now.Add(3*time.Second))
	for _, event := range []domain.CodexHookEvent{childA, childB} {
		if created, err := store.ApplyCodexHookEvent(ctx, event); err != nil || !created {
			t.Fatalf("创建 child request 失败: created=%t err=%v", created, err)
		}
	}

	active, err := store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateWaiting ||
		active[0].RequestCount != 3 || active[0].Session.CWDBasename != "parent-project" ||
		active[0].Session.Model != "parent-model" || active[0].Session.Surface != domain.CodexSurfaceApp ||
		active[0].Session.PromptPreview != "保留父任务 prompt" {
		t.Fatalf("child request 覆盖父 metadata 或未聚合: %+v, %v", active, err)
	}

	childAStop := childA
	childAStop.EventName = "Stop"
	childAStop.EventKey = ""
	childAStop.ReceivedAt = now.Add(4 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, childAStop); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		childA.EventKey:         AttentionRequestResolved,
		childB.EventKey:         AttentionRequestActive,
		rootPermission.EventKey: AttentionRequestActive,
	} {
		if got, err := store.AttentionRequestStatus(ctx, key); err != nil || got != want {
			t.Fatalf("child A Stop 后 request %s = %q, %v；期望 %q", key, got, err, want)
		}
	}
	if state, err := store.RuntimeSessionState(ctx, "parent-session"); err != nil || state != domain.RuntimeStateWaiting {
		t.Fatalf("child A Stop 错误结束父 session: %q, %v", state, err)
	}

	childBPost := childB
	childBPost.EventName = "PostToolUse"
	childBPost.EventKey = ""
	childBPost.ReceivedAt = now.Add(5 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, childBPost); err != nil {
		t.Fatal(err)
	}
	if got, err := store.AttentionRequestStatus(ctx, childB.EventKey); err != nil || got != AttentionRequestResolved {
		t.Fatalf("child B PostToolUse 未精确解除: %q, %v", got, err)
	}
	if state, err := store.RuntimeSessionState(ctx, "parent-session"); err != nil || state != domain.RuntimeStateWaiting {
		t.Fatalf("root request 仍 active 时父状态 = %q, %v", state, err)
	}

	rootPost := rootPermission
	rootPost.EventName = "PostToolUse"
	rootPost.EventKey = ""
	rootPost.ReceivedAt = now.Add(6 * time.Second)
	if _, err := store.ApplyCodexHookEvent(ctx, rootPost); err != nil {
		t.Fatal(err)
	}
	active, err = store.RuntimeSessions(ctx)
	if err != nil || len(active) != 1 || active[0].Session.State != domain.RuntimeStateRunning ||
		active[0].Session.PromptPreview != "保留父任务 prompt" {
		t.Fatalf("所有 request 解决后未恢复父 running: %+v, %v", active, err)
	}
}

func TestSubagentPostToolUseFallsBackWithinSameScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	scopeA := "sha256:" + strings.Repeat("1", 64)
	scopeB := "sha256:" + strings.Repeat("2", 64)
	for index, scope := range []string{scopeA, scopeB} {
		event := attentionEvent("PermissionRequest", now.Add(time.Duration(index)*time.Second))
		event.ExternalSessionID = "fallback-parent"
		event.SubagentScope = scope
		event.TurnID = "same-turn"
		event.ToolName = "Bash"
		event.EventKey = fmt.Sprintf("codex:fallback-%d", index)
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	post := attentionEvent("PostToolUse", now.Add(3*time.Second))
	post.ExternalSessionID = "fallback-parent"
	post.SubagentScope = scopeA
	post.TurnID = "same-turn"
	post.ToolName = "Bash"
	post.ToolUseKey = "sha256:" + strings.Repeat("3", 64)
	if _, err := store.ApplyCodexHookEvent(ctx, post); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.AttentionRequestStatus(ctx, "codex:fallback-0"); got != AttentionRequestResolved {
		t.Fatalf("缺少 request tool ID 时同 scope fallback 未解决: %q", got)
	}
	if got, _ := store.AttentionRequestStatus(ctx, "codex:fallback-1"); got != AttentionRequestActive {
		t.Fatalf("fallback 跨 child scope 误解决: %q", got)
	}
}

func TestSubagentPostToolUseWithoutKeyFallsBackWithinSameScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	scopeA := "sha256:" + strings.Repeat("4", 64)
	scopeB := "sha256:" + strings.Repeat("5", 64)
	for index, scope := range []string{scopeA, scopeB} {
		event := attentionEvent("PermissionRequest", now.Add(time.Duration(index)*time.Second))
		event.ExternalSessionID = "keyless-completion-parent"
		event.SubagentScope = scope
		event.TurnID = "same-turn"
		event.ToolName = "Bash"
		event.ToolUseKey = "sha256:" + strings.Repeat(string(rune('6'+index)), 64)
		event.EventKey = fmt.Sprintf("codex:keyless-completion-%d", index)
		if _, err := store.ApplyCodexHookEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	post := attentionEvent("PostToolUse", now.Add(3*time.Second))
	post.ExternalSessionID = "keyless-completion-parent"
	post.SubagentScope = scopeA
	post.TurnID = "same-turn"
	post.ToolName = "Bash"
	if _, err := store.ApplyCodexHookEvent(ctx, post); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.AttentionRequestStatus(ctx, "codex:keyless-completion-0"); got != AttentionRequestResolved {
		t.Fatalf("缺少 completion tool ID 时同 scope fallback 未解决: %q", got)
	}
	if got, _ := store.AttentionRequestStatus(ctx, "codex:keyless-completion-1"); got != AttentionRequestActive {
		t.Fatalf("缺少 completion tool ID 时 fallback 跨 child scope: %q", got)
	}
}

func TestToolCompletionRequiresExactOrUniqueCorrelation(t *testing.T) {
	key := func(value string) string {
		return "sha256:" + strings.Repeat(value, 64)
	}

	t.Run("工具 ID 不匹配时按相同输入精确解除", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
		request := attentionEvent("PermissionRequest", now)
		request.TurnID, request.ToolName, request.EventKey = "turn", "Bash", "codex:input-match"
		request.ToolUseKey, request.ToolInputKey = key("a"), key("b")
		if _, err := store.ApplyCodexHookEvent(ctx, request); err != nil {
			t.Fatal(err)
		}
		completion := attentionEvent("PostToolUse", now.Add(time.Second))
		completion.TurnID, completion.ToolName = "turn", "Bash"
		completion.ToolUseKey, completion.ToolInputKey = key("c"), key("b")
		if _, err := store.ApplyCodexHookEvent(ctx, completion); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.AttentionRequestStatus(ctx, request.EventKey); got != AttentionRequestResolved {
			t.Fatalf("相同 tool_input_key 未解除请求: %q", got)
		}
	})

	t.Run("输入键不一致时不降级误清", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Date(2026, 8, 5, 9, 10, 0, 0, time.UTC)
		request := attentionEvent("PermissionRequest", now)
		request.TurnID, request.ToolName, request.EventKey = "turn", "Bash", "codex:input-mismatch"
		request.ToolInputKey = key("d")
		if _, err := store.ApplyCodexHookEvent(ctx, request); err != nil {
			t.Fatal(err)
		}
		completion := attentionEvent("PostToolUse", now.Add(time.Second))
		completion.TurnID, completion.ToolName, completion.ToolInputKey = "turn", "Bash", key("e")
		if _, err := store.ApplyCodexHookEvent(ctx, completion); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.AttentionRequestStatus(ctx, request.EventKey); got != AttentionRequestActive {
			t.Fatalf("不同 tool_input_key 误清请求: %q", got)
		}
	})

	t.Run("无精确键且候选不唯一时全部保留", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Date(2026, 8, 5, 9, 20, 0, 0, time.UTC)
		for index := 0; index < 2; index++ {
			request := attentionEvent("PermissionRequest", now.Add(time.Duration(index)*time.Second))
			request.TurnID, request.ToolName = "turn", "Bash"
			request.EventKey = fmt.Sprintf("codex:ambiguous-%d", index)
			if _, err := store.ApplyCodexHookEvent(ctx, request); err != nil {
				t.Fatal(err)
			}
		}
		completion := attentionEvent("PostToolUse", now.Add(2*time.Second))
		completion.TurnID, completion.ToolName = "turn", "Bash"
		if _, err := store.ApplyCodexHookEvent(ctx, completion); err != nil {
			t.Fatal(err)
		}
		waiting, err := store.WaitingSessions(ctx)
		if err != nil || len(waiting) != 1 || waiting[0].RequestCount != 2 {
			t.Fatalf("模糊 fallback 清除了请求: %+v, %v", waiting, err)
		}
	})

	t.Run("无精确键且候选唯一时允许回落", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		now := time.Date(2026, 8, 5, 9, 30, 0, 0, time.UTC)
		request := attentionEvent("PermissionRequest", now)
		request.TurnID, request.ToolName, request.EventKey = "turn", "Bash", "codex:unique-fallback"
		if _, err := store.ApplyCodexHookEvent(ctx, request); err != nil {
			t.Fatal(err)
		}
		completion := attentionEvent("PostToolUse", now.Add(time.Second))
		completion.TurnID, completion.ToolName = "turn", "Bash"
		if _, err := store.ApplyCodexHookEvent(ctx, completion); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.AttentionRequestStatus(ctx, request.EventKey); got != AttentionRequestResolved {
			t.Fatalf("唯一 fallback 未解除请求: %q", got)
		}
	})
}

func TestSubagentStopDoesNotResolveUnscopedRequest(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	request := attentionEvent("PermissionRequest", now)
	request.TurnID, request.ToolName, request.EventKey = "turn", "Bash", "codex:unscoped"
	if _, err := store.ApplyCodexHookEvent(ctx, request); err != nil {
		t.Fatal(err)
	}
	stop := attentionEvent("SubagentStop", now.Add(time.Second))
	stop.SubagentEvent = true
	stop.SubagentScope = "sha256:" + strings.Repeat("f", 64)
	if _, err := store.ApplyCodexHookEvent(ctx, stop); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.AttentionRequestStatus(ctx, request.EventKey); got != AttentionRequestActive {
		t.Fatalf("SubagentStop 清除了无 scope 请求: %q", got)
	}
	emptyScopeStop := attentionEvent("SubagentStop", now.Add(2*time.Second))
	if _, err := store.ApplyCodexHookEvent(ctx, emptyScopeStop); err != nil {
		t.Fatalf("空 scope SubagentStop 应安全忽略: %v", err)
	}
	if got, _ := store.AttentionRequestStatus(ctx, request.EventKey); got != AttentionRequestActive {
		t.Fatalf("空 scope SubagentStop 改变了无 scope 请求: %q", got)
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
