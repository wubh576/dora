package codex

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestThreadTitleReaderUsesCodexTaskName(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "state_5.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO threads (id, title, name) VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?), (?, ?, ?)`,
		"thread-title", "用户的第一条 prompt", "",
		"thread-name", "原标题", "  用户重命名\n后的任务  ",
		"thread-fallback", "清洗后回退的原标题", "\n\t\u200b\u001b",
		"thread-other", "其他任务", "",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenThreadTitleReader(ctx, []string{home})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	titles, err := reader.Titles(ctx, []string{"thread-name", "thread-title", "thread-fallback", "missing", "thread-title"})
	if err != nil {
		t.Fatal(err)
	}
	if len(titles) != 3 || titles["thread-title"] != "用户的第一条 prompt" ||
		titles["thread-name"] != "用户重命名 后的任务" || titles["thread-fallback"] != "清洗后回退的原标题" {
		t.Fatalf("任务标题读取错误: %+v", titles)
	}
}

func TestThreadTitleReaderCleansAndBoundsTitle(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	value := "第一行\n\t第二行\u200b\u001b" + strings.Repeat("界", 200)
	if _, err := db.Exec(`INSERT INTO threads (id, title, name) VALUES (?, ?, '')`, "thread", value); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenThreadTitleReader(ctx, []string{home})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	titles, err := reader.Titles(ctx, []string{"thread"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(titles["thread"], "第一行 第二行") || utf8.RuneCountInString(titles["thread"]) != threadTitleLimit {
		t.Fatalf("标题清洗或长度错误: %q", titles["thread"])
	}
}

func TestThreadTitleReaderDoesNotCreateMissingCodexState(t *testing.T) {
	home := t.TempDir()
	reader, err := OpenThreadTitleReader(context.Background(), []string{home})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	titles, err := reader.Titles(context.Background(), []string{"thread"})
	if err != nil || len(titles) != 0 {
		t.Fatalf("缺失状态库返回错误: %+v, %v", titles, err)
	}
	if _, err := os.Stat(filepath.Join(home, "state_5.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("只读 reader 创建了 Codex 状态库: %v", err)
	}
}
