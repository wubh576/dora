package codex

import (
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestReconcileDedupAndInheritedReplay(t *testing.T) {
	parent := testEvent("parent", "", "known-parent", "replay-a", false, 10)
	childCopy := testEvent("child", "parent", "unknown-child", "replay-a", true, 10)
	childNew := testEvent("child", "parent", "child-new", "replay-new", false, 7)

	result := Reconcile([]domain.UsageEvent{parent, childCopy, childNew})
	assertEventKeys(t, result, "known-parent", "child-new")
}

func TestReconcileForkCompleteCopyCountsOnce(t *testing.T) {
	parent := testEvent("parent", "", "copied-tuple", "copied-replay", false, 10)
	child := testEvent("child", "parent", "copied-tuple", "copied-replay", true, 10)
	child.OccurredAt = parent.OccurredAt.Add(time.Hour)

	result := Reconcile([]domain.UsageEvent{parent, child})
	if len(result) != 1 {
		t.Fatalf("fork 完整复制计数 = %d，期望 1", len(result))
	}
}

func TestReconcileKeepsInheritedWhenParentMissing(t *testing.T) {
	child := testEvent("child", "missing", "child", "replay-a", true, 10)
	result := Reconcile([]domain.UsageEvent{child})
	assertEventKeys(t, result, "child")
}

func TestReconcileKeepsInheritedOnLineageCycle(t *testing.T) {
	first := testEvent("first", "second", "first-event", "same", true, 10)
	second := testEvent("second", "first", "second-event", "same", false, 10)
	result := Reconcile([]domain.UsageEvent{first, second})
	assertEventKeys(t, result, "first-event", "second-event")
}

func TestReconcileKeepsInheritedOnLineageConflict(t *testing.T) {
	child := testEvent("child", "parent-a", "child-inherited", "same", true, 10)
	conflict := testEvent("child", "parent-b", "child-real", "other", false, 4)
	parentA := testEvent("parent-a", "", "parent-a", "same", false, 10)
	parentB := testEvent("parent-b", "", "parent-b", "same", false, 10)

	result := Reconcile([]domain.UsageEvent{child, conflict, parentA, parentB})
	assertEventKeys(t, result, "child-inherited", "child-real", "parent-a", "parent-b")
}

func TestReconcileUsesLargestWins(t *testing.T) {
	empty := testEvent("", "", "same-key", "", false, 0)
	complete := testEvent("", "", "same-key", "", false, 12)
	result := Reconcile([]domain.UsageEvent{empty, complete})
	if len(result) != 1 || result[0].DetailTotal() != 12 {
		t.Fatalf("largest-wins 结果不正确: %+v", result)
	}
}

func TestUsageKeyDoesNotCrossCodexHomes(t *testing.T) {
	usage := normalizedUsage{Input: 10, Output: 2, ReportedTotal: 12, Total: 12}
	first := testEvent("", "", usageKey("home-a", "gpt", usage, usage), "", false, 12)
	second := testEvent("", "", usageKey("home-b", "gpt", usage, usage), "", false, 12)
	result := Reconcile([]domain.UsageEvent{first, second})
	if len(result) != 2 {
		t.Fatalf("不同 Codex home 被错误去重: %+v", result)
	}
}

func TestInheritedReplayDoesNotCrossCodexHomes(t *testing.T) {
	parentUsage := normalizedUsage{Input: 10, Output: 2, ReportedTotal: 12, Total: 12}
	childUsage := normalizedUsage{Input: 10, Output: 2, ReportedTotal: 12, Total: 12}
	parent := testEvent(
		rolloutKey("home-a", "parent"),
		"",
		"parent",
		replayKey("home-a", parentUsage, parentUsage),
		false,
		12,
	)
	child := testEvent(
		rolloutKey("home-b", "child"),
		rolloutKey("home-b", "parent"),
		"child",
		replayKey("home-b", childUsage, childUsage),
		true,
		12,
	)
	result := Reconcile([]domain.UsageEvent{parent, child})
	assertEventKeys(t, result, "parent", "child")
}

func TestSameDedupKeyOnlyCountsOnce(t *testing.T) {
	event := testEvent("", "", "duplicate", "", false, 8)
	copyWithNewTimestamp := event
	copyWithNewTimestamp.OccurredAt = event.OccurredAt.Add(time.Minute)
	result := Reconcile([]domain.UsageEvent{event, copyWithNewTimestamp})
	if len(result) != 1 {
		t.Fatalf("重复 token_count 计数 = %d，期望 1", len(result))
	}
}

func testEvent(rollout, parent, key, fingerprint string, inherited bool, detail int64) domain.UsageEvent {
	return domain.UsageEvent{
		Source:            domain.CodexSource,
		DedupKey:          key,
		OccurredAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Model:             "gpt",
		Project:           "dora",
		InputTokens:       detail,
		TotalTokens:       detail,
		RolloutKey:        rollout,
		ParentRolloutKey:  parent,
		ReplayFingerprint: fingerprint,
		InheritedReplay:   inherited,
	}
}

func assertEventKeys(t *testing.T, events []domain.UsageEvent, expected ...string) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("事件数 = %d，期望 %d；事件 = %+v", len(events), len(expected), events)
	}
	actual := make(map[string]struct{}, len(events))
	for _, event := range events {
		actual[event.DedupKey] = struct{}{}
	}
	for _, key := range expected {
		if _, exists := actual[key]; !exists {
			t.Fatalf("缺少事件 %q；实际 = %+v", key, events)
		}
	}
}
