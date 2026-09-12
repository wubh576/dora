package workbuddyhooks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/wubh576/dora/backend/internal/attention"
	_ "modernc.org/sqlite"
)

// TitleReader 只读取存活任务的标题；延迟打开以支持 Dora 启动后首次安装 WorkBuddy。
type TitleReader struct {
	path string
	db   *sql.DB
}

func NewTitleReader(home string) *TitleReader {
	return &TitleReader{path: filepath.Join(home, "workbuddy.db")}
}

func (r *TitleReader) Titles(ctx context.Context, ids []string) (map[string]string, error) {
	titles := make(map[string]string)
	if len(ids) == 0 {
		return titles, nil
	}
	if r.db == nil {
		if _, err := os.Stat(r.path); errors.Is(err, os.ErrNotExist) {
			return titles, nil
		} else if err != nil {
			return nil, fmt.Errorf("检查 WorkBuddy 标题数据库: %w", err)
		}
		readURL := &url.URL{Scheme: "file", Path: r.path}
		q := readURL.Query()
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
		q.Add("_pragma", "busy_timeout(1000)")
		readURL.RawQuery = q.Encode()
		db, err := sql.Open("sqlite", readURL.String())
		if err != nil {
			return nil, fmt.Errorf("打开 WorkBuddy 标题数据库: %w", err)
		}
		db.SetMaxOpenConns(1)
		r.db = db
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT id, substr(COALESCE(custom_title, ''), 1, 240), substr(COALESCE(title, ''), 1, 240) FROM sessions WHERE deleted_at IS NULL AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 WorkBuddy 标题: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, custom, title string
		if err := rows.Scan(&id, &custom, &title); err != nil {
			return nil, err
		}
		value := attention.CleanSessionName(custom)
		if value == "" {
			value = attention.CleanSessionName(title)
		}
		if value != "" {
			titles[id] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return titles, nil
}
func (r *TitleReader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
