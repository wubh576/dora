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
