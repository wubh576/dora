package codex

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestParseFileNormalizesRealCodexFields(t *testing.T) {
	path := filepath.Join("testdata", "basic.jsonl")
	result, err := NewParser().ParseFile(context.Background(), File{
		Path:    path,
		HomeKey: "home-a",
	}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(result.Events))
	}

	first := result.Events[0]
	if first.Model != "gpt-5.4" || first.Project != "dora" {
		t.Fatalf("模型/项目 = %q/%q，期望 gpt-5.4/dora", first.Model, first.Project)
	}
	if first.InputTokens != 60 ||
		first.OutputTokens != 15 ||
		first.CachedInputTokens != 30 ||
		first.CacheCreationInputTokens != 10 ||
		first.ReasoningOutputTokens != 5 ||
		first.TotalTokens != 120 {
		t.Fatalf("归一化 token 不正确: %+v", first)
	}
	if first.DedupKey == "" || first.ReplayFingerprint == "" || first.RolloutKey == "" {
		t.Fatalf("稳定标识未生成: %+v", first)
	}
	if result.Events[1].TotalTokens != 60 {
		t.Fatalf("错误累计了 total_token_usage，第二条事件 total = %d，期望 last_token_usage 的 60", result.Events[1].TotalTokens)
	}
}

func TestParseFileFallsBackToTotalUsage(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"fallback","cwd":"/tmp/project"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":12,"cached_input_tokens":2,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":16}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(result.Events))
	}
	event := result.Events[0]
	if event.InputTokens != 10 || event.OutputTokens != 3 || event.CachedInputTokens != 2 || event.ReasoningOutputTokens != 1 || event.TotalTokens != 16 {
		t.Fatalf("fallback 归一化错误: %+v", event)
	}
}

func TestParseFileTotalOnlyUsesCumulativeDelta(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"total-only","cwd":"/tmp/project"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}
{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":16,"output_tokens":4,"total_tokens":20}}}}
{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":24,"output_tokens":6,"total_tokens":30}}}}
{"timestamp":"2026-01-02T03:04:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":24,"output_tokens":6,"total_tokens":30}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if len(result.Events) != 3 {
		t.Fatalf("累计快照事件数 = %d，期望 3", len(result.Events))
	}
	var total int64
	for _, event := range result.Events {
		total += event.TotalTokens
		if event.TotalTokens != 10 {
			t.Fatalf("累计快照增量错误: %+v", event)
		}
	}
	if total != 30 {
		t.Fatalf("累计快照被重复相加: total=%d，期望 30", total)
	}
}

func TestParseFileTotalOnlyPersistsIncrementalBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incremental.jsonl")
	firstContent := strings.TrimPrefix(`
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"total-incremental"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}
`, "\n")
	if err := os.WriteFile(path, []byte(firstContent), 0o600); err != nil {
		t.Fatalf("写入初始累计 fixture 失败: %v", err)
	}
	parser := NewParser()
	first, err := parser.ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("首次 ParseFile() 失败: %v", err)
	}
	stateJSON, err := json.Marshal(first.State)
	if err != nil {
		t.Fatalf("编码 parser state 失败: %v", err)
	}
	var persisted ParserState
	if err := json.Unmarshal(stateJSON, &persisted); err != nil {
		t.Fatalf("解码 parser state 失败: %v", err)
	}
	appendFileContent := `{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":16,"output_tokens":4,"total_tokens":20}}}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("打开累计 fixture 失败: %v", err)
	}
	if _, err := file.WriteString(appendFileContent); err != nil {
		file.Close()
		t.Fatalf("追加累计 fixture 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭累计 fixture 失败: %v", err)
	}

	second, err := parser.ParseFile(
		context.Background(),
		File{Path: path, HomeKey: "home"},
		first.CompleteLineEnd,
		persisted,
	)
	if err != nil {
		t.Fatalf("增量 ParseFile() 失败: %v", err)
	}
	if len(second.Events) != 1 || second.Events[0].TotalTokens != 10 {
		t.Fatalf("增量累计基线未保留: %+v", second.Events)
	}
}

func TestParseFileTotalOnlyPreservesResetAndIncomparableTotal(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"total-reset"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}
{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":16,"output_tokens":4,"total_tokens":20}}}}
{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}}
{"timestamp":"2026-01-02T03:04:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":7}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	var total int64
	for _, event := range result.Events {
		total += event.TotalTokens
	}
	if len(result.Events) != 4 || total != 27 {
		t.Fatalf("reset/缺失字段丢失或重复 token: events=%+v total=%d", result.Events, total)
	}
	last := result.Events[len(result.Events)-1]
	if last.DetailTotal() != 0 || last.ReportedTotalTokens != 2 || last.TotalTokens != 2 {
		t.Fatalf("不可比较明细未仅保留 total 增量: %+v", last)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("reset/缺失字段未给出诊断 warning: %v", result.Warnings)
	}
	reconciled := Reconcile(result.Events)
	var reconciledTotal int64
	for _, event := range reconciled {
		reconciledTotal += event.TotalTokens
	}
	if len(reconciled) != 4 || reconciledTotal != 27 {
		t.Fatalf("reset epoch 被 dedup key 合并: events=%+v total=%d", reconciled, reconciledTotal)
	}
}

func TestParseFileLastUsageAdvancesTotalOnlyBaseline(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"mixed-total"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}
{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"total_token_usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}}
{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":16,"output_tokens":4,"total_tokens":20}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	var total int64
	for _, event := range result.Events {
		total += event.TotalTokens
	}
	if len(result.Events) != 3 || total != 20 || result.Events[2].TotalTokens != 5 {
		t.Fatalf("last usage 未推进累计基线: %+v", result.Events)
	}
}

func TestParseFileLastOnlyBeforeCumulativeAvoidsDoubleCount(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"last-before-total"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}}
{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	var total, input, output int64
	for _, event := range result.Events {
		total += event.TotalTokens
		input += event.InputTokens
		output += event.OutputTokens
	}
	if len(result.Events) != 2 ||
		total != 10 ||
		input != 8 ||
		output != 2 ||
		result.Events[1].TotalTokens != 5 {
		t.Fatalf("last-only 后累计快照重复计数: %+v", result.Events)
	}
}

func TestParseFileClampsSubcategoriesAndKeepsReportedTotal(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"cached_input_tokens":8,"cache_write_input_tokens":4,"output_tokens":2,"reasoning_output_tokens":7,"total_tokens":99}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	event := result.Events[0]
	if event.InputTokens != 0 || event.CachedInputTokens != 5 || event.CacheCreationInputTokens != 0 {
		t.Fatalf("input 子类截断错误: %+v", event)
	}
	if event.OutputTokens != 0 || event.ReasoningOutputTokens != 2 {
		t.Fatalf("output 子类截断错误: %+v", event)
	}
	if event.ReportedTotalTokens != 99 || event.TotalTokens != 99 {
		t.Fatalf("reported total 未保留: %+v", event)
	}
}

func TestParseFileKeepsReportedTotalWithoutDetails(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":42}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	event := result.Events[0]
	if event.DetailTotal() != 0 || event.ReportedTotalTokens != 42 || event.TotalTokens != 42 {
		t.Fatalf("仅 reported total 的事件未保真: %+v", event)
	}
}

func TestTurnContextWithoutValidTimestampClosesInheritedPrefix(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"child","parent_thread_id":"parent"}}
{"timestamp":"not-a-time","type":"turn_context","payload":{"model":"gpt-child"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if result.Events[0].InheritedReplay {
		t.Fatal("turn_context 后的新 usage 不应标记为 inherited replay")
	}
	if result.Events[0].Model != "gpt-child" {
		t.Fatalf("模型 = %q，期望 gpt-child", result.Events[0].Model)
	}
}

func TestParseFileRejectsNegativeToken(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1,"total_tokens":2}}}}
`)
	_, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err == nil || !strings.Contains(err.Error(), "不能为负数") {
		t.Fatalf("ParseFile() 错误 = %v，期望负 token 错误", err)
	}
}

func TestParseFileSkipsSafeOversizedRecord(t *testing.T) {
	payload := strings.Repeat("x", 512)
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"patch_apply_end","output":"`+payload+`"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}}
`)
	parser := Parser{MaxLineBytes: 200}
	result, err := parser.ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if len(result.Events) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("超大安全行处理结果不正确: events=%d warnings=%v", len(result.Events), result.Warnings)
	}
}

func TestParseFileRejectsUnknownOversizedRecord(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"response_item","payload":{"type":"message","content":"`+strings.Repeat("x", 512)+`"}}
`)
	parser := Parser{MaxLineBytes: 200}
	_, err := parser.ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err == nil || !strings.Contains(err.Error(), "未知记录") {
		t.Fatalf("ParseFile() 错误 = %v，期望未知超大记录错误", err)
	}
}

func TestEarlyUnknownEventsUseFirstKnownModel(t *testing.T) {
	path := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"model-switch"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}}
{"timestamp":"2026-01-02T03:04:07Z","type":"turn_context","payload":{"model":"gpt-first"}}
{"timestamp":"2026-01-02T03:04:08Z","type":"turn_context","payload":{"model":"gpt-last"}}
`)
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if result.Events[0].Model != "gpt-first" {
		t.Fatalf("早期事件模型 = %q，期望首个确定模型 gpt-first", result.Events[0].Model)
	}
}

func TestParseFileIgnoresIncompleteTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.jsonl")
	content := "{\"timestamp\":\"2026-01-02T03:04:05Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":3}}}}\n{\"timestamp\":"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 fixture 失败: %v", err)
	}
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "home"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("ParseFile() 失败: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("事件数 = %d，期望 1", len(result.Events))
	}
	if result.CompleteLineEnd >= int64(len(content)) {
		t.Fatalf("完整行 offset = %d，不能包含末尾残行", result.CompleteLineEnd)
	}
}

func TestParseFileSnapshotDefersConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growing.jsonl")
	firstLine := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":3}}}}` + "\n"
	secondLine := `{"timestamp":"2026-01-02T03:04:06Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":4}}}}` + "\n"
	if err := os.WriteFile(path, []byte(firstLine), 0o600); err != nil {
		t.Fatalf("写入初始 snapshot fixture 失败: %v", err)
	}
	snapshotEnd := int64(len(firstLine))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("打开 snapshot fixture 失败: %v", err)
	}
	if _, err := file.WriteString(secondLine); err != nil {
		file.Close()
		t.Fatalf("追加 snapshot fixture 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 snapshot fixture 失败: %v", err)
	}

	parser := NewParser()
	first, err := parser.ParseFileSnapshot(
		context.Background(),
		File{Path: path, HomeKey: "home"},
		0,
		snapshotEnd,
		ParserState{},
	)
	if err != nil {
		t.Fatalf("ParseFileSnapshot() 失败: %v", err)
	}
	if len(first.Events) != 1 || first.CompleteLineEnd != snapshotEnd {
		t.Fatalf("snapshot 读取了并发追加内容: %+v", first)
	}
	second, err := parser.ParseFileSnapshot(
		context.Background(),
		File{Path: path, HomeKey: "home"},
		first.CompleteLineEnd,
		int64(len(firstLine)+len(secondLine)),
		first.State,
	)
	if err != nil {
		t.Fatalf("增量 ParseFileSnapshot() 失败: %v", err)
	}
	if len(second.Events) != 1 || second.Events[0].TotalTokens != 4 {
		t.Fatalf("下一次 snapshot 未读取延后的 append: %+v", second)
	}
}

func TestParseFileSnapshotRejectsTruncatedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.jsonl")
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":3}}}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 snapshot fixture 失败: %v", err)
	}
	snapshotEnd := int64(len(content))
	if err := os.Truncate(path, snapshotEnd/2); err != nil {
		t.Fatalf("截断 snapshot fixture 失败: %v", err)
	}

	_, err := NewParser().ParseFileSnapshot(
		context.Background(),
		File{Path: path, HomeKey: "home"},
		0,
		snapshotEnd,
		ParserState{},
	)
	if err == nil || !strings.Contains(err.Error(), "扫描期间变短") {
		t.Fatalf("截断 snapshot 错误 = %v，期望明确失败", err)
	}
}

func TestDedupKeyIgnoresTimestampPathAndRolloutButIncludesRunningTotal(t *testing.T) {
	first := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"rollout-a"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}}
`)
	second := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"rollout-b"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-02-03T04:05:08Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}}
`)
	third := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"rollout-c"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-02-03T04:05:08Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":110,"output_tokens":20,"total_tokens":130}}}}
`)

	parseKey := func(path string) string {
		t.Helper()
		result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: "same-home"}, 0, ParserState{})
		if err != nil {
			t.Fatalf("ParseFile() 失败: %v", err)
		}
		return result.Events[0].DedupKey
	}
	firstKey := parseKey(first)
	secondKey := parseKey(second)
	thirdKey := parseKey(third)
	if firstKey != secondKey {
		t.Fatal("DedupKey 不应包含时间戳、文件路径或 rollout/session ID")
	}
	if firstKey == thirdKey {
		t.Fatal("DedupKey 必须包含 running total")
	}
}

func TestParseCompressedFiles(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "basic.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}

	tests := []struct {
		name  string
		ext   string
		write func(*testing.T, string, []byte)
	}{
		{name: "gzip", ext: ".jsonl.gz", write: writeGzip},
		{name: "zstd", ext: ".jsonl.zst", write: writeZstd},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage"+test.ext)
			test.write(t, path, source)
			result, err := NewParser().ParseFile(context.Background(), File{
				Path:       path,
				HomeKey:    "home",
				Compressed: true,
			}, 0, ParserState{})
			if err != nil {
				t.Fatalf("ParseFile() 失败: %v", err)
			}
			if len(result.Events) != 2 {
				t.Fatalf("事件数 = %d，期望 2", len(result.Events))
			}
		})
	}
}

func writeJSONL(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.jsonl")
	value := strings.TrimPrefix(content, "\n")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("写入 fixture 失败: %v", err)
	}
	return path
}

func writeGzip(t *testing.T, path string, content []byte) {
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

func writeZstd(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zstd fixture 失败: %v", err)
	}
	writer, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatalf("创建 zstd writer 失败: %v", err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("写入 zstd fixture 失败: %v", err)
	}
	writer.Close()
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 zstd fixture 失败: %v", err)
	}
}
