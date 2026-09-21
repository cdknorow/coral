package proxy

import (
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
	// "claude-opus-4-6" should match "claude-opus-4-6-20260407" (1M context)
	p, ok := lookupPricing("claude-opus-4-6")
	assert.True(t, ok)
	assert.Equal(t, 1_000_000, p.ContextWindow, "claude-opus-4-6 should resolve to 1M context window")
	assert.Equal(t, Pricing["claude-opus-4-6-20260407"].InputPerMTok, p.InputPerMTok)
}

func TestLookupPricing_Claude46Models(t *testing.T) {
	// All Claude 4.6 short aliases should resolve to 1M context
	tests := []struct {
		alias string
		key   string
	}{
		{"claude-opus-4-6", "claude-opus-4-6-20260407"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6-20260407"},
		{"claude-haiku-4-5", "claude-haiku-4-5-20251001"},
	}
	for _, tt := range tests {
		p, ok := lookupPricing(tt.alias)
		assert.True(t, ok, "expected match for %s", tt.alias)
		assert.Equal(t, 1_000_000, p.ContextWindow, "%s should have 1M context", tt.alias)
		assert.Equal(t, Pricing[tt.key], p, "%s should match %s", tt.alias, tt.key)
	}
}

func TestLookupPricing_PrefixMatchShortestKey(t *testing.T) {
	// "claude-sonnet-4" is a prefix of both "claude-sonnet-4-20250514" (200K)
	// and "claude-sonnet-4-6-20260407" (1M). Should consistently pick the
	// shortest key (200K) to avoid non-deterministic behavior.
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
		"claude-haiku-4-5-20251001":       1_000_000,
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
	b := CalculateCostBreakdown("claude-haiku-4-20250514", usage)
	require.True(t, b.PricingFound)
	assert.InDelta(t, 0.80, b.InputCostUSD, 0.001)
	assert.InDelta(t, 4.00, b.OutputCostUSD, 0.001)
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
