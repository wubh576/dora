package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const migrationVersion = 1

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("数据库路径不能为空")
	}

	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据库目录 %q: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("设置数据库目录权限 %q: %w", dir, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取数据库目录 %q: %w", dir, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("设置数据库权限 %q: %w", path, err)
	}

	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行 SQLite 配置 %q: %w", statement, err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始数据库 migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version       INTEGER PRIMARY KEY,
			applied_at_ms INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("创建 schema_migrations: %w", err)
	}

	var applied bool
	if err := tx.QueryRowContext(
		ctx,
		"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
		migrationVersion,
	).Scan(&applied); err != nil {
		return fmt.Errorf("检查 migration %d: %w", migrationVersion, err)
	}

	if !applied {
		now := time.Now().UTC().UnixMilli()
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE dora_state (
				id                INTEGER PRIMARY KEY CHECK (id = 1),
				initialized_at_ms INTEGER NOT NULL
			)
		`); err != nil {
			return fmt.Errorf("创建 dora_state: %w", err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO dora_state (id, initialized_at_ms) VALUES (1, ?)",
			now,
		); err != nil {
			return fmt.Errorf("保存 Dora 初始化状态: %w", err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations (version, applied_at_ms) VALUES (?, ?)",
			migrationVersion,
			now,
		); err != nil {
			return fmt.Errorf("记录 migration %d: %w", migrationVersion, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交数据库 migration: %w", err)
	}
	return nil
}

func (s *Store) InitializedAt(ctx context.Context) (time.Time, error) {
	var initializedAtMS int64
	if err := s.db.QueryRowContext(
		ctx,
		"SELECT initialized_at_ms FROM dora_state WHERE id = 1",
	).Scan(&initializedAtMS); err != nil {
		return time.Time{}, fmt.Errorf("查询 Dora 初始化状态: %w", err)
	}
	return time.UnixMilli(initializedAtMS).UTC(), nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
