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
		cache5m    float64
		cache1h    float64
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
		{model: "claude-opus-4-8", input: 5, cached: 0.5, cache5m: 6.25, cache1h: 10, output: 25},
		{model: "claude-opus-4-7", input: 5, cached: 0.5, cache5m: 6.25, cache1h: 10, output: 25},
		{model: "claude-opus-4-6", input: 5, cached: 0.5, cache5m: 6.25, cache1h: 10, output: 25},
		{model: "claude-opus-4-5", input: 5, cached: 0.5, cache5m: 6.25, cache1h: 10, output: 25},
		{model: "claude-sonnet-4-6", input: 3, cached: 0.3, cache5m: 3.75, cache1h: 6, output: 15},
		{model: "claude-sonnet-4-5-20250929", input: 3, cached: 0.3, cache5m: 3.75, cache1h: 6, output: 15},
		{model: "claude-haiku-4-5-20251001", input: 1, cached: 0.1, cache5m: 1.25, cache1h: 2, output: 5},
	}
	for _, test := range tests {
		price, ok := Default.match(test.model)
		if !ok {
			t.Fatalf("模型 %q 未匹配默认定价目录", test.model)
		}
		if price.InputUSDPerMTok != test.input ||
			price.CachedInputUSDPerMTok != test.cached ||
			price.CacheWriteUSDPerMTok != test.cacheWrite ||
			price.CacheWrite5mUSDPerMTok != test.cache5m ||
			price.CacheWrite1hUSDPerMTok != test.cache1h ||
			price.OutputUSDPerMTok != test.output {
			t.Fatalf("模型 %q 定价错误: %+v", test.model, price)
		}
	}
	if _, ok := Default.match("codex-auto-review"); ok {
		t.Fatal("非公开模型不应套用默认价格")
	}
	for _, model := range []string{
		"claude-opus-4-8-custom",
		"claude-sonnet-4-6-preview",
		"claude-haiku-4-5-custom",
		"claude-haiku-4-5-20251001-custom",
	} {
		if _, ok := Default.match(model); ok {
			t.Fatalf("非官方 Claude 模型 ID %q 不应套用 Anthropic 价格", model)
		}
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
	if len(estimate.Sources) != 1 || estimate.Sources[0].Label != "OpenAI 官方定价" {
		t.Fatalf("定价来源错误: %+v", estimate.Sources)
	}
}

func TestEstimateMatchesModelPricingIndependentOfAgentSource(t *testing.T) {
	estimate, err := Default.Estimate([]domain.UsageEvent{
		{
			Source: domain.CodexSource, Model: "claude-opus-4-8",
			InputTokens: 1_000_000, CachedInputTokens: 1_000_000,
			CacheCreationInputTokens: 1_000_000, CacheCreation5mTokens: 400_000, CacheCreation1hTokens: 600_000,
			OutputTokens: 1_000_000, ReasoningOutputTokens: 1_000_000, TotalTokens: 5_000_000,
		},
		{
			Source: domain.ClaudeCodeSource, Model: "gpt-5.4",
			InputTokens: 1_000_000, TotalTokens: 1_000_000,
		},
		{
			Source: domain.CodexSource, Model: "gpt-5.3-codex",
			InputTokens: 1_000_000, TotalTokens: 1_000_000,
		},
		{
			Source: domain.ClaudeCodeSource, Model: "kimi-future-model",
			InputTokens: 1_000_000, TotalTokens: 1_000_000,
		},
	})
	if err != nil {
		t.Fatalf("Estimate() 失败: %v", err)
	}
	if !closeEnough(estimate.EstimatedUSD, 68.25) {
		t.Fatalf("跨 Agent 模型定价 = %f，期望 68.25", estimate.EstimatedUSD)
	}
	if estimate.PricedTokens != 7_000_000 || estimate.UnpricedTokens != 1_000_000 {
		t.Fatalf("跨 Agent 定价覆盖错误: %+v", estimate)
	}
	if len(estimate.Sources) != 2 || estimate.Sources[0].Label != "Anthropic 官方定价" || estimate.Sources[1].Label != "OpenAI 官方定价" {
		t.Fatalf("多厂商定价来源错误: %+v", estimate.Sources)
	}
	if len(estimate.UnpricedModels) != 1 || estimate.UnpricedModels[0] != "kimi-future-model" {
		t.Fatalf("未知第三方模型不应套用 Agent 价格: %+v", estimate.UnpricedModels)
	}
}

func TestEstimateKeepsClaudeCacheWriteWithoutDurationUnpriced(t *testing.T) {
	estimate, err := Default.Estimate([]domain.UsageEvent{{
		Source: domain.ClaudeCodeSource, Model: "claude-haiku-4-5",
		InputTokens: 1_000_000, CacheCreationInputTokens: 1_000_000, TotalTokens: 2_000_000,
	}})
	if err != nil {
		t.Fatalf("Estimate() 失败: %v", err)
	}
	if !closeEnough(estimate.EstimatedUSD, 1) || estimate.PricedTokens != 1_000_000 || estimate.UnpricedTokens != 1_000_000 {
		t.Fatalf("缺少缓存时长的 Claude token 被错误定价: %+v", estimate)
	}
}

func TestCatalogSupportsThirdPartyModelsWithPartialTokenPricing(t *testing.T) {
	catalog, err := parseCatalog([]byte(`{
		"version": 2,
		"currency": "USD",
		"unitTokens": 1000000,
		"checkedAt": "2026-08-01",
		"basis": "fixture",
		"models": [{
			"id": "third-party-coder",
			"aliases": [],
			"snapshotPrefixes": [],
			"inputUsdPerMTok": 1,
			"outputUsdPerMTok": 3,
			"sourceLabel": "第三方厂商官方定价",
			"sourceUrl": "https://pricing.vendor.example/models"
		}]
	}`))
	if err != nil {
		t.Fatalf("第三方模型目录解析失败: %v", err)
	}
	estimate, err := catalog.Estimate([]domain.UsageEvent{{
		Source: domain.CodexSource, Model: "third-party-coder",
		InputTokens: 1_000_000, CachedInputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 3_000_000,
	}})
	if err != nil {
		t.Fatalf("第三方模型估算失败: %v", err)
	}
	if !closeEnough(estimate.EstimatedUSD, 4) || estimate.PricedTokens != 2_000_000 || estimate.UnpricedTokens != 1_000_000 {
		t.Fatalf("第三方模型部分定价错误: %+v", estimate)
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
				"sourceLabel": "OpenAI",
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
				"sourceLabel": "OpenAI",
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
