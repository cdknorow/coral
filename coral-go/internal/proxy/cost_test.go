package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupPricing_ExactMatch(t *testing.T) {
	for model := range Pricing {
		p, ok := lookupPricing(model)
		assert.True(t, ok, "expected exact match for %s", model)
		assert.Equal(t, Pricing[model], p)
	}
}

func TestLookupPricing_PrefixMatch(t *testing.T) {
	// "claude-sonnet-4" is a prefix of "claude-sonnet-4-20250514"
	p, ok := lookupPricing("claude-sonnet-4")
	assert.True(t, ok)
	assert.Equal(t, Pricing["claude-sonnet-4-20250514"].InputPerMTok, p.InputPerMTok)
}

func TestLookupPricing_AliasMatch(t *testing.T) {
	// A dated or suffixed form of an ID resolves to that model's own row.
	p, ok := lookupPricing("claude-opus-4-6-20260407")
	assert.True(t, ok)
	assert.Equal(t, Pricing["claude-opus-4-6"], p)
}

func TestLookupPricing_Claude46Models(t *testing.T) {
	// Dated IDs resolve to the dateless row. The 4.6 models have a 1M context
	// window; Haiku 4.5 is 200K.
	tests := []struct {
		id      string
		key     string
		context int
	}{
		{"claude-opus-4-6-20260407", "claude-opus-4-6", 1_000_000},
		{"claude-sonnet-4-6-20260407", "claude-sonnet-4-6", 1_000_000},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5", 200_000},
	}
	for _, tt := range tests {
		p, ok := lookupPricing(tt.id)
		assert.True(t, ok, "expected match for %s", tt.id)
		assert.Equal(t, tt.context, p.ContextWindow, "%s context window", tt.id)
		assert.Equal(t, Pricing[tt.key], p, "%s should match %s", tt.id, tt.key)
	}
}

func TestLookupPricing_PrefixMatchShortestKey(t *testing.T) {
	// "claude-sonnet-4" means Sonnet 4 (200K), not Sonnet 4.6 (1M), and must
	// resolve the same way every time despite map iteration order.
	for i := 0; i < 100; i++ {
		p, ok := lookupPricing("claude-sonnet-4")
		assert.True(t, ok)
		assert.Equal(t, 200_000, p.ContextWindow, "iteration %d: claude-sonnet-4 should consistently resolve to 200K", i)
	}
}

func TestLookupPricing_BracketSuffix(t *testing.T) {
	// Model strings with bracket suffixes like "[1m]" should be stripped
	p, ok := lookupPricing("claude-opus-4-6[1m]")
	assert.True(t, ok)
	assert.Equal(t, 1_000_000, p.ContextWindow)

	p, ok = lookupPricing("claude-opus-4-6-20260407[1m]")
	assert.True(t, ok)
	assert.Equal(t, 1_000_000, p.ContextWindow)
}

func TestLookupPricing_UnknownModel(t *testing.T) {
	_, ok := lookupPricing("totally-unknown-model")
	assert.False(t, ok)
}

func TestLookupPricing_EmptyModelIsUnknown(t *testing.T) {
	for _, model := range []string{"", " ", "\t\n"} {
		_, ok := lookupPricing(model)
		assert.False(t, ok)
		assert.Zero(t, LookupContextWindow(model))
	}
}

func TestLookupContextWindow_CurrentClaudeModelsDeterministic(t *testing.T) {
	models := []string{
		"claude-opus-4-7[1m]", "claude-opus-4-8[1m]",
		"claude-opus-5[1m]", "claude-sonnet-5", "claude-fable-5-1",
	}
	for i := 0; i < 100; i++ {
		for _, model := range models {
			assert.Equal(t, 1_000_000, LookupContextWindow(model), "iteration %d model %s", i, model)
		}
	}
}

func TestLookupContextWindow_FableUsagePercentage(t *testing.T) {
	window := LookupContextWindow("claude-fable-5-1")
	assert.Equal(t, 1_000_000, window)
	assert.Equal(t, 29, int(float64(293_050)/float64(window)*100))
}

// Fable had a context window entry but no pricing row, so every Fable session
// was recorded at $0 with no error. These rates are Anthropic's first-party
// API prices per million tokens.
func TestFablePricing(t *testing.T) {
	perMillion := TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}

	b := CalculateCostBreakdown("claude-fable-5-1", perMillion)
	require.True(t, b.PricingFound)
	assert.InDelta(t, 10.00, b.InputCostUSD, 1e-9)
	assert.InDelta(t, 50.00, b.OutputCostUSD, 1e-9)
	assert.InDelta(t, 0.25, b.CacheReadCostUSD, 1e-9, "Fable 5.1 cache reads are 0.025x input, not the usual 0.1x")
	assert.InDelta(t, 12.50, b.CacheWriteCostUSD, 1e-9)
	assert.InDelta(t, 72.75, b.TotalCostUSD, 1e-9)

	prev := CalculateCostBreakdown("claude-fable-5", perMillion)
	require.True(t, prev.PricingFound)
	assert.InDelta(t, 1.00, prev.CacheReadCostUSD, 1e-9, "Fable 5 cache reads cost four times Fable 5.1's")
	assert.InDelta(t, 10.00, prev.InputCostUSD, 1e-9)
	assert.InDelta(t, 50.00, prev.OutputCostUSD, 1e-9)
	assert.InDelta(t, 12.50, prev.CacheWriteCostUSD, 1e-9)
}

// The two Fable rows differ only in cache-read price, so every spelling of a
// model ID must land on the right one.
func TestFablePricing_ModelIDVariantsResolveToTheRightRow(t *testing.T) {
	cacheReadRate := func(model string) float64 {
		b := CalculateCostBreakdown(model, TokenUsage{CacheReadTokens: 1_000_000})
		require.True(t, b.PricingFound, model)
		return b.CacheReadCostUSD
	}
	for _, model := range []string{
		"claude-fable-5-1", "claude-fable-5-1[1m]", " CLAUDE-FABLE-5-1[1m] ", "claude-fable-5-1-20260601",
	} {
		assert.InDelta(t, 0.25, cacheReadRate(model), 1e-9, model)
	}
	for _, model := range []string{"claude-fable-5", "claude-fable-5[1m]", "claude-fable-5-20260301"} {
		assert.InDelta(t, 1.00, cacheReadRate(model), 1e-9, model)
	}
}

func TestFablePricing_Deterministic(t *testing.T) {
	// lookupPricing iterates a map; the dated-ID fallback must not flip between rows.
	for i := 0; i < 200; i++ {
		b := CalculateCostBreakdown("claude-fable-5-20260301", TokenUsage{CacheReadTokens: 1_000_000})
		require.InDelta(t, 1.00, b.CacheReadCostUSD, 1e-9, "iteration %d", i)
	}
}

// A realistic agent turn, to show the scale of what was being recorded as $0:
// mostly cache reads, as in the session that exposed the bug.
func TestFablePricing_RealisticAgentTurn(t *testing.T) {
	cost := CalculateCost("claude-fable-5-1", TokenUsage{
		InputTokens: 32, OutputTokens: 100, CacheReadTokens: 292_368, CacheWriteTokens: 650,
	})
	// 32*10 + 100*50 + 292368*0.25 + 650*12.5 = 86,537 micro-dollars
	assert.InDelta(t, 0.086537, cost, 1e-9)
}

func TestLookupPricing_SingleSegmentNoMatch(t *testing.T) {
	for _, model := range []string{"c", "claude", "claude-", "claude-opus"} {
		_, ok := lookupPricing(model)
		assert.False(t, ok, "%q is too broad to identify a model", model)
		assert.Zero(t, LookupContextWindow(model))
	}
}

func TestLookupContextWindow_Claude5FamiliesAndBedrockPointReleases(t *testing.T) {
	tests := map[string]int{
		"claude-opus-5-1":                 1_000_000,
		"claude-opus-5-20260901":          1_000_000,
		"claude-sonnet-5-1":               1_000_000,
		"claude-haiku-5":                  1_000_000,
		"claude-fable-5-1-20260601":       1_000_000,
		"anthropic.claude-opus-5-v1:0":    1_000_000,
		"us.anthropic.claude-opus-5-v1:0": 1_000_000,
		" CLAUDE-FABLE-5-1[1m] ":          1_000_000,
		"claude-opus-4-6-20260407":        1_000_000,
		"claude-haiku-4-5-20251001":       200_000,
		"claude-sonnet-4-20250514":        200_000,
		"claude-opus-4-20250514":          200_000,
		"claude-sonnet-4":                 200_000,
		"gpt-4o":                          128_000,
		"o3":                              200_000,
		"gemini-2.5-pro":                  1_000_000,
	}
	for i := 0; i < 300; i++ {
		for model, want := range tests {
			assert.Equal(t, want, LookupContextWindow(model), "iteration %d model %s", i, model)
		}
	}
}

func TestLookupPricing_NoFalsePositiveOnShortInput(t *testing.T) {
	// Single segment that doesn't prefix-match anything
	_, ok := lookupPricing("mistral")
	assert.False(t, ok)
}

func TestCalculateCostBreakdown_Sonnet(t *testing.T) {
	usage := TokenUsage{
		InputTokens:      1_000_000,
		OutputTokens:     1_000_000,
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 1_000_000,
	}
	b := CalculateCostBreakdown("claude-sonnet-4-20250514", usage)
	require.True(t, b.PricingFound)
	assert.InDelta(t, 3.00, b.InputCostUSD, 0.001)
	assert.InDelta(t, 15.00, b.OutputCostUSD, 0.001)
	assert.InDelta(t, 0.30, b.CacheReadCostUSD, 0.001)
	assert.InDelta(t, 3.75, b.CacheWriteCostUSD, 0.001)
	assert.InDelta(t, 22.05, b.TotalCostUSD, 0.001)
}

func TestCalculateCostBreakdown_ZeroTokens(t *testing.T) {
	b := CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{})
	require.True(t, b.PricingFound)
	assert.Equal(t, 0.0, b.TotalCostUSD)
	assert.Equal(t, 0.0, b.InputCostUSD)
}

func TestCalculateCostBreakdown_UnknownModel(t *testing.T) {
	b := CalculateCostBreakdown("unknown-model-xyz", TokenUsage{InputTokens: 1000})
	assert.False(t, b.PricingFound)
	assert.Equal(t, 0.0, b.TotalCostUSD)
	assert.Equal(t, "unknown-model-xyz", b.Model)
}

func TestCalculateCostBreakdown_InputOnlyNoCacheTokens(t *testing.T) {
	usage := TokenUsage{InputTokens: 500_000, OutputTokens: 100_000}
	b := CalculateCostBreakdown("gpt-4o", usage)
	require.True(t, b.PricingFound)
	// gpt-4o: input $2.50/MTok, output $10.00/MTok
	assert.InDelta(t, 1.25, b.InputCostUSD, 0.001)  // 500k * 2.50 / 1M
	assert.InDelta(t, 1.00, b.OutputCostUSD, 0.001) // 100k * 10.00 / 1M
	assert.Equal(t, 0.0, b.CacheReadCostUSD)        // no cache pricing for OpenAI
	assert.InDelta(t, 2.25, b.TotalCostUSD, 0.001)
}

func TestCalculateCost_MatchesBreakdownTotal(t *testing.T) {
	usage := TokenUsage{InputTokens: 10000, OutputTokens: 5000, CacheReadTokens: 2000}
	cost := CalculateCost("claude-sonnet-4-20250514", usage)
	breakdown := CalculateCostBreakdown("claude-sonnet-4-20250514", usage)
	assert.Equal(t, breakdown.TotalCostUSD, cost)
}

func TestCalculateCostBreakdown_OpusPricing(t *testing.T) {
	usage := TokenUsage{InputTokens: 100_000, OutputTokens: 50_000}
	b := CalculateCostBreakdown("claude-opus-4-20250514", usage)
	require.True(t, b.PricingFound)
	// opus: input $15/MTok, output $75/MTok
	assert.InDelta(t, 1.50, b.InputCostUSD, 0.001)  // 100k * 15 / 1M
	assert.InDelta(t, 3.75, b.OutputCostUSD, 0.001) // 50k * 75 / 1M
	assert.InDelta(t, 5.25, b.TotalCostUSD, 0.001)
}

func TestCalculateCostBreakdown_HaikuPricing(t *testing.T) {
	usage := TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	b := CalculateCostBreakdown("claude-haiku-4-5-20251001", usage)
	require.True(t, b.PricingFound)
	assert.InDelta(t, 1.00, b.InputCostUSD, 0.001)
	assert.InDelta(t, 5.00, b.OutputCostUSD, 0.001)

	// $0.80/$4 is Haiku 3.5, which the table used to file under a
	// non-existent "claude-haiku-4" model.
	old := CalculateCostBreakdown("claude-3-5-haiku-20241022", usage)
	require.True(t, old.PricingFound)
	assert.InDelta(t, 0.80, old.InputCostUSD, 0.001)
	assert.InDelta(t, 4.00, old.OutputCostUSD, 0.001)
}

func TestCalculateCostBreakdown_GeminiPro(t *testing.T) {
	usage := TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	b := CalculateCostBreakdown("gemini-2.5-pro", usage)
	require.True(t, b.PricingFound)
	assert.InDelta(t, 1.25, b.InputCostUSD, 0.001)
	assert.InDelta(t, 10.00, b.OutputCostUSD, 0.001)
}

func TestCommonPrefixLen(t *testing.T) {
	tests := []struct {
		a, b []string
		want int
	}{
		{[]string{"a", "b", "c"}, []string{"a", "b", "d"}, 2},
		{[]string{"a"}, []string{"a", "b"}, 1},
		{[]string{"x"}, []string{"y"}, 0},
		{[]string{}, []string{"a"}, 0},
		{[]string{"a", "b"}, []string{"a", "b"}, 2},
	}
	for _, tt := range tests {
		got := commonPrefixLen(tt.a, tt.b)
		assert.Equal(t, tt.want, got, "commonPrefixLen(%v, %v)", tt.a, tt.b)
	}
}

func TestPricingTable_AllModelsPresent(t *testing.T) {
	table := PricingTable()
	assert.Len(t, table, len(Pricing))

	models := make(map[string]bool)
	for _, e := range table {
		models[e.Model] = true
	}
	for model := range Pricing {
		assert.True(t, models[model], "missing model %s in PricingTable", model)
	}
}

func TestPricingTable_StableSortOrder(t *testing.T) {
	t1 := PricingTable()
	t2 := PricingTable()
	require.Equal(t, len(t1), len(t2))
	for i := range t1 {
		assert.Equal(t, t1[i].Model, t2[i].Model)
	}
}

func TestCalculateCostBreakdown_BreakdownStoresPricing(t *testing.T) {
	b := CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 1000})
	assert.Equal(t, 3.00, b.Pricing.InputPerMTok)
	assert.Equal(t, 15.00, b.Pricing.OutputPerMTok)
	assert.Equal(t, 0.30, b.Pricing.CacheReadPerMTok)
	assert.Equal(t, 3.75, b.Pricing.CacheWritePerMTok)
}

// ── Rate card ────────────────────────────────────────────────
// Prices per million tokens from the providers' pricing pages (see the comment
// on Pricing for the URLs). A failure here means the table drifted from what
// was verified, not that the test is stale: re-check the source before editing.

func TestAnthropicRateCard(t *testing.T) {
	// model -> {input, output, cache read, 5-minute cache write}
	card := map[string][4]float64{
		"claude-fable-5-1":  {10, 50, 0.25, 12.50},
		"claude-mythos-5-1": {10, 50, 0.25, 12.50},
		"claude-fable-5":    {10, 50, 1.00, 12.50},
		"claude-mythos-5":   {10, 50, 1.00, 12.50},
		"claude-opus-5":     {5, 25, 0.50, 6.25},
		"claude-opus-4-8":   {5, 25, 0.50, 6.25},
		"claude-opus-4-7":   {5, 25, 0.50, 6.25},
		"claude-opus-4-6":   {5, 25, 0.50, 6.25},
		"claude-opus-4-5":   {5, 25, 0.50, 6.25},
		"claude-opus-4-1":   {15, 75, 1.50, 18.75},
		"claude-opus-4":     {15, 75, 1.50, 18.75},
		"claude-sonnet-5":   {2, 10, 0.20, 2.50},
		"claude-sonnet-4-6": {3, 15, 0.30, 3.75},
		"claude-sonnet-4-5": {3, 15, 0.30, 3.75},
		"claude-sonnet-4":   {3, 15, 0.30, 3.75},
		"claude-haiku-4-5":  {1, 5, 0.10, 1.25},
		"claude-3-5-haiku":  {0.80, 4, 0.08, 1.00},
	}
	perMillion := TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}
	for model, want := range card {
		b := CalculateCostBreakdown(model, perMillion)
		require.True(t, b.PricingFound, model)
		assert.InDelta(t, want[0], b.InputCostUSD, 1e-9, "%s input", model)
		assert.InDelta(t, want[1], b.OutputCostUSD, 1e-9, "%s output", model)
		assert.InDelta(t, want[2], b.CacheReadCostUSD, 1e-9, "%s cache read", model)
		assert.InDelta(t, want[3], b.CacheWriteCostUSD, 1e-9, "%s cache write", model)
	}
}

// Anthropic's cache rates are fixed multiples of input. A row that breaks the
// pattern is a typo, with Fable/Mythos 5.1 the one documented exception.
func TestAnthropicCacheRatesFollowTheMultipliers(t *testing.T) {
	for model, p := range Pricing {
		if !strings.HasPrefix(model, "claude-") {
			continue
		}
		readMultiplier := 0.1
		if model == "claude-fable-5-1" || model == "claude-mythos-5-1" {
			readMultiplier = 0.025
		}
		assert.InDelta(t, p.InputPerMTok*readMultiplier, p.CacheReadPerMTok, 1e-9, "%s cache read", model)
		assert.InDelta(t, p.InputPerMTok*1.25, p.CacheWritePerMTok, 1e-9, "%s cache write", model)
		assert.InDelta(t, p.InputPerMTok*5, p.OutputPerMTok, 1e-9, "%s output is 5x input", model)
		assert.False(t, p.CachedInputIncluded, "%s: Anthropic reports cache reads outside input_tokens", model)
	}
}

// The Opus price dropped from $15 to $5 at 4.5. IDs on either side of that
// line share most of their name, so every spelling is pinned here.
func TestOpusGenerationsResolveToTheRightPrice(t *testing.T) {
	want := map[string]float64{
		"claude-opus-4":              15,
		"claude-opus-4-20250514":     15,
		"claude-opus-4-1":            15,
		"claude-opus-4-1-20250805":   15,
		"claude-opus-4-5":            5,
		"claude-opus-4-5-20251101":   5,
		"claude-opus-4-6":            5,
		"claude-opus-4-7":            5,
		"claude-opus-4-8":            5,
		"claude-opus-4-8[1m]":        5,
		"claude-opus-5":              5,
		"claude-opus-5[1m]":          5,
		"claude-opus-5-20260901":     5,
		" CLAUDE-OPUS-5[1m] ":        5,
		"claude-sonnet-5":            2,
		"claude-sonnet-5-20260601":   2,
		"claude-sonnet-4-6":          3,
		"claude-sonnet-4-5-20250929": 3,
	}
	for i := 0; i < 50; i++ { // map iteration order must not change the answer
		for model, input := range want {
			p, ok := lookupPricing(model)
			require.True(t, ok, model)
			require.InDelta(t, input, p.InputPerMTok, 1e-9, "iteration %d: %s", i, model)
		}
	}
}

func TestOpenAIRateCard(t *testing.T) {
	// model -> {input, cached input, output}
	card := map[string][3]float64{
		"gpt-5.6-sol":   {4.00, 0.40, 20.00},
		"gpt-5.6-terra": {2.00, 0.20, 12.00},
		"gpt-5.6-luna":  {0.20, 0.02, 1.20},
		"gpt-5.5":       {5.00, 0.50, 30.00},
		"gpt-5.4":       {2.50, 0.25, 15.00},
		"gpt-5.4-mini":  {0.75, 0.075, 4.50},
		"gpt-5.4-nano":  {0.20, 0.02, 1.25},
		"gpt-5.3-codex": {1.75, 0.175, 14.00},
		"gpt-5.2":       {1.75, 0.175, 14.00},
		"gpt-5.1":       {1.25, 0.125, 10.00},
		"gpt-5":         {1.25, 0.125, 10.00},
		"gpt-5-mini":    {0.25, 0.025, 2.00},
		"gpt-5-nano":    {0.05, 0.005, 0.40},
		"gpt-4o":        {2.50, 1.25, 10.00},
		"gpt-4o-mini":   {0.15, 0.075, 0.60},
		"o3":            {2.00, 0.50, 8.00},
	}
	for model, want := range card {
		p, ok := lookupPricing(model)
		require.True(t, ok, model)
		assert.InDelta(t, want[0], p.InputPerMTok, 1e-9, "%s input", model)
		assert.InDelta(t, want[1], p.CacheReadPerMTok, 1e-9, "%s cached input", model)
		assert.InDelta(t, want[2], p.OutputPerMTok, 1e-9, "%s output", model)
		assert.True(t, p.CachedInputIncluded, "%s: OpenAI input_tokens includes cached tokens", model)
	}
}

// The -pro models have no cached-input discount. Their cached rate must equal
// the input rate, or cached tokens (subtracted from input) would be free.
func TestOpenAIProModelsBillCachedInputAtFullRate(t *testing.T) {
	for _, model := range []string{"gpt-5.5-pro", "gpt-5.4-pro", "gpt-5.2-pro", "gpt-5-pro"} {
		p, ok := lookupPricing(model)
		require.True(t, ok, model)
		assert.Equal(t, p.InputPerMTok, p.CacheReadPerMTok, model)
		withCache := CalculateCost(model, TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 900_000})
		without := CalculateCost(model, TokenUsage{InputTokens: 1_000_000})
		assert.InDelta(t, without, withCache, 1e-9, "%s: caching changes nothing", model)
	}
}

// OpenAI reports cached tokens INSIDE input_tokens. Billing both in full
// would charge the cached part twice. Figures are from a real Codex rollout:
// 118.0M input of which 115.0M was cached.
func TestOpenAICachedTokensAreNotBilledTwice(t *testing.T) {
	usage := TokenUsage{InputTokens: 117_998_875, CacheReadTokens: 114_978_432, OutputTokens: 205_746}
	b := CalculateCostBreakdown("gpt-5.6-sol", usage)
	require.True(t, b.PricingFound)

	uncached := float64(117_998_875 - 114_978_432)
	assert.InDelta(t, uncached*4.00/1e6, b.InputCostUSD, 1e-6, "only the uncached remainder pays the input rate")
	assert.InDelta(t, 114_978_432*0.40/1e6, b.CacheReadCostUSD, 1e-6)
	assert.InDelta(t, 205_746*20.00/1e6, b.OutputCostUSD, 1e-6)
	assert.InDelta(t, 62.19, b.TotalCostUSD, 0.01)

	doubleBilled := 117_998_875*4.00/1e6 + 114_978_432*0.40/1e6 + 205_746*20.00/1e6
	assert.Less(t, b.TotalCostUSD, doubleBilled/8, "the naive sum would be over $500")
}

func TestCachedInputNeverGoesNegative(t *testing.T) {
	// A malformed report with more cached than input tokens must not produce a credit.
	b := CalculateCostBreakdown("gpt-5.4", TokenUsage{InputTokens: 100, CacheReadTokens: 500})
	assert.Zero(t, b.InputCostUSD)
	assert.GreaterOrEqual(t, b.TotalCostUSD, 0.0)
}

// Anthropic reports cache reads OUTSIDE input_tokens: nothing is subtracted.
func TestAnthropicCacheReadsAreBilledAlongsideInput(t *testing.T) {
	b := CalculateCostBreakdown("claude-opus-5", TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000})
	assert.InDelta(t, 5.00, b.InputCostUSD, 1e-9)
	assert.InDelta(t, 0.50, b.CacheReadCostUSD, 1e-9)
}

func TestGeminiRateCard(t *testing.T) {
	pro, ok := lookupPricing("gemini-2.5-pro")
	require.True(t, ok)
	assert.Equal(t, [3]float64{1.25, 0.125, 10.00}, [3]float64{pro.InputPerMTok, pro.CacheReadPerMTok, pro.OutputPerMTok})
	flash, ok := lookupPricing("gemini-2.5-flash")
	require.True(t, ok)
	assert.Equal(t, [3]float64{0.30, 0.03, 2.50}, [3]float64{flash.InputPerMTok, flash.CacheReadPerMTok, flash.OutputPerMTok})
}

func TestStripProviderDecoration(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":                                "claude-opus-5",
		"anthropic.claude-opus-5":                      "claude-opus-5",
		"us.anthropic.claude-opus-5-v1:0":              "claude-opus-5",
		"eu.anthropic.claude-sonnet-4-5-20250929-v1:0": "claude-sonnet-4-5-20250929",
		"global.anthropic.claude-fable-5-1":            "claude-fable-5-1",
		"anthropic.claude-3-5-haiku-20241022-v2":       "claude-3-5-haiku-20241022",
		"claude-haiku-4-5@20251001":                    "claude-haiku-4-5-20251001",
		"gpt-5.6-sol":                                  "gpt-5.6-sol",
		"us.meta.llama":                                "us.meta.llama", // a region prefix is only stripped before "anthropic."
	}
	for in, want := range cases {
		assert.Equal(t, want, stripProviderDecoration(in), in)
	}
}
