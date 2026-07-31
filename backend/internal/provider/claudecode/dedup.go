package claudecode

import (
	"sort"

	"github.com/wubh576/dora/backend/internal/domain"
)

func Reconcile(events []domain.UsageEvent) []domain.UsageEvent {
	deduplicated := make(map[string]domain.UsageEvent, len(events))
	for _, event := range events {
		existing, found := deduplicated[event.DedupKey]
		if !found || prefer(event, existing) {
			deduplicated[event.DedupKey] = event
		}
	}
	result := make([]domain.UsageEvent, 0, len(deduplicated))
	for _, event := range deduplicated {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].DedupKey < result[j].DedupKey
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result
}

func prefer(candidate, existing domain.UsageEvent) bool {
	if candidate.DetailTotal() != existing.DetailTotal() {
		return candidate.DetailTotal() > existing.DetailTotal()
	}
	if candidate.TotalTokens != existing.TotalTokens {
		return candidate.TotalTokens > existing.TotalTokens
	}
	return existing.Model == "unknown" && candidate.Model != "unknown"
}
