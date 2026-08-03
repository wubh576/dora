package codex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	_ "modernc.org/sqlite"
)

const threadTitleLimit = 120

// ThreadTitleReader 只读 Codex App 自己的任务标题，Dora runtime 决定是否缓存。
type ThreadTitleReader struct {
	dbs []*sql.DB
}

func OpenThreadTitleReader(ctx context.Context, configuredHomes []string) (*ThreadTitleReader, error) {
	homes, err := ResolveHomes(configuredHomes)
	if err != nil {
		return nil, err
	}
	reader := &ThreadTitleReader{}
	for _, home := range homes {
		path := filepath.Join(home, "state_5.sqlite")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			_ = reader.Close()
			return nil, fmt.Errorf("检查 Codex 任务标题数据库: %w", err)
		}
		db, err := openThreadTitleDB(ctx, path)
		if err != nil {
			_ = reader.Close()
			return nil, err
		}
		reader.dbs = append(reader.dbs, db)
	}
	return reader, nil
}

func openThreadTitleDB(ctx context.Context, path string) (*sql.DB, error) {
	readURL := &url.URL{Scheme: "file", Path: path}
	query := readURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(1000)")
	readURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", readURL.String())
	if err != nil {
		return nil, fmt.Errorf("打开 Codex 任务标题数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, "SELECT id, title, name FROM threads WHERE 0")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("读取 Codex 任务标题结构: %w", err)
	}
	_ = rows.Close()
	return db, nil
}

func (reader *ThreadTitleReader) Titles(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	titles := make(map[string]string)
	unique := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" {
			continue
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		unique = append(unique, sessionID)
	}
	if len(unique) == 0 {
		return titles, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	query := `SELECT id, substr(COALESCE(name, ''), 1, 240), substr(COALESCE(title, ''), 1, 240)
		FROM threads WHERE id IN (` + placeholders + `)`
	args := make([]any, len(unique))
	for index, sessionID := range unique {
		args[index] = sessionID
	}
	for _, db := range reader.dbs {
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("查询 Codex 任务标题: %w", err)
		}
		for rows.Next() {
			var sessionID, name, title string
			if err := rows.Scan(&sessionID, &name, &title); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("解析 Codex 任务标题: %w", err)
			}
			if _, exists := titles[sessionID]; exists {
				continue
			}
			value := cleanThreadTitle(name)
			if value == "" {
				value = cleanThreadTitle(title)
			}
			if value != "" {
				titles[sessionID] = value
			}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("关闭 Codex 任务标题查询: %w", err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("读取 Codex 任务标题: %w", err)
		}
	}
	return titles, nil
}

func (reader *ThreadTitleReader) Close() error {
	if reader == nil {
		return nil
	}
	var closeErrors []error
	for _, db := range reader.dbs {
		closeErrors = append(closeErrors, db.Close())
	}
	return errors.Join(closeErrors...)
}

func cleanThreadTitle(value string) string {
	var builder strings.Builder
	separated := true
	count := 0
	for _, current := range value {
		if unicode.IsSpace(current) || unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			if !separated && count < threadTitleLimit {
				builder.WriteByte(' ')
				count++
			}
			separated = true
			continue
		}
		if count >= threadTitleLimit {
			break
		}
		builder.WriteRune(current)
		count++
		separated = false
	}
	return strings.TrimSpace(builder.String())
}
