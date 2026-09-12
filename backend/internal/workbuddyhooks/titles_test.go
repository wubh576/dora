package workbuddyhooks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleReaderReadsOnlyRequestedLiveTitles(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	r := NewTitleReader(home)
	defer r.Close()
	if titles, err := r.Titles(ctx, []string{"s"}); err != nil || len(titles) != 0 {
		t.Fatalf("不存在的数据库: %+v %v", titles, err)
	}
	if _, err := os.Stat(filepath.Join(home, "workbuddy.db")); !os.IsNotExist(err) {
		t.Fatal("读标题意外创建数据库")
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "workbuddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY, title TEXT, custom_title TEXT, deleted_at INTEGER);
 INSERT INTO sessions VALUES('s','自动标题',' 自定义\n标题 ',NULL),('fallback','原任务名',NULL,NULL),('deleted','已删除',NULL,1),('other','不应读取',NULL,NULL);`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE sessions SET custom_title=? WHERE id='s'`, " 自定义\n\u200b标题 "); err != nil {
		t.Fatal(err)
	}
	titles, err := r.Titles(ctx, []string{"s", "fallback", "deleted"})
	if err != nil || len(titles) != 2 || titles["s"] != "自定义 标题" || titles["fallback"] != "原任务名" {
		t.Fatalf("%+v %v", titles, err)
	}
	if _, err = r.db.Exec(`UPDATE sessions SET title='不能写入'`); err == nil {
		t.Fatal("标题连接不是只读")
	}
	if _, err = db.Exec(`UPDATE sessions SET custom_title=? WHERE id='s'`, strings.Repeat("界", 200)); err != nil {
		t.Fatal(err)
	}
	titles, err = r.Titles(ctx, []string{"s"})
	if err != nil || titles["s"] != strings.Repeat("界", 120) {
		t.Fatalf("新标题未刷新或未截断: %+v %v", titles, err)
	}
}
