package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestOpenInitializesAndPersistsState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "dora.db")

	firstStore, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() 第一次启动失败: %v", err)
	}
	firstInitializedAt, err := firstStore.InitializedAt(ctx)
	if err != nil {
		t.Fatalf("InitializedAt() 第一次读取失败: %v", err)
	}

	var migrationCount int
	if err := firstStore.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?",
		migrationVersion,
	).Scan(&migrationCount); err != nil {
		t.Fatalf("查询 migration 失败: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration 记录数 = %d，期望 1", migrationCount)
	}
	var sessionTableCount int
	if err := firstStore.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_sessions'",
	).Scan(&sessionTableCount); err != nil {
		t.Fatalf("检查 session 表失败: %v", err)
	}
	if sessionTableCount != 0 {
		t.Fatal("Dora 不应持久化 agent session")
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("Close() 第一次关闭失败: %v", err)
	}

	secondStore, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() 第二次启动失败: %v", err)
	}
	defer secondStore.Close()

	secondInitializedAt, err := secondStore.InitializedAt(ctx)
	if err != nil {
		t.Fatalf("InitializedAt() 第二次读取失败: %v", err)
	}
	if !secondInitializedAt.Equal(firstInitializedAt) {
		t.Fatalf("初始化时间被重置: 第一次 %s，第二次 %s", firstInitializedAt, secondInitializedAt)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取数据库文件信息失败: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("数据库权限 = %o，期望 600", got)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读取数据库目录信息失败: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("新建数据库目录权限 = %o，期望 700", got)
	}
}

func TestOpenPreservesExistingDirectoryPermissions(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("创建既有目录失败: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("设置既有目录权限失败: %v", err)
	}

	store, err := Open(ctx, filepath.Join(dir, "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("读取既有目录信息失败: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("既有目录权限被修改为 %o，期望保持 755", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar, err := os.Stat(filepath.Join(dir, "dora.db") + suffix)
		if err != nil {
			t.Fatalf("读取 SQLite sidecar %s 失败: %v", suffix, err)
		}
		if got := sidecar.Mode().Perm(); got != 0o600 {
			t.Fatalf("SQLite sidecar %s 权限 = %o，期望 600", suffix, got)
		}
	}
}

func TestRepositoryRejectsInvalidUsageAndPreservesActiveGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	valid := domain.UsageEvent{
		Source:      domain.CodexSource,
		DedupKey:    "valid",
		OccurredAt:  time.Now().UTC(),
		Model:       "gpt",
		Project:     "dora",
		InputTokens: 3,
		TotalTokens: 3,
	}
	if err := store.BeginUsageScan(ctx, "valid-run", "full", time.Now()); err != nil {
		t.Fatalf("BeginUsageScan() 失败: %v", err)
	}
	if err := store.CompleteUsageScan(ctx, "valid-run", time.Now(), []domain.UsageEvent{valid}, nil, 0, 1, ""); err != nil {
		t.Fatalf("CompleteUsageScan() 有效事件失败: %v", err)
	}

	invalid := valid
	invalid.DedupKey = "invalid"
	invalid.InputTokens = -1
	if err := store.BeginUsageScan(ctx, "invalid-run", "full", time.Now()); err != nil {
		t.Fatalf("BeginUsageScan() 失败: %v", err)
	}
	if err := store.CompleteUsageScan(ctx, "invalid-run", time.Now(), []domain.UsageEvent{invalid}, nil, 0, 1, ""); err == nil {
		t.Fatal("repository 接受了负 token")
	}
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("LoadUsageEvents() 失败: %v", err)
	}
	if len(events) != 1 || events[0].DedupKey != "valid" {
		t.Fatalf("staging 失败替换了 active generation: %+v", events)
	}
}

func TestProviderUsageGenerationsRemainIsolated(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	events := []domain.UsageEvent{
		{
			Source: domain.CodexSource, DedupKey: "codex-message", OccurredAt: now,
			Model: "gpt", Project: "dora", InputTokens: 3, TotalTokens: 3,
		},
		{
			Source: domain.ClaudeCodeSource, DedupKey: "claude-message", OccurredAt: now,
			Model: "claude", Project: "dora", OutputTokens: 5,
			CacheCreationInputTokens: 7, CacheCreation5mTokens: 3, CacheCreation1hTokens: 4, TotalTokens: 12,
		},
	}
	for index, source := range []string{domain.CodexSource, domain.ClaudeCodeSource} {
		runID := fmt.Sprintf("provider-run-%d", index)
		if err := store.BeginProviderUsageScan(ctx, source, runID, "full", now); err != nil {
			t.Fatalf("BeginProviderUsageScan(%s) 失败: %v", source, err)
		}
		if err := store.CompleteProviderUsageScan(
			ctx, source, runID, now, events[index:index+1], nil, 1, 1, "",
		); err != nil {
			t.Fatalf("CompleteProviderUsageScan(%s) 失败: %v", source, err)
		}
	}
	if err := store.BeginProviderUsageScan(ctx, domain.CodexSource, "mismatched-run", "full", now); err != nil {
		t.Fatalf("创建 mismatch run 失败: %v", err)
	}
	if err := store.CompleteProviderUsageScan(
		ctx, domain.ClaudeCodeSource, "mismatched-run", now, events[1:2], nil, 1, 1, "",
	); err == nil {
		t.Fatal("CompleteProviderUsageScan() 接受了其他 provider 的 runID")
	}
	if err := store.FailProviderUsageScan(
		ctx, domain.ClaudeCodeSource, "mismatched-run", now, 1, 0, errors.New("fixture failure"),
	); err == nil {
		t.Fatal("FailProviderUsageScan() 接受了其他 provider 的 runID")
	}
	var mismatchStatus string
	if err := store.db.QueryRowContext(
		ctx,
		"SELECT status FROM scan_runs WHERE run_id = ? AND source = ?",
		"mismatched-run",
		domain.CodexSource,
	).Scan(&mismatchStatus); err != nil {
		t.Fatalf("读取 mismatch run 失败: %v", err)
	}
	if mismatchStatus != "running" {
		t.Fatalf("provider mismatch 修改了原 run 状态: %s", mismatchStatus)
	}
	if err := store.FailProviderUsageScan(
		ctx, domain.CodexSource, "mismatched-run", now, 0, 0, errors.New("fixture cleanup"),
	); err != nil {
		t.Fatalf("清理 mismatch fixture 失败: %v", err)
	}

	if err := store.BeginProviderUsageScan(ctx, domain.ClaudeCodeSource, "claude-failure", "incremental", now); err != nil {
		t.Fatalf("创建 Claude failure run 失败: %v", err)
	}
	if err := store.FailProviderUsageScan(
		ctx, domain.ClaudeCodeSource, "claude-failure", now, 1, 0, errors.New("fixture failure"),
	); err != nil {
		t.Fatalf("记录 Claude failure run 失败: %v", err)
	}

	for index, source := range []string{domain.CodexSource, domain.ClaudeCodeSource} {
		stored, err := store.LoadUsageEvents(ctx, source)
		if err != nil {
			t.Fatalf("LoadUsageEvents(%s) 失败: %v", source, err)
		}
		if len(stored) != 1 || stored[0].DedupKey != events[index].DedupKey {
			t.Fatalf("provider generation 串扰: source=%s events=%+v", source, stored)
		}
		if source == domain.ClaudeCodeSource &&
			(stored[0].CacheCreation5mTokens != 3 || stored[0].CacheCreation1hTokens != 4) {
			t.Fatalf("Claude cache creation 时长明细未持久化: %+v", stored[0])
		}
	}
}

func TestFailedGenerationSwapCleansStagingAndPreservesActiveData(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	active := domain.UsageEvent{
		Source:      domain.CodexSource,
		DedupKey:    "active",
		OccurredAt:  time.Now().UTC(),
		Model:       "gpt",
		Project:     "dora",
		InputTokens: 3,
		TotalTokens: 3,
	}
	if err := store.BeginUsageScan(ctx, "active-run", "full", time.Now()); err != nil {
		t.Fatalf("BeginUsageScan(active) 失败: %v", err)
	}
	if err := store.CompleteUsageScan(ctx, "active-run", time.Now(), []domain.UsageEvent{active}, nil, 1, 1, ""); err != nil {
		t.Fatalf("CompleteUsageScan(active) 失败: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_generation_swap
		BEFORE DELETE ON usage_events
		BEGIN
			SELECT RAISE(ABORT, 'fixture swap failure');
		END
	`); err != nil {
		t.Fatalf("创建 swap failure fixture 失败: %v", err)
	}

	replacement := active
	replacement.DedupKey = "replacement"
	replacement.InputTokens = 7
	replacement.TotalTokens = 7
	if err := store.BeginUsageScan(ctx, "failed-swap", "full", time.Now()); err != nil {
		t.Fatalf("BeginUsageScan(failed) 失败: %v", err)
	}
	swapErr := store.CompleteUsageScan(
		ctx,
		"failed-swap",
		time.Now(),
		[]domain.UsageEvent{replacement},
		nil,
		1,
		1,
		"",
	)
	if swapErr == nil {
		t.Fatal("generation swap fixture 未失败")
	}
	var stagedBeforeCleanup int
	if err := store.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM usage_events_staging WHERE run_id = 'failed-swap'",
	).Scan(&stagedBeforeCleanup); err != nil {
		t.Fatalf("读取 staging 失败: %v", err)
	}
	if stagedBeforeCleanup != 1 {
		t.Fatalf("swap 失败前 staging 数 = %d，期望 1", stagedBeforeCleanup)
	}
	if err := store.FailUsageScan(ctx, "failed-swap", time.Now(), 1, 1, swapErr); err != nil {
		t.Fatalf("FailUsageScan() 失败: %v", err)
	}
	var stagedAfterCleanup int
	if err := store.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM usage_events_staging WHERE run_id = 'failed-swap'",
	).Scan(&stagedAfterCleanup); err != nil {
		t.Fatalf("读取清理后 staging 失败: %v", err)
	}
	if stagedAfterCleanup != 0 {
		t.Fatalf("失败 scan 遗留 staging: %d", stagedAfterCleanup)
	}
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 active generation 失败: %v", err)
	}
	if len(events) != 1 || events[0].DedupKey != active.DedupKey {
		t.Fatalf("swap 失败破坏 active generation: %+v", events)
	}
}

func TestFailedUsageScanPreservesLastSuccessTime(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	failureAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if err := store.BeginUsageScan(ctx, "first-failure", "full", failureAt.Add(-time.Second)); err != nil {
		t.Fatalf("BeginUsageScan() 首次失败记录失败: %v", err)
	}
	if err := store.FailUsageScan(ctx, "first-failure", failureAt, 1, 0, errors.New("fixture failure")); err != nil {
		t.Fatalf("FailUsageScan() 首次失败记录失败: %v", err)
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("UsageProviderState() 首次失败读取失败: %v", err)
	}
	if state.Status != "error" || state.LastScanAt != nil {
		t.Fatalf("首次失败伪造了成功时间: %+v", state)
	}

	successAt := failureAt.Add(time.Minute)
	if err := store.BeginUsageScan(ctx, "success", "full", successAt.Add(-time.Second)); err != nil {
		t.Fatalf("BeginUsageScan() 成功记录失败: %v", err)
	}
	if err := store.CompleteUsageScan(ctx, "success", successAt, nil, nil, 1, 0, ""); err != nil {
		t.Fatalf("CompleteUsageScan() 失败: %v", err)
	}
	laterFailureAt := successAt.Add(20 * time.Minute)
	if err := store.BeginUsageScan(ctx, "later-failure", "incremental", laterFailureAt.Add(-time.Second)); err != nil {
		t.Fatalf("BeginUsageScan() 后续失败记录失败: %v", err)
	}
	if err := store.FailUsageScan(ctx, "later-failure", laterFailureAt, 2, 1, errors.New("later fixture failure")); err != nil {
		t.Fatalf("FailUsageScan() 后续失败记录失败: %v", err)
	}
	// 模拟旧版本已把失败时间写入 provider_state，读取仍应以成功 run 为准。
	if _, err := store.db.ExecContext(
		ctx,
		"UPDATE provider_state SET last_usage_at_ms = ? WHERE provider = ?",
		laterFailureAt.UnixMilli(),
		domain.CodexSource,
	); err != nil {
		t.Fatalf("写入旧版本状态 fixture 失败: %v", err)
	}
	state, err = store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("UsageProviderState() 后续失败读取失败: %v", err)
	}
	if state.Status != "error" || state.LastScanAt == nil || !state.LastScanAt.Equal(successAt) {
		t.Fatalf("后续失败覆盖了最后成功时间: %+v", state)
	}
}

func TestWALAllowsReadDuringWriteTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.withImmediateTransaction(ctx, func(_ *sql.Conn) error {
			close(writeStarted)
			<-releaseWrite
			return nil
		})
	}()
	<-writeStarted

	readDone := make(chan error, 1)
	go func() {
		_, err := store.InitializedAt(ctx)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("写事务期间读取失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("读取被写事务阻塞")
	}
	close(releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("写事务失败: %v", err)
	}
}

func TestCanceledReadDoesNotPoisonLaterQueries(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer store.Close()

	queryCtx, cancel := context.WithCancel(ctx)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		var total int64
		done <- store.readDB.QueryRowContext(queryCtx, `
			WITH RECURSIVE values_list(value) AS (
				VALUES(0)
				UNION ALL
				SELECT value + 1 FROM values_list WHERE value < 100000000
			)
			SELECT SUM(value) FROM values_list
		`).Scan(&total)
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消长查询未返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("取消长查询超时")
	}

	if _, err := store.InitializedAt(ctx); err != nil {
		t.Fatalf("取消查询污染了后续读取: %v", err)
	}
	if _, err := store.readDB.ExecContext(ctx, "UPDATE dora_state SET initialized_at_ms = 0"); err == nil {
		t.Fatal("SQLite 读取连接允许写入")
	}
}

func TestOpenMigratesVersionOneDatabaseWithoutResettingState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dora.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("创建 v1 数据库失败: %v", err)
	}
	expected := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE dora_state (id INTEGER PRIMARY KEY CHECK (id = 1), initialized_at_ms INTEGER NOT NULL)`,
		`INSERT INTO schema_migrations (version, applied_at_ms) VALUES (1, 1)`,
		`INSERT INTO dora_state (id, initialized_at_ms) VALUES (1, ` + fmt.Sprint(expected.UnixMilli()) + `)`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			t.Fatalf("准备 v1 数据库失败: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭 v1 数据库失败: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("升级 v1 数据库失败: %v", err)
	}
	defer store.Close()
	actual, err := store.InitializedAt(ctx)
	if err != nil {
		t.Fatalf("读取升级后的初始化时间失败: %v", err)
	}
	if !actual.Equal(expected) {
		t.Fatalf("升级重置初始化时间: actual=%s expected=%s", actual, expected)
	}
	var migrationCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("读取升级 migration 失败: %v", err)
	}
	if migrationCount != migrationVersion {
		t.Fatalf("migration 数 = %d，期望 %d", migrationCount, migrationVersion)
	}
}

func TestOpenMigratesVersionThreeWithoutLosingUsageOrQuota(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dora.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at_ms INTEGER NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}
	for index, migration := range []func(context.Context, *sql.Tx, int64) error{
		migrateDoraState, migrateUsage, migrateQuota,
	} {
		tx, err := legacy.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := migration(ctx, tx, 1); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations VALUES (?, 1)", index+1); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO usage_events (
			source, dedup_key, occurred_at_ms, model, project,
			input_tokens, total_tokens, updated_at_ms
		) VALUES (?, 'legacy-usage', 1, 'gpt', 'dora', 7, 7, 1)
	`, domain.CodexSource); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO quota_snapshots (
			provider, window_key, label, used_percent, remaining_percent,
			fetched_at_ms, source, source_state
		) VALUES (?, 'five_hour', '5 小时', 25, 75, 1, 'fixture', 'ready')
	`, domain.CodexSource); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("升级 v3 数据库失败: %v", err)
	}
	defer store.Close()
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil || len(events) != 1 || events[0].DedupKey != "legacy-usage" {
		t.Fatalf("v3 usage 丢失: events=%+v err=%v", events, err)
	}
	if events[0].CacheCreation5mTokens != 0 || events[0].CacheCreation1hTokens != 0 {
		t.Fatalf("v3 usage migration 未使用安全默认值: %+v", events[0])
	}
	quotas, err := store.LatestQuotaSnapshots(ctx, domain.CodexSource)
	if err != nil || len(quotas) != 1 || quotas[0].RemainingPercent != 75 {
		t.Fatalf("v3 quota 丢失: quotas=%+v err=%v", quotas, err)
	}
	var migrationCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != migrationVersion {
		t.Fatalf("migration 数 = %d，期望 %d", migrationCount, migrationVersion)
	}
}
