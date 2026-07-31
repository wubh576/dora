package scan

import (
	"compress/gzip"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
	_ "modernc.org/sqlite"
)

func TestScannerFullIncrementalAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "provider", "codex", "testdata", "basic.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	if err := os.WriteFile(sessionPath, fixture, 0o600); err != nil {
		t.Fatalf("写入 session fixture 失败: %v", err)
	}

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})

	first, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if first.Mode != "full" || first.EventsSeen != 2 || first.EventsStored != 2 {
		t.Fatalf("首次扫描结果不正确: %+v", first)
	}

	second, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("第二次扫描失败: %v", err)
	}
	if second.Mode != "incremental" || second.EventsSeen != 0 || second.EventsStored != 2 {
		t.Fatalf("未变化扫描结果不正确: %+v", second)
	}

	newEvent := []byte(`{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":24},"total_token_usage":{"input_tokens":170,"cached_input_tokens":55,"cache_write_input_tokens":10,"output_tokens":34,"reasoning_output_tokens":8,"total_tokens":204}}}}` + "\n")
	appendFile(t, sessionPath, newEvent)

	third, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("增量扫描失败: %v", err)
	}
	if third.Mode != "incremental" || third.EventsSeen != 1 || third.EventsStored != 3 {
		t.Fatalf("增量扫描结果不正确: %+v", third)
	}

	appendFile(t, sessionPath, newEvent)
	fourth, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("重复增量扫描失败: %v", err)
	}
	if fourth.EventsSeen != 1 || fourth.EventsStored != 3 {
		t.Fatalf("重复事件未去重: %+v", fourth)
	}

	fullAgain, err := scanner.Scan(ctx, true)
	if err != nil {
		t.Fatalf("重复全量扫描失败: %v", err)
	}
	if fullAgain.Mode != "full" || fullAgain.EventsStored != 3 {
		t.Fatalf("重复全量扫描不幂等: %+v", fullAgain)
	}
}

func TestScannerLastOnlyThenCumulativeUsesPersistedBaseline(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "total-only.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	initial := strings.TrimPrefix(`
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"total-only"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3,"cached_input_tokens":1,"cache_write_input_tokens":1,"output_tokens":2,"reasoning_output_tokens":1,"total_tokens":5}}}}
`, "\n")
	if err := os.WriteFile(sessionPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("写入 total-only fixture 失败: %v", err)
	}

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次 total-only 扫描失败: %v", err)
	}
	assertStoredTokenBreakdown(t, store, 5, 1)

	for index, line := range []string{
		`{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":6,"cached_input_tokens":2,"cache_write_input_tokens":2,"output_tokens":4,"reasoning_output_tokens":2,"total_tokens":10}}}}` + "\n",
		`{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":12,"cached_input_tokens":4,"cache_write_input_tokens":4,"output_tokens":8,"reasoning_output_tokens":4,"total_tokens":20}}}}` + "\n",
		`{"timestamp":"2026-01-02T03:04:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":18,"cached_input_tokens":6,"cache_write_input_tokens":6,"output_tokens":12,"reasoning_output_tokens":6,"total_tokens":30}}}}` + "\n",
	} {
		appendFile(t, sessionPath, []byte(line))
		report, err := scanner.Scan(ctx, false)
		if err != nil {
			t.Fatalf("第 %d 次 total-only 增量扫描失败: %v", index+1, err)
		}
		if report.Mode != "incremental" || report.EventsSeen != 1 {
			t.Fatalf("第 %d 次 total-only 增量结果错误: %+v", index+1, report)
		}
		assertStoredTokenBreakdown(
			t,
			store,
			int64((index+1)*10),
			int64((index+1)*2),
		)
	}
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("未变化 total-only 扫描失败: %v", err)
	}
	assertStoredTokenBreakdown(t, store, 30, 6)
	if _, err := scanner.Scan(ctx, true); err != nil {
		t.Fatalf("total-only 全量校验失败: %v", err)
	}
	assertStoredTokenBreakdown(t, store, 30, 6)
}

func TestScannerFailurePreservesPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "provider", "codex", "testdata", "basic.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	if err := os.WriteFile(sessionPath, fixture, 0o600); err != nil {
		t.Fatalf("写入 session fixture 失败: %v", err)
	}

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}

	appendFile(t, sessionPath, []byte(`{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}`+"\n"))
	if _, err := scanner.Scan(ctx, false); err == nil {
		t.Fatal("无效 token 扫描未失败")
	} else if strings.Contains(err.Error(), sessionPath) {
		t.Fatalf("扫描错误泄漏 transcript 路径: %q", err)
	}

	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取旧 generation 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("失败扫描破坏旧 generation，事件数 = %d", len(events))
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 provider 状态失败: %v", err)
	}
	if state.Status != "error" || state.LastError == "" {
		t.Fatalf("失败状态不明确: %+v", state)
	}
	if strings.Contains(state.LastError, sessionPath) || !strings.Contains(state.LastError, "不能为负数") {
		t.Fatalf("失败状态未脱敏或缺少原因: %q", state.LastError)
	}
}

func TestScannerTruncateDuringParsePreservesPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}

	var truncateErr error
	scanner.beforeParse = func(file codex.File) {
		truncateErr = os.Truncate(file.Path, 16)
	}
	if _, err := scanner.Scan(ctx, true); err == nil {
		t.Fatal("扫描计划后的文件截断未使扫描失败")
	}
	if truncateErr != nil {
		t.Fatalf("截断扫描 fixture 失败: %v", truncateErr)
	}

	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取旧 generation 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("并发截断破坏旧 generation，事件数 = %d", len(events))
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 provider 状态失败: %v", err)
	}
	if state.Status != "error" || strings.Contains(state.LastError, sessionPath) {
		t.Fatalf("并发截断状态不明确: %+v", state)
	}
}

func TestScannerEmptyHomeSucceeds(t *testing.T) {
	ctx := context.Background()
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()

	report, err := New(store, []string{t.TempDir()}).Scan(ctx, false)
	if err != nil {
		t.Fatalf("空数据扫描失败: %v", err)
	}
	if report.Mode != "full" || report.FilesSeen != 0 || report.EventsStored != 0 {
		t.Fatalf("空数据扫描结果不正确: %+v", report)
	}
	state, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取空数据状态失败: %v", err)
	}
	if state.Status != "ready" || state.FilesSeen != 0 {
		t.Fatalf("空数据状态不明确: %+v", state)
	}
}

func TestScannerCompletesPreviouslyIncompleteLine(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	partial := `{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":7`
	appendFile(t, sessionPath, []byte(partial))

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	first, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("含残行首次扫描失败: %v", err)
	}
	if first.EventsStored != 2 {
		t.Fatalf("残行被提前解析: %+v", first)
	}

	appendFile(t, sessionPath, []byte(`,"output_tokens":1,"total_tokens":8},"total_token_usage":{"input_tokens":157,"output_tokens":31,"total_tokens":188}}}}`+"\n"))
	second, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("补全残行增量扫描失败: %v", err)
	}
	if second.Mode != "incremental" || second.EventsSeen != 1 || second.EventsStored != 3 {
		t.Fatalf("残行补全结果不正确: %+v", second)
	}
}

func TestScannerRebuildsOnTruncateAndFileDeletion(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	extraPath := filepath.Join(home, "sessions", "extra.jsonl")
	extra := `{"timestamp":"2026-01-03T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-extra","last_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10},"total_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10}}}}` + "\n"
	if err := os.WriteFile(extraPath, []byte(extra), 0o600); err != nil {
		t.Fatalf("写入额外 fixture 失败: %v", err)
	}

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	first, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if first.EventsStored != 3 {
		t.Fatalf("首次事件数 = %d，期望 3", first.EventsStored)
	}

	replacement := `{"timestamp":"2026-01-04T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-replaced","last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"total_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(replacement), 0o600); err != nil {
		t.Fatalf("截断 fixture 失败: %v", err)
	}
	truncated, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("截断后扫描失败: %v", err)
	}
	if truncated.Mode != "full" || truncated.EventsStored != 2 {
		t.Fatalf("截断未触发全量重建: %+v", truncated)
	}

	if err := os.Remove(extraPath); err != nil {
		t.Fatalf("删除 fixture 失败: %v", err)
	}
	deleted, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("删除后扫描失败: %v", err)
	}
	if deleted.Mode != "full" || deleted.EventsStored != 1 {
		t.Fatalf("删除文件未修复陈旧事件: %+v", deleted)
	}
}

func TestScannerRebuildsWhenDeletedFileIsReplacedAtSameCount(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	aPath := filepath.Join(sessionDir, "a.jsonl")
	bPath := filepath.Join(sessionDir, "b.jsonl")
	aFixture := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-a","last_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10},"total_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10}}}}` + "\n"
	bFixture := `{"timestamp":"2026-01-03T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-b","last_token_usage":{"input_tokens":18,"output_tokens":2,"total_tokens":20},"total_token_usage":{"input_tokens":18,"output_tokens":2,"total_tokens":20}}}}` + "\n"
	if err := os.WriteFile(aPath, []byte(aFixture), 0o600); err != nil {
		t.Fatalf("写入 A fixture 失败: %v", err)
	}
	if err := os.WriteFile(bPath, []byte(bFixture), 0o600); err != nil {
		t.Fatalf("写入 B fixture 失败: %v", err)
	}

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	first, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if first.Mode != "full" || first.FilesSeen != 2 || first.EventsStored != 2 {
		t.Fatalf("首次 A+B 扫描结果错误: %+v", first)
	}

	if err := os.Remove(bPath); err != nil {
		t.Fatalf("删除 B fixture 失败: %v", err)
	}
	cPath := filepath.Join(sessionDir, "c.jsonl")
	cFixture := `{"timestamp":"2026-01-04T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-c","last_token_usage":{"input_tokens":27,"output_tokens":3,"total_tokens":30},"total_token_usage":{"input_tokens":27,"output_tokens":3,"total_tokens":30}}}}` + "\n"
	if err := os.WriteFile(cPath, []byte(cFixture), 0o600); err != nil {
		t.Fatalf("写入 C fixture 失败: %v", err)
	}

	second, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("A+C 扫描失败: %v", err)
	}
	if second.Mode != "full" || second.FilesSeen != 2 || second.EventsStored != 2 {
		t.Fatalf("同数量文件替换未触发全量重建: %+v", second)
	}
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 A+C usage 失败: %v", err)
	}
	totalsByModel := make(map[string]int64, len(events))
	for _, event := range events {
		totalsByModel[event.Model] += event.TotalTokens
	}
	if len(events) != 2 ||
		totalsByModel["gpt-a"] != 10 ||
		totalsByModel["gpt-c"] != 30 ||
		totalsByModel["gpt-b"] != 0 {
		t.Fatalf("全量重建后仍有陈旧 usage: events=%+v", events)
	}
}

func TestScannerRebuildsWhenCompressedFileChanges(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "usage.jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	fixture := readBasicFixture(t)
	writeGzipFixture(t, sessionPath, fixture)

	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次压缩扫描失败: %v", err)
	}

	extra := []byte(`{"timestamp":"2026-01-03T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-extra","last_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10},"total_token_usage":{"input_tokens":9,"output_tokens":1,"total_tokens":10}}}}` + "\n")
	writeGzipFixture(t, sessionPath, append(fixture, extra...))
	report, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("压缩文件变化扫描失败: %v", err)
	}
	if report.Mode != "full" || report.EventsStored != 3 {
		t.Fatalf("压缩文件变化未触发全量: %+v", report)
	}
}

func TestScannerRebuildsOnReplacementParserChangeAndDailyVerification(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	dbPath := filepath.Join(t.TempDir(), "dora.db")
	store, err := dorasqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	currentTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scanner.now = func() time.Time { return currentTime }
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}

	replacementPath := filepath.Join(filepath.Dir(sessionPath), "replacement.tmp")
	if err := os.WriteFile(replacementPath, readBasicFixture(t), 0o600); err != nil {
		t.Fatalf("写入替换 fixture 失败: %v", err)
	}
	if err := os.Rename(replacementPath, sessionPath); err != nil {
		t.Fatalf("替换 fixture 失败: %v", err)
	}
	replaced, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("替换后扫描失败: %v", err)
	}
	if replaced.Mode != "full" {
		t.Fatalf("文件 identity 变化未触发全量: %+v", replaced)
	}

	external, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库连接失败: %v", err)
	}
	if _, err := external.ExecContext(ctx, "UPDATE source_files SET parser_version = 0"); err != nil {
		t.Fatalf("修改 parser version fixture 失败: %v", err)
	}
	if err := external.Close(); err != nil {
		t.Fatalf("关闭测试数据库连接失败: %v", err)
	}
	parserChanged, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("parser version 变化扫描失败: %v", err)
	}
	if parserChanged.Mode != "full" {
		t.Fatalf("parser version 变化未触发全量: %+v", parserChanged)
	}

	currentTime = currentTime.Add(25 * time.Hour)
	daily, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("每日校验扫描失败: %v", err)
	}
	if daily.Mode != "full" {
		t.Fatalf("24 小时后未执行全量校验: %+v", daily)
	}
}

func TestScannerUnknownOversizedLinePreservesGeneration(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}

	scanner.parser = codex.Parser{MaxLineBytes: 200}
	oversized := `{"timestamp":"2026-01-02T03:04:09Z","type":"response_item","payload":{"type":"message","content":"` + strings.Repeat("x", 512) + `"}}` + "\n"
	appendFile(t, sessionPath, []byte(oversized))
	if _, err := scanner.Scan(ctx, false); err == nil {
		t.Fatal("未知超大行扫描未失败")
	}
	events, err := store.LoadUsageEvents(ctx, domain.CodexSource)
	if err != nil {
		t.Fatalf("读取旧 generation 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("未知超大行破坏旧 generation: %+v", events)
	}
}

func TestScannerSingleflightPreservesForcedFullRequest(t *testing.T) {
	ctx := context.Background()
	home, sessionPath := writeBasicSession(t)
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()
	scanner := New(store, []string{home})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	appendFile(t, sessionPath, []byte(`{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":7,"output_tokens":1,"total_tokens":8},"total_token_usage":{"input_tokens":157,"output_tokens":31,"total_tokens":188}}}}`+"\n"))

	started := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	scanner.beforeRun = func() {
		blockOnce.Do(func() {
			close(started)
			<-release
		})
	}

	incrementalResult := make(chan Report, 1)
	incrementalError := make(chan error, 1)
	go func() {
		report, err := scanner.Scan(ctx, false)
		incrementalResult <- report
		incrementalError <- err
	}()
	<-started

	fullResult := make(chan Report, 1)
	fullError := make(chan error, 1)
	go func() {
		report, err := scanner.Scan(ctx, true)
		fullResult <- report
		fullError <- err
	}()
	close(release)

	incremental := <-incrementalResult
	if err := <-incrementalError; err != nil {
		t.Fatalf("增量 singleflight 失败: %v", err)
	}
	full := <-fullResult
	if err := <-fullError; err != nil {
		t.Fatalf("强制全量 singleflight 失败: %v", err)
	}
	if incremental.Mode != "incremental" || full.Mode != "full" || incremental.RunID == full.RunID {
		t.Fatalf("强制全量请求被增量吞掉: incremental=%+v full=%+v", incremental, full)
	}
}

func writeBasicSession(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	if err := os.WriteFile(sessionPath, readBasicFixture(t), 0o600); err != nil {
		t.Fatalf("写入 session fixture 失败: %v", err)
	}
	return home, sessionPath
}

func readBasicFixture(t *testing.T) []byte {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "provider", "codex", "testdata", "basic.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	return fixture
}

func writeGzipFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 gzip fixture 失败: %v", err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("写入 gzip fixture 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 gzip writer 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 gzip fixture 失败: %v", err)
	}
}

func appendFile(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("打开 append fixture 失败: %v", err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatalf("追加 fixture 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 append fixture 失败: %v", err)
	}
}

func assertStoredTokenBreakdown(
	t *testing.T,
	store *dorasqlite.Store,
	expectedTotal int64,
	expectedEach int64,
) {
	t.Helper()
	events, err := store.LoadUsageEvents(context.Background(), domain.CodexSource)
	if err != nil {
		t.Fatalf("读取 usage events 失败: %v", err)
	}
	var total, input, output, cacheRead, cacheCreation, reasoning int64
	for _, event := range events {
		total += event.TotalTokens
		input += event.InputTokens
		output += event.OutputTokens
		cacheRead += event.CachedInputTokens
		cacheCreation += event.CacheCreationInputTokens
		reasoning += event.ReasoningOutputTokens
	}
	if total != expectedTotal ||
		input != expectedEach ||
		output != expectedEach ||
		cacheRead != expectedEach ||
		cacheCreation != expectedEach ||
		reasoning != expectedEach {
		t.Fatalf(
			"存储 token 分类错误: total=%d input=%d output=%d cache=%d create=%d reasoning=%d；events=%+v",
			total,
			input,
			output,
			cacheRead,
			cacheCreation,
			reasoning,
			events,
		)
	}
}
