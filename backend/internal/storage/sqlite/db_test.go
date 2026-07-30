package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
}
