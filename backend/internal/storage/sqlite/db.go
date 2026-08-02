package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const migrationVersion = 6

type Store struct {
	db     *sql.DB
	readDB *sql.DB
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
	if err := secureSQLiteFiles(path); err != nil {
		db.Close()
		return nil, err
	}

	readURL := &url.URL{Scheme: "file", Path: path}
	query := readURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	readURL.RawQuery = query.Encode()
	readDB, err := sql.Open("sqlite", readURL.String())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("打开 SQLite 读取连接: %w", err)
	}
	readDB.SetMaxOpenConns(4)
	// 被浏览器取消的查询会中断 SQLite 连接；不复用该连接可避免后续读取持续失败。
	readDB.SetMaxIdleConns(0)
	if err := readDB.PingContext(ctx); err != nil {
		readDB.Close()
		db.Close()
		return nil, fmt.Errorf("配置 SQLite 读取连接: %w", err)
	}
	store.readDB = readDB
	if err := secureSQLiteFiles(path); err != nil {
		readDB.Close()
		db.Close()
		return nil, err
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

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version       INTEGER PRIMARY KEY,
			applied_at_ms INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("创建 schema_migrations: %w", err)
	}

	migrations := []func(context.Context, *sql.Tx, int64) error{
		migrateDoraState,
		migrateUsage,
		migrateQuota,
		migrateUsageProviderDiagnostics,
		migrateCacheCreationDurations,
		migrateRuntimeAttention,
	}
	for index, migration := range migrations {
		version := index + 1
		var applied bool
		if err := s.db.QueryRowContext(
			ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
			version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("检查 migration %d: %w", version, err)
		}
		if applied {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("开始 migration %d: %w", version, err)
		}
		now := time.Now().UTC().UnixMilli()
		if err := migration(ctx, tx, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("执行 migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations (version, applied_at_ms) VALUES (?, ?)",
			version,
			now,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("记录 migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交 migration %d: %w", version, err)
		}
	}
	return nil
}

func migrateDoraState(ctx context.Context, tx *sql.Tx, now int64) error {
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
	return nil
}

func migrateUsage(ctx context.Context, tx *sql.Tx, _ int64) error {
	statements := []string{
		`CREATE TABLE scan_runs (
			run_id          TEXT PRIMARY KEY,
			source          TEXT NOT NULL,
			mode            TEXT NOT NULL,
			started_at_ms   INTEGER NOT NULL,
			finished_at_ms  INTEGER,
			status          TEXT NOT NULL,
			files_seen      INTEGER NOT NULL DEFAULT 0,
			events_seen     INTEGER NOT NULL DEFAULT 0,
			error_message   TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE source_files (
			source             TEXT NOT NULL,
			path               TEXT NOT NULL,
			file_identity      TEXT NOT NULL DEFAULT '',
			size_bytes         INTEGER NOT NULL DEFAULT 0,
			mtime_ns           INTEGER NOT NULL DEFAULT 0,
			parsed_offset      INTEGER NOT NULL DEFAULT 0,
			complete_line_end  INTEGER NOT NULL DEFAULT 0,
			head_hash          TEXT NOT NULL DEFAULT '',
			tail_hash          TEXT NOT NULL DEFAULT '',
			parser_version     INTEGER NOT NULL,
			parser_state_json  TEXT NOT NULL DEFAULT '',
			last_success_at_ms INTEGER,
			last_error         TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (source, path)
		)`,
		`CREATE TABLE usage_events (
			id                          INTEGER PRIMARY KEY AUTOINCREMENT,
			source                      TEXT NOT NULL,
			dedup_key                   TEXT NOT NULL,
			occurred_at_ms              INTEGER NOT NULL,
			model                       TEXT NOT NULL,
			project                     TEXT NOT NULL DEFAULT 'unknown',
			input_tokens                INTEGER NOT NULL DEFAULT 0,
			output_tokens               INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens         INTEGER NOT NULL DEFAULT 0,
			cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens     INTEGER NOT NULL DEFAULT 0,
			reported_total_tokens       INTEGER NOT NULL DEFAULT 0,
			total_tokens                INTEGER NOT NULL DEFAULT 0,
			rollout_key                 TEXT NOT NULL DEFAULT '',
			parent_rollout_key          TEXT NOT NULL DEFAULT '',
			replay_fingerprint          TEXT NOT NULL DEFAULT '',
			inherited_replay            INTEGER NOT NULL DEFAULT 0,
			updated_at_ms               INTEGER NOT NULL,
			UNIQUE (source, dedup_key)
		)`,
		`CREATE INDEX idx_usage_events_time ON usage_events (occurred_at_ms)`,
		`CREATE INDEX idx_usage_events_source_time ON usage_events (source, occurred_at_ms)`,
		`CREATE INDEX idx_usage_events_model_time ON usage_events (model, occurred_at_ms)`,
		`CREATE INDEX idx_usage_events_project_time ON usage_events (project, occurred_at_ms)`,
		`CREATE TABLE usage_events_staging (
			run_id                      TEXT NOT NULL,
			source                      TEXT NOT NULL,
			dedup_key                   TEXT NOT NULL,
			occurred_at_ms              INTEGER NOT NULL,
			model                       TEXT NOT NULL,
			project                     TEXT NOT NULL DEFAULT 'unknown',
			input_tokens                INTEGER NOT NULL DEFAULT 0,
			output_tokens               INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens         INTEGER NOT NULL DEFAULT 0,
			cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens     INTEGER NOT NULL DEFAULT 0,
			reported_total_tokens       INTEGER NOT NULL DEFAULT 0,
			total_tokens                INTEGER NOT NULL DEFAULT 0,
			rollout_key                 TEXT NOT NULL DEFAULT '',
			parent_rollout_key          TEXT NOT NULL DEFAULT '',
			replay_fingerprint          TEXT NOT NULL DEFAULT '',
			inherited_replay            INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (run_id, source, dedup_key)
		)`,
		`CREATE TABLE provider_state (
			provider            TEXT PRIMARY KEY,
			usage_status        TEXT NOT NULL DEFAULT 'not_scanned',
			quota_status        TEXT NOT NULL DEFAULT 'not_configured',
			last_usage_at_ms    INTEGER,
			last_quota_at_ms    INTEGER,
			last_usage_error    TEXT NOT NULL DEFAULT '',
			last_quota_error    TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateQuota(ctx context.Context, tx *sql.Tx, _ int64) error {
	statements := []string{
		`CREATE TABLE quota_snapshots (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			provider           TEXT NOT NULL,
			window_key         TEXT NOT NULL,
			label              TEXT NOT NULL,
			used_percent       REAL NOT NULL,
			remaining_percent  REAL NOT NULL,
			resets_at_ms       INTEGER,
			fetched_at_ms      INTEGER NOT NULL,
			source             TEXT NOT NULL,
			source_state       TEXT NOT NULL,
			plan               TEXT NOT NULL DEFAULT '',
			account_label      TEXT NOT NULL DEFAULT '',
			UNIQUE (provider, window_key, fetched_at_ms)
		)`,
		`CREATE INDEX idx_quota_provider_time
			ON quota_snapshots (provider, fetched_at_ms DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateUsageProviderDiagnostics(ctx context.Context, tx *sql.Tx, _ int64) error {
	for _, statement := range []string{
		"ALTER TABLE provider_state ADD COLUMN config_found INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE provider_state ADD COLUMN session_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE provider_state ADD COLUMN parser_version INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateCacheCreationDurations(ctx context.Context, tx *sql.Tx, _ int64) error {
	for _, statement := range []string{
		"ALTER TABLE usage_events ADD COLUMN cache_creation_5m_tokens INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE usage_events ADD COLUMN cache_creation_1h_tokens INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE usage_events_staging ADD COLUMN cache_creation_5m_tokens INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE usage_events_staging ADD COLUMN cache_creation_1h_tokens INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateRuntimeAttention(ctx context.Context, tx *sql.Tx, _ int64) error {
	statements := []string{
		`CREATE TABLE runtime_sessions (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			provider            TEXT NOT NULL,
			external_session_id TEXT NOT NULL,
			cwd_basename        TEXT NOT NULL DEFAULT '',
			model               TEXT NOT NULL DEFAULT '',
			surface             TEXT NOT NULL DEFAULT 'unknown',
			terminal_kind       TEXT NOT NULL DEFAULT '',
			tty                 TEXT NOT NULL DEFAULT '',
			state               TEXT NOT NULL,
			last_seen_at_ms     INTEGER NOT NULL,
			UNIQUE (provider, external_session_id)
		)`,
		`CREATE INDEX idx_runtime_sessions_state_seen
			ON runtime_sessions (state, last_seen_at_ms DESC)`,
		`CREATE TABLE attention_requests (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			runtime_session_id   INTEGER,
			event_key            TEXT NOT NULL UNIQUE,
			kind                 TEXT NOT NULL,
			summary              TEXT NOT NULL,
			turn_id              TEXT NOT NULL DEFAULT '',
			created_at_ms        INTEGER NOT NULL,
			notified_at_ms       INTEGER,
			resolved_at_ms       INTEGER,
			resolution_reason    TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (runtime_session_id) REFERENCES runtime_sessions(id) ON DELETE SET NULL
		)`,
		`CREATE INDEX idx_attention_requests_active
			ON attention_requests (runtime_session_id, resolved_at_ms, created_at_ms DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InitializedAt(ctx context.Context) (time.Time, error) {
	var initializedAtMS int64
	if err := s.readDB.QueryRowContext(
		ctx,
		"SELECT initialized_at_ms FROM dora_state WHERE id = 1",
	).Scan(&initializedAtMS); err != nil {
		return time.Time{}, fmt.Errorf("查询 Dora 初始化状态: %w", err)
	}
	return time.UnixMilli(initializedAtMS).UTC(), nil
}

func (s *Store) Close() error {
	return errors.Join(s.readDB.Close(), s.db.Close())
}

func secureSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("设置 SQLite 文件权限 %q: %w", candidate, err)
		}
	}
	return nil
}
