package pricing

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
)

//go:embed catalog.json
var defaultCatalogJSON []byte

var Default = mustParseCatalog(defaultCatalogJSON)

type Catalog struct {
	Version    int
	Currency   string
	UnitTokens int64
	CheckedAt  string
	SourceURL  string
	Basis      string
	Models     []ModelPrice
}

type ModelPrice struct {
	ID                    string   `json:"id"`
	Aliases               []string `json:"aliases"`
	SnapshotPrefixes      []string `json:"snapshotPrefixes"`
	InputUSDPerMTok       float64  `json:"inputUsdPerMTok"`
	CachedInputUSDPerMTok float64  `json:"cachedInputUsdPerMTok"`
	CacheWriteUSDPerMTok  float64  `json:"cacheWriteUsdPerMTok"`
	OutputUSDPerMTok      float64  `json:"outputUsdPerMTok"`
	SourceURL             string   `json:"sourceUrl"`
}

type Estimate struct {
	Currency       string        `json:"currency"`
	EstimatedUSD   float64       `json:"estimatedUsd"`
	PricedTokens   int64         `json:"pricedTokens"`
	UnpricedTokens int64         `json:"unpricedTokens"`
	Coverage       float64       `json:"coverage"`
	Breakdown      CostBreakdown `json:"breakdown"`
	CheckedAt      string        `json:"checkedAt"`
	SourceURL      string        `json:"sourceUrl"`
	Basis          string        `json:"basis"`
	UnpricedModels []string      `json:"unpricedModels"`
}

type CostBreakdown struct {
	InputUSD         float64 `json:"inputUsd"`
	CacheReadUSD     float64 `json:"cacheReadUsd"`
	CacheCreationUSD float64 `json:"cacheCreationUsd"`
	OutputUSD        float64 `json:"outputUsd"`
	ReasoningUSD     float64 `json:"reasoningUsd"`
}

type catalogFile struct {
	Version    int          `json:"version"`
	Currency   string       `json:"currency"`
	UnitTokens int64        `json:"unitTokens"`
	CheckedAt  string       `json:"checkedAt"`
	SourceURL  string       `json:"sourceUrl"`
	Basis      string       `json:"basis"`
	Models     []ModelPrice `json:"models"`
}

func (c Catalog) Estimate(events []domain.UsageEvent) (Estimate, error) {
	result := Estimate{
		Currency:       c.Currency,
		CheckedAt:      c.CheckedAt,
		SourceURL:      c.SourceURL,
		Basis:          c.Basis,
		UnpricedModels: []string{},
	}
	unpricedModels := make(map[string]struct{})
	for _, event := range events {
		detailTokens, err := eventDetailTotal(event)
		if err != nil {
			return Estimate{}, err
		}
		if event.TotalTokens < detailTokens {
			return Estimate{}, errors.New("token 明细大于总量")
		}

		price, priced := c.match(event.Model)
		if !priced {
			if err := addTokens(&result.UnpricedTokens, event.TotalTokens); err != nil {
				return Estimate{}, err
			}
			if event.TotalTokens > 0 {
				unpricedModels[modelName(event.Model)] = struct{}{}
			}
			continue
		}

		if err := addTokens(&result.PricedTokens, detailTokens); err != nil {
			return Estimate{}, err
		}
		gap := event.TotalTokens - detailTokens
		if err := addTokens(&result.UnpricedTokens, gap); err != nil {
			return Estimate{}, err
		}
		if gap > 0 {
			unpricedModels[modelName(event.Model)] = struct{}{}
		}

		result.Breakdown.InputUSD += tokenCost(event.InputTokens, price.InputUSDPerMTok, c.UnitTokens)
		result.Breakdown.CacheReadUSD += tokenCost(event.CachedInputTokens, price.CachedInputUSDPerMTok, c.UnitTokens)
		result.Breakdown.CacheCreationUSD += tokenCost(event.CacheCreationInputTokens, price.CacheWriteUSDPerMTok, c.UnitTokens)
		result.Breakdown.OutputUSD += tokenCost(event.OutputTokens, price.OutputUSDPerMTok, c.UnitTokens)
		// OpenAI 将 reasoning token 按 output token 计费。
		result.Breakdown.ReasoningUSD += tokenCost(event.ReasoningOutputTokens, price.OutputUSDPerMTok, c.UnitTokens)
	}

	result.Breakdown.InputUSD = roundUSD(result.Breakdown.InputUSD)
	result.Breakdown.CacheReadUSD = roundUSD(result.Breakdown.CacheReadUSD)
	result.Breakdown.CacheCreationUSD = roundUSD(result.Breakdown.CacheCreationUSD)
	result.Breakdown.OutputUSD = roundUSD(result.Breakdown.OutputUSD)
	result.Breakdown.ReasoningUSD = roundUSD(result.Breakdown.ReasoningUSD)
	result.EstimatedUSD = roundUSD(
		result.Breakdown.InputUSD +
			result.Breakdown.CacheReadUSD +
			result.Breakdown.CacheCreationUSD +
			result.Breakdown.OutputUSD +
			result.Breakdown.ReasoningUSD,
	)
	coveredTokens, err := sumTokens(result.PricedTokens, result.UnpricedTokens)
	if err != nil {
		return Estimate{}, err
	}
	if coveredTokens > 0 {
		result.Coverage = float64(result.PricedTokens) / float64(coveredTokens)
	}
	for model := range unpricedModels {
		result.UnpricedModels = append(result.UnpricedModels, model)
	}
	sort.Strings(result.UnpricedModels)
	return result, nil
}

func (c Catalog) match(model string) (ModelPrice, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, price := range c.Models {
		if model == strings.ToLower(price.ID) {
			return price, true
		}
		for _, alias := range price.Aliases {
			if model == strings.ToLower(alias) {
				return price, true
			}
		}
	}

	bestLength := 0
	var best ModelPrice
	for _, price := range c.Models {
		for _, prefix := range price.SnapshotPrefixes {
			if normalized := strings.ToLower(prefix); strings.HasPrefix(model, normalized) && len(normalized) > bestLength {
				bestLength = len(normalized)
				best = price
			}
		}
	}
	return best, bestLength > 0
}

func parseCatalog(data []byte) (Catalog, error) {
	var file catalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Catalog{}, fmt.Errorf("解析定价目录: %w", err)
	}
	catalog := Catalog{
		Version:    file.Version,
		Currency:   strings.TrimSpace(file.Currency),
		UnitTokens: file.UnitTokens,
		CheckedAt:  file.CheckedAt,
		SourceURL:  file.SourceURL,
		Basis:      strings.TrimSpace(file.Basis),
		Models:     file.Models,
	}
	if err := validateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func mustParseCatalog(data []byte) Catalog {
	catalog, err := parseCatalog(data)
	if err != nil {
		panic(err)
	}
	return catalog
}

func validateCatalog(catalog Catalog) error {
	if catalog.Version <= 0 || catalog.Currency != "USD" || catalog.UnitTokens != 1_000_000 {
		return errors.New("定价目录版本、货币或 token 单位无效")
	}
	if _, err := time.Parse(time.DateOnly, catalog.CheckedAt); err != nil {
		return errors.New("定价目录核对日期无效")
	}
	if catalog.Basis == "" || !officialURL(catalog.SourceURL) || len(catalog.Models) == 0 {
		return errors.New("定价目录元数据不完整")
	}

	names := make(map[string]struct{})
	prefixes := make(map[string]struct{})
	for _, price := range catalog.Models {
		if strings.TrimSpace(price.ID) == "" || !officialURL(price.SourceURL) {
			return errors.New("模型定价标识或来源无效")
		}
		for _, value := range []float64{
			price.InputUSDPerMTok,
			price.CachedInputUSDPerMTok,
			price.CacheWriteUSDPerMTok,
			price.OutputUSDPerMTok,
		} {
			if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("模型 %s 的价格无效", price.ID)
			}
		}
		for _, name := range append([]string{price.ID}, price.Aliases...) {
			normalized := strings.ToLower(strings.TrimSpace(name))
			if normalized == "" {
				return fmt.Errorf("模型 %s 包含空别名", price.ID)
			}
			if _, exists := names[normalized]; exists {
				return fmt.Errorf("模型别名 %s 重复", name)
			}
			names[normalized] = struct{}{}
		}
		for _, prefix := range price.SnapshotPrefixes {
			normalized := strings.ToLower(strings.TrimSpace(prefix))
			if normalized == "" {
				return fmt.Errorf("模型 %s 包含空 snapshot prefix", price.ID)
			}
			if _, exists := prefixes[normalized]; exists {
				return fmt.Errorf("snapshot prefix %s 重复", prefix)
			}
			prefixes[normalized] = struct{}{}
		}
	}
	return nil
}

func officialURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	return parsed.Host == "developers.openai.com" || parsed.Host == "openai.com"
}

func eventDetailTotal(event domain.UsageEvent) (int64, error) {
	for _, value := range []int64{
		event.InputTokens,
		event.CachedInputTokens,
		event.CacheCreationInputTokens,
		event.OutputTokens,
		event.ReasoningOutputTokens,
		event.TotalTokens,
	} {
		if value < 0 {
			return 0, errors.New("token 不能为负数")
		}
	}
	return sumTokens(
		event.InputTokens,
		event.CachedInputTokens,
		event.CacheCreationInputTokens,
		event.OutputTokens,
		event.ReasoningOutputTokens,
	)
}

func tokenCost(tokens int64, price float64, unit int64) float64 {
	return float64(tokens) / float64(unit) * price
}

func roundUSD(value float64) float64 {
	// 目录价格最多保留三位小数，按单 token 换算后九位小数即可无损表达。
	return math.Round(value*1_000_000_000) / 1_000_000_000
}

func addTokens(total *int64, value int64) error {
	next, err := sumTokens(*total, value)
	if err != nil {
		return err
	}
	*total = next
	return nil
}

func sumTokens(values ...int64) (int64, error) {
	var result int64
	for _, value := range values {
		if value < 0 || result > math.MaxInt64-value {
			return 0, errors.New("token 汇总超出 int64")
		}
		result += value
	}
	return result, nil
}

func modelName(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}
