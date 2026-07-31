package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestParserReadsUsageModelsCachesAndReasoning(t *testing.T) {
	result := parseFixture(t, "basic.jsonl", ParserState{})
	if len(result.Events) != 3 {
		t.Fatalf("event 数 = %d，期望 3: %+v", len(result.Events), result.Events)
	}
	first := result.Events[0]
	if first.Model != "claude-opus-4-8" || first.Project != "dora" ||
		first.InputTokens != 10 || first.OutputTokens != 2 || first.CachedInputTokens != 4 ||
		first.CacheCreationInputTokens != 2 || first.CacheCreation5mTokens != 1 || first.CacheCreation1hTokens != 1 ||
		first.ReasoningOutputTokens != 4 || first.TotalTokens != 22 {
		t.Fatalf("Anthropic thinking carve 错误: %+v", first)
	}
	second := result.Events[1]
	if second.OutputTokens != 2 || second.ReasoningOutputTokens != 3 || second.TotalTokens != 6 {
		t.Fatalf("原生 reasoning 被重复 carve: %+v", second)
	}
	third := result.Events[2]
	if third.Model != "custom-coder-v2" || third.OutputTokens != 5 || third.ReasoningOutputTokens != 0 {
		t.Fatalf("非 Anthropic 模型被错误 carve: %+v", third)
	}
}

func TestParserLargestWinsAcrossStreamingForkAndSubagent(t *testing.T) {
	stream := parseFixture(t, "streaming.jsonl", ParserState{})
	stateJSON, err := json.Marshal(stream.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"msg-stream", "flush-1", "/tmp/"} {
		if strings.Contains(string(stateJSON), sensitive) {
			t.Fatalf("parser checkpoint 持久化了 session/message/path: %s", stateJSON)
		}
	}
	fork := parseFixture(t, "fork.jsonl", ParserState{})
	events := Reconcile(append(stream.Events, fork.Events...))
	if len(events) != 2 {
		t.Fatalf("stream/fork 重复计数: %+v", events)
	}
	if events[0].TotalTokens != 16 || events[0].InputTokens != 8 || events[0].CacheCreationInputTokens != 1 ||
		events[0].CacheCreation5mTokens != 1 || events[0].CacheCreation1hTokens != 0 {
		t.Fatalf("streaming 未采用 final usage: %+v", events[0])
	}
	if events[1].Model != "gateway-model-x" || events[1].TotalTokens != 5 {
		t.Fatalf("fork 新消息丢失: %+v", events[1])
	}

	otherHome := parseFixtureWithFile(t, "fork.jsonl", File{HomeKey: "other-home", Project: "fixture"}, ParserState{})
	if got := len(Reconcile(append(events, otherHome.Events...))); got != 4 {
		t.Fatalf("不同 config home 被错误合并: %d", got)
	}
}

func TestParserSkipsUsageWithoutMessageID(t *testing.T) {
	content := strings.Join([]string{
		`{"type":"assistant","uuid":"local-only","timestamp":"2026-07-31T10:00:00Z","message":{"role":"assistant","model":"model-x","usage":{"input_tokens":2}}}`,
		`{"type":"assistant","timestamp":"2026-07-31T10:00:01Z","message":{"role":"assistant","model":"model-x","usage":{"input_tokens":9}}}`,
	}, "\n") + "\n"
	result := parseContent(t, content)
	if len(result.Events) != 0 || len(result.Warnings) != 2 {
		t.Fatalf("缺失 ID 保守策略错误: %+v", result)
	}
}

func TestParserPreservesPartialTailAndIncrementalState(t *testing.T) {
	complete := `{"type":"assistant","uuid":"one","timestamp":"2026-07-31T10:00:00Z","message":{"id":"msg-one","role":"assistant","model":"model-a","usage":{"input_tokens":2}}}` + "\n"
	partial := `{"type":"assistant","uuid":"two","timestamp":"2026-07-31T10:00:01Z","message":{"id":"msg-two"`
	path := filepath.Join(t.TempDir(), "partial.jsonl")
	if err := os.WriteFile(path, []byte(complete+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{Path: path, HomeKey: "home", Project: "fixture"}
	result := parsePath(t, file, 0, int64(len(complete+partial)), ParserState{})
	if result.CompleteLineEnd != int64(len(complete)) || len(result.Events) != 1 {
		t.Fatalf("半行 checkpoint 错误: %+v", result)
	}
	if err := os.WriteFile(path, []byte(complete+partial+`,"role":"assistant","model":"model-b","usage":{"output_tokens":3}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	next := parsePath(t, file, result.CompleteLineEnd, info.Size(), result.State)
	events := Reconcile(append(result.Events, next.Events...))
	if len(events) != 2 || events[1].Model != "model-b" || events[1].OutputTokens != 3 {
		t.Fatalf("半行补全增量解析错误: %+v", events)
	}
}

func TestParserRejectsNegativeAndOverflowUsage(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":-1}`,
		`{"input_tokens":9223372036854775807,"output_tokens":1}`,
		`{"cache_creation_input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":0}}`,
		`{"cache_creation":{"ephemeral_5m_input_tokens":-1,"ephemeral_1h_input_tokens":0}}`,
	} {
		content := `{"type":"assistant","uuid":"bad","timestamp":"2026-07-31T10:00:00Z","message":{"id":"bad","role":"assistant","model":"model","usage":` + usage + `}}` + "\n"
		path := filepath.Join(t.TempDir(), "bad.jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		file := File{Path: path, HomeKey: "home", Project: "fixture"}
		if _, err := NewParser().ParseFileSnapshot(context.Background(), file, 0, int64(len(content)), ParserState{}); err == nil {
			t.Fatalf("非法 usage 未报错: %s", usage)
		}
	}
}

func TestParserUsesCacheDurationDetailWhenAggregateIsMissing(t *testing.T) {
	content := `{"type":"assistant","timestamp":"2026-07-31T10:00:00Z","message":{"id":"cache-detail","role":"assistant","model":"claude-sonnet-4-6","usage":{"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":5}}}}` + "\n"
	result := parseContent(t, content)
	if len(result.Events) != 1 {
		t.Fatalf("cache detail event 数错误: %+v", result.Events)
	}
	event := result.Events[0]
	if event.CacheCreationInputTokens != 8 || event.CacheCreation5mTokens != 3 || event.CacheCreation1hTokens != 5 || event.TotalTokens != 8 {
		t.Fatalf("cache detail 归一化错误: %+v", event)
	}
}

func TestReconcilePrefersCacheDurationDetail(t *testing.T) {
	withoutDetail := domain.UsageEvent{
		DedupKey: "same-message", Model: "claude-opus-4-8",
		CacheCreationInputTokens: 8, TotalTokens: 8,
	}
	withDetail := withoutDetail
	withDetail.CacheCreation5mTokens = 3
	withDetail.CacheCreation1hTokens = 5
	for _, events := range [][]domain.UsageEvent{{withoutDetail, withDetail}, {withDetail, withoutDetail}} {
		reconciled := Reconcile(events)
		if len(reconciled) != 1 || reconciled[0].CacheCreation5mTokens != 3 || reconciled[0].CacheCreation1hTokens != 5 {
			t.Fatalf("Reconcile() 丢失 cache duration 明细: %+v", reconciled)
		}
	}
}

func parseFixture(t *testing.T, name string, state ParserState) ParseResult {
	t.Helper()
	return parseFixtureWithFile(t, name, File{HomeKey: "home", Project: "fixture"}, state)
}

func parseFixtureWithFile(t *testing.T, name string, file File, state ParserState) ParseResult {
	t.Helper()
	file.Path = filepath.Join("testdata", name)
	info, err := os.Stat(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	return parsePath(t, file, 0, info.Size(), state)
}

func parseContent(t *testing.T, content string) ParseResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return parsePath(t, File{Path: path, HomeKey: "home", Project: "fixture"}, 0, int64(len(content)), ParserState{})
}

func parsePath(t *testing.T, file File, offset, end int64, state ParserState) ParseResult {
	t.Helper()
	result, err := NewParser().ParseFileSnapshot(context.Background(), file, offset, end, state)
	if err != nil {
		t.Fatalf("ParseFileSnapshot() 失败: %v", err)
	}
	return result
}
