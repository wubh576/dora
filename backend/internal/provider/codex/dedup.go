package codex

import (
	"fmt"
	"sort"

	"github.com/wubh576/dora/backend/internal/domain"
)

func Reconcile(events []domain.UsageEvent) []domain.UsageEvent {
	deduplicated := make(map[string]domain.UsageEvent, len(events))
	order := make([]string, 0, len(events))
	for _, event := range events {
		existing, found := deduplicated[event.DedupKey]
		if !found {
			deduplicated[event.DedupKey] = event
			order = append(order, event.DedupKey)
			continue
		}
		if event.DetailTotal() > existing.DetailTotal() {
			deduplicated[event.DedupKey] = event
		}
	}

	rollouts := make(map[string]string)
	conflicts := make(map[string]bool)
	for _, event := range deduplicated {
		if event.RolloutKey == "" {
			continue
		}
		parent, found := rollouts[event.RolloutKey]
		if !found {
			rollouts[event.RolloutKey] = event.ParentRolloutKey
		} else if parent != event.ParentRolloutKey {
			conflicts[event.RolloutKey] = true
		}
	}

	ancestorFingerprints := make(map[string]map[string]struct{})
	for _, event := range deduplicated {
		if event.RolloutKey == "" || event.InheritedReplay || event.ReplayFingerprint == "" {
			continue
		}
		if ancestorFingerprints[event.RolloutKey] == nil {
			ancestorFingerprints[event.RolloutKey] = make(map[string]struct{})
		}
		ancestorFingerprints[event.RolloutKey][event.ReplayFingerprint] = struct{}{}
	}

	result := make([]domain.UsageEvent, 0, len(deduplicated))
	for _, key := range order {
		event, exists := deduplicated[key]
		if !exists {
			continue
		}
		if event.InheritedReplay && shouldDropInherited(event, rollouts, conflicts, ancestorFingerprints) {
			continue
		}
		result = append(result, event)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].DedupKey < result[j].DedupKey
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result
}

func shouldDropInherited(
	event domain.UsageEvent,
	rollouts map[string]string,
	conflicts map[string]bool,
	fingerprints map[string]map[string]struct{},
) bool {
	if event.ParentRolloutKey == "" || event.ReplayFingerprint == "" || conflicts[event.RolloutKey] {
		return false
	}

	visited := map[string]struct{}{event.RolloutKey: {}}
	parent := event.ParentRolloutKey
	matched := false
	for parent != "" {
		if _, cycle := visited[parent]; cycle {
			return false
		}
		visited[parent] = struct{}{}
		if conflicts[parent] {
			return false
		}
		next, exists := rollouts[parent]
		if !exists {
			return false
		}
		if _, exists := fingerprints[parent][event.ReplayFingerprint]; exists {
			matched = true
		}
		parent = next
	}
	return matched
}

func usageKey(homeKey, model string, last, total normalizedUsage) string {
	return stableHash(fmt.Sprintf(
		"codex|%s|%s|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d",
		homeKey,
		model,
		last.Input,
		last.Output,
		last.Cached,
		last.CacheCreation,
		last.Reasoning,
		total.Input,
		total.Output,
		total.Cached,
		total.CacheCreation,
		total.Reasoning,
		total.ReportedTotal,
		total.Total,
		last.ReportedTotal,
		last.Total,
	))
}

func replayKey(homeKey string, last, total normalizedUsage) string {
	return stableHash(fmt.Sprintf(
		"codex-replay|%s|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d",
		homeKey,
		last.Input,
		last.Output,
		last.Cached,
		last.CacheCreation,
		last.Reasoning,
		total.Input,
		total.Output,
		total.Cached,
		total.CacheCreation,
		total.Reasoning,
		total.ReportedTotal,
		total.Total,
		last.ReportedTotal,
		last.Total,
	))
}

func rolloutKey(homeKey, rolloutID string) string {
	return stableHash("codex-rollout|" + homeKey + "|" + rolloutID)
}
