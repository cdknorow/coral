package proxy

import (
	"regexp"
	"sort"
	"strings"
)

// ModelPricing holds per-million-token pricing and context window for a model.
type ModelPricing struct {
	InputPerMTok      float64 // $ per 1M input tokens
	OutputPerMTok     float64 // $ per 1M output tokens
	CacheReadPerMTok  float64 // $ per 1M cached-input / cache-read tokens
	CacheWritePerMTok float64 // $ per 1M cache-write tokens (Anthropic only)
	ContextWindow     int     // max context window in tokens (0 = unknown)

	// CachedInputIncluded records how the provider reports cached tokens.
	// Anthropic reports input_tokens EXCLUDING cache reads, so the two are
	// billed side by side. OpenAI and Google report an input count that already
	// INCLUDES the cached tokens, so the cached part must be subtracted before
	// the input rate is applied or it is billed twice.
	CachedInputIncluded bool
}

// Pricing maps canonical model names to their pricing.
// Use lookupPricing() for matching — it handles provider prefixes, dated IDs
// and short names.
//
// Sources, checked 2026-09-21. Re-check them when adding or changing a row:
//   - Anthropic: https://platform.claude.com/docs/en/about-claude/pricing
//   - OpenAI:    https://developers.openai.com/api/docs/pricing (standard tier)
//   - Google:    https://ai.google.dev/gemini-api/docs/pricing (paid tier)
//
// Anthropic cache rates follow fixed multipliers of the input price: reads
// 0.1x (0.025x on Fable/Mythos 5.1), 5-minute writes 1.25x. CacheWritePerMTok
// is the 5-minute rate; 1-hour writes bill at 2x, which this single-rate
// struct cannot express.
//
// Keys are the providers' real model IDs. Every generation that has its own
// price gets its own row: the fuzzy fallback in lookupPricing matches on shared
// leading segments, so "claude-opus-4-5" must not be left to fall through to
// "claude-opus-4", which costs three times as much.
var Pricing = map[string]ModelPricing{
	// Anthropic — Fable / Mythos 5.x. 5.1 differs from 5 ONLY in cache reads.
	// Agent sessions are dominated by cache reads, so they stay separate rows.
	"claude-fable-5-1":  {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 0.25, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},
	"claude-mythos-5-1": {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 0.25, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},
	"claude-fable-5":    {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 1.00, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},
	"claude-mythos-5":   {InputPerMTok: 10.00, OutputPerMTok: 50.00, CacheReadPerMTok: 1.00, CacheWritePerMTok: 12.50, ContextWindow: 1_000_000},

	// Anthropic — Opus. 4.5 and later are $5/$25; Opus 4 and 4.1 remain $15/$75.
	"claude-opus-5":          {InputPerMTok: 5.00, OutputPerMTok: 25.00, CacheReadPerMTok: 0.50, CacheWritePerMTok: 6.25, ContextWindow: 1_000_000},
	"claude-opus-4-8":        {InputPerMTok: 5.00, OutputPerMTok: 25.00, CacheReadPerMTok: 0.50, CacheWritePerMTok: 6.25, ContextWindow: 1_000_000},
	"claude-opus-4-7":        {InputPerMTok: 5.00, OutputPerMTok: 25.00, CacheReadPerMTok: 0.50, CacheWritePerMTok: 6.25, ContextWindow: 1_000_000},
	"claude-opus-4-6":        {InputPerMTok: 5.00, OutputPerMTok: 25.00, CacheReadPerMTok: 0.50, CacheWritePerMTok: 6.25, ContextWindow: 1_000_000},
	"claude-opus-4-5":        {InputPerMTok: 5.00, OutputPerMTok: 25.00, CacheReadPerMTok: 0.50, CacheWritePerMTok: 6.25, ContextWindow: 200_000},
	"claude-opus-4-1":        {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},
	"claude-opus-4":          {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},
	"claude-opus-4-20250514": {InputPerMTok: 15.00, OutputPerMTok: 75.00, CacheReadPerMTok: 1.50, CacheWritePerMTok: 18.75, ContextWindow: 200_000},

	// Anthropic — Sonnet. Sonnet 5 is $2/$10 (the launch price became permanent).
	"claude-sonnet-5":          {InputPerMTok: 2.00, OutputPerMTok: 10.00, CacheReadPerMTok: 0.20, CacheWritePerMTok: 2.50, ContextWindow: 1_000_000},
	"claude-sonnet-4-6":        {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 1_000_000},
	"claude-sonnet-4-5":        {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},
	"claude-sonnet-4":          {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},
	"claude-sonnet-4-20250514": {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.75, ContextWindow: 200_000},

	// Anthropic — Haiku. There is no "Haiku 4"; $0.80/$4 is Haiku 3.5's price.
	"claude-haiku-4-5": {InputPerMTok: 1.00, OutputPerMTok: 5.00, CacheReadPerMTok: 0.10, CacheWritePerMTok: 1.25, ContextWindow: 200_000},
	"claude-3-5-haiku": {InputPerMTok: 0.80, OutputPerMTok: 4.00, CacheReadPerMTok: 0.08, CacheWritePerMTok: 1.00, ContextWindow: 200_000},

	// Bedrock and Vertex model IDs ("us.anthropic.claude-opus-5-v1:0",
	// "claude-opus-4-5@20251101") have no rows of their own: lookupPricing
	// strips the provider decoration and prices them from the rows above.
	// Global endpoints match first-party rates; regional and multi-region
	// endpoints carry a 10% premium from the 4.5 generation on, not modelled here.

	// OpenAI — GPT-5 family (Codex agents). CacheReadPerMTok is "cached input".
	// gpt-5.6-sol is promotional pricing through 2026-11-21. The -pro models
	// have no cached-input discount, so their cached rate equals the input rate.
	"gpt-5.6-sol":   {InputPerMTok: 4.00, OutputPerMTok: 20.00, CacheReadPerMTok: 0.40, CachedInputIncluded: true},
	"gpt-5.6-terra": {InputPerMTok: 2.00, OutputPerMTok: 12.00, CacheReadPerMTok: 0.20, CachedInputIncluded: true},
	"gpt-5.6-luna":  {InputPerMTok: 0.20, OutputPerMTok: 1.20, CacheReadPerMTok: 0.02, CachedInputIncluded: true},
	"gpt-5.5":       {InputPerMTok: 5.00, OutputPerMTok: 30.00, CacheReadPerMTok: 0.50, CachedInputIncluded: true},
	"gpt-5.5-pro":   {InputPerMTok: 30.00, OutputPerMTok: 180.00, CacheReadPerMTok: 30.00, CachedInputIncluded: true},
	"gpt-5.4":       {InputPerMTok: 2.50, OutputPerMTok: 15.00, CacheReadPerMTok: 0.25, CachedInputIncluded: true},
	"gpt-5.4-mini":  {InputPerMTok: 0.75, OutputPerMTok: 4.50, CacheReadPerMTok: 0.075, CachedInputIncluded: true},
	"gpt-5.4-nano":  {InputPerMTok: 0.20, OutputPerMTok: 1.25, CacheReadPerMTok: 0.02, CachedInputIncluded: true},
	"gpt-5.4-pro":   {InputPerMTok: 30.00, OutputPerMTok: 180.00, CacheReadPerMTok: 30.00, CachedInputIncluded: true},
	"gpt-5.3-codex": {InputPerMTok: 1.75, OutputPerMTok: 14.00, CacheReadPerMTok: 0.175, CachedInputIncluded: true},
	"gpt-5.2":       {InputPerMTok: 1.75, OutputPerMTok: 14.00, CacheReadPerMTok: 0.175, CachedInputIncluded: true},
	"gpt-5.2-pro":   {InputPerMTok: 21.00, OutputPerMTok: 168.00, CacheReadPerMTok: 21.00, CachedInputIncluded: true},
	"gpt-5.1":       {InputPerMTok: 1.25, OutputPerMTok: 10.00, CacheReadPerMTok: 0.125, CachedInputIncluded: true},
	"gpt-5":         {InputPerMTok: 1.25, OutputPerMTok: 10.00, CacheReadPerMTok: 0.125, CachedInputIncluded: true},
	"gpt-5-mini":    {InputPerMTok: 0.25, OutputPerMTok: 2.00, CacheReadPerMTok: 0.025, CachedInputIncluded: true},
	"gpt-5-nano":    {InputPerMTok: 0.05, OutputPerMTok: 0.40, CacheReadPerMTok: 0.005, CachedInputIncluded: true},
	"gpt-5-pro":     {InputPerMTok: 15.00, OutputPerMTok: 120.00, CacheReadPerMTok: 15.00, CachedInputIncluded: true},

	// OpenAI — earlier models
	"gpt-4o":      {InputPerMTok: 2.50, OutputPerMTok: 10.00, CacheReadPerMTok: 1.25, ContextWindow: 128_000, CachedInputIncluded: true},
	"gpt-4o-mini": {InputPerMTok: 0.15, OutputPerMTok: 0.60, CacheReadPerMTok: 0.075, ContextWindow: 128_000, CachedInputIncluded: true},
	"o3":          {InputPerMTok: 2.00, OutputPerMTok: 8.00, CacheReadPerMTok: 0.50, ContextWindow: 200_000, CachedInputIncluded: true},

	// Google — paid tier, prompts up to 200k tokens. Gemini 2.5 Pro doubles its
	// input price (and output goes to $15) above 200k, which is not modelled.
	"gemini-2.5-pro":   {InputPerMTok: 1.25, OutputPerMTok: 10.00, CacheReadPerMTok: 0.125, ContextWindow: 1_000_000, CachedInputIncluded: true},
	"gemini-2.5-flash": {InputPerMTok: 0.30, OutputPerMTok: 2.50, CacheReadPerMTok: 0.03, ContextWindow: 1_000_000, CachedInputIncluded: true},
}

// modelAliases maps model identifiers that cannot be resolved by exact or
// segment matching to a canonical pricing row. It is empty now that every
// priced generation has its own row; it stays as the place to put a true
// alias (a differently named ID for the same model) if one appears.
var modelAliases = map[string]string{}

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

// bedrockVersionSuffix matches the "-v1:0" / "-v2" tail of Bedrock model IDs.
var bedrockVersionSuffix = regexp.MustCompile(`-v\d+(:\d+)?$`)

// stripProviderDecoration reduces a cloud-provider model ID to the first-party
// ID it denotes, so one pricing row serves every platform:
//
//	us.anthropic.claude-opus-5-v1:0  -> claude-opus-5          (Bedrock)
//	claude-opus-4-5@20251101         -> claude-opus-4-5-20251101 (Vertex)
func stripProviderDecoration(model string) string {
	for _, region := range []string{"global.", "us.", "eu.", "apac.", "jp.", "au."} {
		if strings.HasPrefix(model, region+"anthropic.") {
			model = strings.TrimPrefix(model, region)
			break
		}
	}
	model = strings.TrimPrefix(model, "anthropic.")
	model = bedrockVersionSuffix.ReplaceAllString(model, "")
	return strings.ReplaceAll(model, "@", "-")
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
	model = stripProviderDecoration(normalizeModel(model))
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

	// See ModelPricing.CachedInputIncluded: for OpenAI and Google the cached
	// tokens are already inside InputTokens and must not be billed twice.
	billableInput := usage.InputTokens
	if pricing.CachedInputIncluded {
		billableInput = max(usage.InputTokens-usage.CacheReadTokens, 0)
	}

	breakdown := CostBreakdown{
		Model:             model,
		PricingFound:      true,
		Pricing:           pricing,
		InputCostUSD:      float64(billableInput) * pricing.InputPerMTok / 1_000_000,
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
