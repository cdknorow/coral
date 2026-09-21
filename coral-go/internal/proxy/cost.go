package proxy

import (
	"sort"
	"strings"
)

// ModelPricing holds per-million-token pricing and context window for a model.
type ModelPricing struct {
	InputPerMTok      float64 // $ per 1M input tokens
	OutputPerMTok     float64 // $ per 1M output tokens
	CacheReadPerMTok  float64 // $ per 1M cache-read tokens (Anthropic)
	CacheWritePerMTok float64 // $ per 1M cache-write tokens (Anthropic)
	ContextWindow     int     // max context window in tokens (0 = unknown)
}

// Pricing maps canonical model names to their pricing.
// Use lookupPricing() for matching — it handles aliases and short names.
var Pricing = map[string]ModelPricing{
	// Anthropic — Claude 4
	"claude-opus-4-20250514":   {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},
	"claude-sonnet-4-20250514": {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},
	"claude-haiku-4-20250514":  {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 200_000},

	// Anthropic — Claude 4.5/4.6 (1M context)
	"claude-opus-4-6-20260407":   {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 1_000_000},
	"claude-sonnet-4-6-20260407": {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 1_000_000},
	"claude-haiku-4-5-20251001":  {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 1_000_000},

	// Anthropic — Claude Fable 5.x (1M context). First-party API rates.
	// CacheWritePerMTok is the 5-minute TTL rate (1.25x input); 1-hour writes
	// bill at $20 (2x), which this single-rate struct cannot express.
	// Fable 5.1 differs from Fable 5 ONLY in cache reads: $0.25 (0.025x input)
	// versus $1.00 (0.1x). Agent sessions are dominated by cache reads, so the
	// two must stay separate rows rather than aliases of each other.
	"claude-fable-5-1": {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 0.25, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},
	"claude-fable-5":   {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 1.00, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},

	// Bedrock — Claude 4 (on-demand pricing matches direct API; model IDs use anthropic. prefix)
	"anthropic.claude-opus-4-20250514-v1:0":      {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},
	"anthropic.claude-sonnet-4-20250514-v1:0":    {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},
	"anthropic.claude-haiku-4-20250514-v1:0":     {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 200_000},
	"us.anthropic.claude-opus-4-20250514-v1:0":   {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},
	"us.anthropic.claude-sonnet-4-20250514-v1:0": {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},
	"us.anthropic.claude-haiku-4-20250514-v1:0":  {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 200_000},

	// Bedrock — Claude 4.5/4.6 (1M context)
	"anthropic.claude-opus-4-6-20260407-v1:0":      {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 1_000_000},
	"anthropic.claude-sonnet-4-6-20260407-v1:0":    {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 1_000_000},
	"anthropic.claude-haiku-4-5-20251001-v1:0":     {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 1_000_000},
	"us.anthropic.claude-opus-4-6-20260407-v1:0":   {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 1_000_000},
	"us.anthropic.claude-sonnet-4-6-20260407-v1:0": {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 1_000_000},
	"us.anthropic.claude-haiku-4-5-20251001-v1:0":  {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 1_000_000},

	// OpenAI
	"gpt-4o":      {InputPerMTok: 2.50, OutputPerMTok: 10.00, ContextWindow: 128_000},
	"gpt-4o-mini": {InputPerMTok: 0.15, OutputPerMTok: 0.60, ContextWindow: 128_000},
	"o3":          {InputPerMTok: 2.00, OutputPerMTok: 8.00, ContextWindow: 200_000},

	// Google
	"gemini-2.5-pro":   {InputPerMTok: 1.25, OutputPerMTok: 10.00, ContextWindow: 1_000_000},
	"gemini-2.5-flash": {InputPerMTok: 0.15, OutputPerMTok: 0.60, ContextWindow: 1_000_000},
}

// modelAliases maps model identifiers observed from supported agent CLIs to
// canonical pricing rows. These aliases deliberately reuse repository pricing
// data instead of duplicating or guessing prices for every dated identifier.
var modelAliases = map[string]string{
	"claude-opus-4-7": "claude-opus-4-6-20260407",
	"claude-opus-4-8": "claude-opus-4-6-20260407",
	"claude-opus-5":   "claude-opus-4-6-20260407",
	"claude-sonnet-5": "claude-sonnet-4-6-20260407",
}

// modelContextWindows records authoritative context sizes even when Coral has
// no verified pricing row for a model (and therefore must not invent costs).
var modelContextWindows = map[string]int{
	"claude-opus-4-7":  1_000_000,
	"claude-opus-4-8":  1_000_000,
	"claude-opus-5":    1_000_000,
	"claude-sonnet-5":  1_000_000,
	"claude-fable-5-1": 1_000_000,
}

func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.IndexByte(model, '['); idx >= 0 {
		model = strings.TrimSpace(model[:idx])
	}
	return model
}

func explicitContextWindow(model string) (int, bool) {
	if window, ok := modelContextWindows[model]; ok {
		return window, true
	}
	for _, providerPrefix := range []string{"", "anthropic.", "us.anthropic."} {
		for _, family := range []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-5", "claude-fable-5"} {
			prefix := providerPrefix + family
			if model == prefix || strings.HasPrefix(model, prefix+"-") {
				return 1_000_000, true
			}
		}
	}
	return 0, false
}

// lookupPricing finds pricing for a model. Matching strategy:
//  1. Exact match against pricing table
//  2. Prefix match: model is a prefix of a known key (e.g. "claude-opus-4" matches "claude-opus-4-20250514")
//  3. Longest common prefix: find the pricing key with the longest shared
//     dash-delimited prefix. Handles aliases like "claude-opus-4-6" matching
//     "claude-opus-4-20250514" (both share prefix "claude-opus-4").
func lookupPricing(model string) (ModelPricing, bool) {
	model = normalizeModel(model)
	if model == "" {
		return ModelPricing{}, false
	}

	// 1. Exact match
	if p, ok := Pricing[model]; ok {
		return p, true
	}
	if canonical, ok := modelAliases[model]; ok {
		p, found := Pricing[canonical]
		return p, found
	}
	modelParts := strings.Split(model, "-")
	if len(modelParts) < 3 || modelParts[0] == "" || modelParts[1] == "" || modelParts[2] == "" {
		return ModelPricing{}, false
	}

	keys := make([]string, 0, len(Pricing))
	for key := range Pricing {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// 2. Prefix match: the incoming model is a prefix of a known key.
	// Prefer the shortest matching key to avoid ambiguity (e.g. "claude-sonnet-4"
	// should match "claude-sonnet-4-20250514" not "claude-sonnet-4-6-20260407").
	var bestPrefix ModelPricing
	bestPrefixLen := 0
	for _, key := range keys {
		p := Pricing[key]
		if strings.HasPrefix(key, model) {
			if bestPrefixLen == 0 || len(key) < bestPrefixLen {
				bestPrefix = p
				bestPrefixLen = len(key)
			}
		}
	}
	if bestPrefixLen > 0 {
		return bestPrefix, true
	}

	// 3. Longest common dash-delimited prefix.
	// Split both the model and each pricing key on dashes, count how many
	// leading segments match. The key with the most matching segments wins.
	// Requires at least 2 matching segments to avoid false positives.
	var best ModelPricing
	bestMatch := 1 // minimum 2 matching segments required
	for _, key := range keys {
		p := Pricing[key]
		keyParts := strings.Split(key, "-")
		match := commonPrefixLen(modelParts, keyParts)
		if match > bestMatch {
			best = p
			bestMatch = match
		}
	}
	if bestMatch > 1 {
		return best, true
	}
	return ModelPricing{}, false
}

// commonPrefixLen returns the number of matching leading elements between two slices.
func commonPrefixLen(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// TokenUsage holds token counts from a provider response.
type TokenUsage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// CostBreakdown stores the applied pricing and computed dollar breakdown.
type CostBreakdown struct {
	Model             string       `json:"model"`
	PricingFound      bool         `json:"pricing_found"`
	Pricing           ModelPricing `json:"pricing"`
	InputCostUSD      float64      `json:"input_cost_usd"`
	OutputCostUSD     float64      `json:"output_cost_usd"`
	CacheReadCostUSD  float64      `json:"cache_read_cost_usd"`
	CacheWriteCostUSD float64      `json:"cache_write_cost_usd"`
	TotalCostUSD      float64      `json:"total_cost_usd"`
}

// PricingEntry is a serializable pricing table row.
type PricingEntry struct {
	Model string `json:"model"`
	ModelPricing
}

// PricingTable returns the current pricing table in stable model order.
func PricingTable() []PricingEntry {
	keys := make([]string, 0, len(Pricing))
	for model := range Pricing {
		keys = append(keys, model)
	}
	sort.Strings(keys)

	rows := make([]PricingEntry, 0, len(keys))
	for _, model := range keys {
		rows = append(rows, PricingEntry{
			Model:        model,
			ModelPricing: Pricing[model],
		})
	}
	return rows
}

// CalculateCostBreakdown computes the dollar cost breakdown for a request.
func CalculateCostBreakdown(model string, usage TokenUsage) CostBreakdown {
	pricing, ok := lookupPricing(model)
	if !ok {
		return CostBreakdown{Model: model}
	}

	breakdown := CostBreakdown{
		Model:             model,
		PricingFound:      true,
		Pricing:           pricing,
		InputCostUSD:      float64(usage.InputTokens) * pricing.InputPerMTok / 1_000_000,
		OutputCostUSD:     float64(usage.OutputTokens) * pricing.OutputPerMTok / 1_000_000,
		CacheReadCostUSD:  float64(usage.CacheReadTokens) * pricing.CacheReadPerMTok / 1_000_000,
		CacheWriteCostUSD: float64(usage.CacheWriteTokens) * pricing.CacheWritePerMTok / 1_000_000,
	}
	breakdown.TotalCostUSD = breakdown.InputCostUSD + breakdown.OutputCostUSD +
		breakdown.CacheReadCostUSD + breakdown.CacheWriteCostUSD
	return breakdown
}

// CalculateCost computes the dollar cost for a request.
func CalculateCost(model string, usage TokenUsage) float64 {
	return CalculateCostBreakdown(model, usage).TotalCostUSD
}

// LookupContextWindow returns the context window size for a model (0 if unknown).
func LookupContextWindow(model string) int {
	normalized := normalizeModel(model)
	if normalized == "" {
		return 0
	}
	if window, ok := explicitContextWindow(normalized); ok {
		return window
	}
	if p, ok := lookupPricing(model); ok {
		return p.ContextWindow
	}
	return 0
}
