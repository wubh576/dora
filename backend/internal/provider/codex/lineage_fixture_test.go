package codex

import (
	"context"
	"testing"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestLineageFixturesRemoveInheritedPrefixAndKeepNewUsage(t *testing.T) {
	parentPath := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"parent"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-parent"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
`)
	childPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"child","parent_thread_id":"parent"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
{"timestamp":"2026-02-03T04:05:08Z","type":"turn_context","payload":{"model":"gpt-child"}}
{"timestamp":"2026-02-03T04:05:09Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6},"total_token_usage":{"input_tokens":15,"output_tokens":3,"total_tokens":18}}}}
`)

	parent := parseFixtureEvents(t, parentPath, "home-a")
	child := parseFixtureEvents(t, childPath, "home-a")
	if !child[0].InheritedReplay || child[0].Model != "unknown" {
		t.Fatalf("child prefix 未正确标记: %+v", child[0])
	}

	result := Reconcile(append(parent, child...))
	if len(result) != 2 {
		t.Fatalf("lineage 去重后事件数 = %d，期望 parent history + child new usage 共 2", len(result))
	}
	assertModels(t, result, "gpt-parent", "gpt-child")
}

func TestLineageFixturesKeepChildWhenParentMissing(t *testing.T) {
	childPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"child","parent_thread_id":"missing"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
`)
	result := Reconcile(parseFixtureEvents(t, childPath, "home-a"))
	if len(result) != 1 {
		t.Fatalf("父文件缺失时 child usage 被误删: %+v", result)
	}
}

func TestLineageFixturesKeepConflictingAndCyclicLineage(t *testing.T) {
	conflictPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"conflict","parent_thread_id":"parent-a","forked_from_id":"parent-b"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"total_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}}
`)
	parser := NewParser()
	conflictResult, err := parser.ParseFile(context.Background(), File{Path: conflictPath, HomeKey: "home-a"}, 0, ParserState{})
	if err != nil {
		t.Fatalf("解析 conflict fixture 失败: %v", err)
	}
	if len(conflictResult.Warnings) == 0 || conflictResult.Events[0].InheritedReplay {
		t.Fatalf("冲突 lineage 未安全保留: %+v", conflictResult)
	}

	firstPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"first","parent_thread_id":"second"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-first","last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"total_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}}
`)
	secondPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"second","parent_thread_id":"first"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"model":"gpt-second","last_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"total_token_usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}}
`)
	cyclic := append(
		parseFixtureEvents(t, firstPath, "home-a"),
		parseFixtureEvents(t, secondPath, "home-a")...,
	)
	if result := Reconcile(cyclic); len(result) != 2 {
		t.Fatalf("循环 lineage 误删 usage: %+v", result)
	}
}

func TestLineageFixturesDoNotCrossHomes(t *testing.T) {
	parentPath := writeJSONL(t, `
{"timestamp":"2026-01-02T03:04:05Z","type":"session_meta","payload":{"id":"parent"}}
{"timestamp":"2026-01-02T03:04:06Z","type":"turn_context","payload":{"model":"gpt-parent"}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
`)
	childPath := writeJSONL(t, `
{"timestamp":"2026-02-03T04:05:06Z","type":"session_meta","payload":{"id":"child","parent_thread_id":"parent"}}
{"timestamp":"2026-02-03T04:05:07Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}}
`)
	events := append(
		parseFixtureEvents(t, parentPath, "home-a"),
		parseFixtureEvents(t, childPath, "home-b")...,
	)
	if result := Reconcile(events); len(result) != 2 {
		t.Fatalf("不同 Codex home 发生 lineage 去重: %+v", result)
	}
}

func parseFixtureEvents(t *testing.T, path, homeKey string) []domain.UsageEvent {
	t.Helper()
	result, err := NewParser().ParseFile(context.Background(), File{Path: path, HomeKey: homeKey}, 0, ParserState{})
	if err != nil {
		t.Fatalf("解析 lineage fixture 失败: %v", err)
	}
	return result.Events
}

func assertModels(t *testing.T, events []domain.UsageEvent, expected ...string) {
	t.Helper()
	models := make(map[string]struct{}, len(events))
	for _, event := range events {
		models[event.Model] = struct{}{}
	}
	for _, model := range expected {
		if _, exists := models[model]; !exists {
			t.Fatalf("缺少模型 %q；事件 = %+v", model, events)
		}
	}
}
