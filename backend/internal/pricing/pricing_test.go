package pricing

import (
	"math"
	"testing"

	"github.com/wubh576/dora/backend/internal/domain"
)

func TestDefaultCatalogMatchesSupportedModels(t *testing.T) {
	tests := []struct {
		model      string
		input      float64
		cached     float64
		cacheWrite float64
		output     float64
	}{
		{model: "gpt-5.6-sol", input: 5, cached: 0.5, cacheWrite: 6.25, output: 30},
		{model: "gpt-5.6", input: 5, cached: 0.5, cacheWrite: 6.25, output: 30},
		{model: "gpt-5.6-sol-2026-07-09", input: 5, cached: 0.5, cacheWrite: 6.25, output: 30},
		{model: "gpt-5.6-terra", input: 2, cached: 0.2, cacheWrite: 2.5, output: 12},
		{model: "gpt-5.6-luna", input: 0.2, cached: 0.02, cacheWrite: 0.25, output: 1.2},
		{model: "gpt-5.5", input: 5, cached: 0.5, cacheWrite: 5, output: 30},
		{model: "gpt-5.4", input: 2.5, cached: 0.25, cacheWrite: 2.5, output: 15},
		{model: "gpt-5.3-codex", input: 1.75, cached: 0.175, cacheWrite: 1.75, output: 14},
	}
	for _, test := range tests {
		price, ok := Default.match(test.model)
		if !ok {
			t.Fatalf("模型 %q 未匹配默认定价目录", test.model)
		}
		if price.InputUSDPerMTok != test.input ||
			price.CachedInputUSDPerMTok != test.cached ||
			price.CacheWriteUSDPerMTok != test.cacheWrite ||
			price.OutputUSDPerMTok != test.output {
			t.Fatalf("模型 %q 定价错误: %+v", test.model, price)
		}
	}
	if _, ok := Default.match("codex-auto-review"); ok {
		t.Fatal("非公开模型不应套用默认价格")
	}
}

func TestEstimatePricesTokenClassesAndReportsUnknownCoverage(t *testing.T) {
	estimate, err := Default.Estimate([]domain.UsageEvent{
		{
			Model:                    "gpt-5.6-sol",
			InputTokens:              1_000_000,
			CachedInputTokens:        1_000_000,
			CacheCreationInputTokens: 1_000_000,
			OutputTokens:             1_000_000,
			ReasoningOutputTokens:    1_000_000,
			TotalTokens:              5_000_000,
		},
		{
			Model:       "codex-auto-review",
			TotalTokens: 1_000_000,
		},
	})
	if err != nil {
		t.Fatalf("Estimate() 失败: %v", err)
	}
	if !closeEnough(estimate.EstimatedUSD, 71.75) {
		t.Fatalf("estimated USD = %f，期望 71.75", estimate.EstimatedUSD)
	}
	if estimate.PricedTokens != 5_000_000 || estimate.UnpricedTokens != 1_000_000 {
		t.Fatalf("定价覆盖 token 错误: %+v", estimate)
	}
	if !closeEnough(estimate.Coverage, 5.0/6.0) {
		t.Fatalf("coverage = %f，期望 %f", estimate.Coverage, 5.0/6.0)
	}
	if len(estimate.UnpricedModels) != 1 || estimate.UnpricedModels[0] != "codex-auto-review" {
		t.Fatalf("未定价模型错误: %+v", estimate.UnpricedModels)
	}
}

func TestEstimateUsesUncachedInputRateForPre56CacheCreation(t *testing.T) {
	estimate, err := Default.Estimate([]domain.UsageEvent{{
		Model:                    "gpt-5.5",
		CacheCreationInputTokens: 1_000_000,
		TotalTokens:              1_000_000,
	}})
	if err != nil {
		t.Fatalf("Estimate() 失败: %v", err)
	}
	if !closeEnough(estimate.EstimatedUSD, 5) {
		t.Fatalf("GPT-5.5 cache creation cost = %f，期望 5", estimate.EstimatedUSD)
	}
}

func TestEstimateKeepsUnclassifiedTokensUnpriced(t *testing.T) {
	estimate, err := Default.Estimate([]domain.UsageEvent{{
		Model:       "gpt-5.4",
		TotalTokens: 42,
	}})
	if err != nil {
		t.Fatalf("Estimate() 失败: %v", err)
	}
	if estimate.EstimatedUSD != 0 || estimate.PricedTokens != 0 || estimate.UnpricedTokens != 42 {
		t.Fatalf("total-only token 不应被猜测分类: %+v", estimate)
	}
}

func TestCatalogUsesLongestSnapshotPrefix(t *testing.T) {
	catalog, err := parseCatalog([]byte(`{
		"version": 1,
		"currency": "USD",
		"unitTokens": 1000000,
		"checkedAt": "2026-07-31",
		"sourceUrl": "https://developers.openai.com/api/docs/models/compare",
		"basis": "test",
		"models": [
			{
				"id": "broad",
				"aliases": [],
				"snapshotPrefixes": ["gpt-"],
				"inputUsdPerMTok": 1,
				"cachedInputUsdPerMTok": 1,
				"cacheWriteUsdPerMTok": 1,
				"outputUsdPerMTok": 1,
				"sourceUrl": "https://developers.openai.com/api/docs/models"
			},
			{
				"id": "specific",
				"aliases": [],
				"snapshotPrefixes": ["gpt-5.6-sol-"],
				"inputUsdPerMTok": 2,
				"cachedInputUsdPerMTok": 2,
				"cacheWriteUsdPerMTok": 2,
				"outputUsdPerMTok": 2,
				"sourceUrl": "https://developers.openai.com/api/docs/models/gpt-5.6-sol"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("parseCatalog() 失败: %v", err)
	}
	price, ok := catalog.match("gpt-5.6-sol-2026-07-09")
	if !ok || price.ID != "specific" {
		t.Fatalf("最长前缀匹配错误: %+v, ok=%v", price, ok)
	}
}

func closeEnough(actual, expected float64) bool {
	return math.Abs(actual-expected) < 0.0000001
}
