package agentmodels

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCustomSetKeepsAValidListAndCleansIt(t *testing.T) {
	out, err := ValidateCustomSet([]Custom{
		{Family: "claude", Value: " global-opus-5-5 ", DisplayName: "  Opus\n 5.5  global ", Efforts: []string{"low", "xhigh"}},
		{Family: "codex", Value: "gpt-6-astra", Efforts: []string{"minimal", "high"}},
		{Family: "agy", Value: "openai/gpt-4o:high", Efforts: nil},
		{Family: "pi", Value: "my-model[1m]"},
	})
	require.NoError(t, err)
	require.Len(t, out, 4)
	assert.Equal(t, "global-opus-5-5", out[0].Value)
	assert.Equal(t, "Opus 5.5 global", out[0].DisplayName)
	assert.Equal(t, []string{"low", "xhigh"}, out[0].Efforts, "the order given is kept")
	assert.Equal(t, "gpt-6-astra", out[1].DisplayName, "the id when no name is given")
	assert.Equal(t, []string{}, out[2].Efforts, "never null")
	assert.Equal(t, []string{}, out[3].Efforts)
}

// A model id is one argument of --model: nothing flag-shaped, spaced or controlled gets in, whatever the charset allows elsewhere.
func TestValidateCustomSetRefusesWhatCouldBeFlagShapedOrOddOnACommandLine(t *testing.T) {
	for _, bad := range []string{"", "  ", "-x", "--dangerously-skip-permissions", "--model=x", "-", "a b", "a\tb", "a\nb", "a\x00b", "a;b", "a$b", "a`b", "a'b", `a"b`, "a|b", "a&b", "\u00e9", "a=b", "../x", strings.Repeat("a", 129), "\u202ex", "a\u200bb"} {
		_, err := ValidateCustomSet([]Custom{{Family: "claude", Value: bad}})
		require.Error(t, err, "%q", bad)
		var ce *CustomError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, "value", ce.Field, "%q", bad)
		assert.Equal(t, 0, ce.Row)
	}
	_, err := ValidateCustomSet([]Custom{{Family: "claude", Value: strings.Repeat("a", 128)}})
	assert.NoError(t, err, "128 is the limit")
}

func TestValidateCustomSetNamesTheRowFamilyAndField(t *testing.T) {
	cases := []struct {
		name  string
		in    []Custom
		row   int
		field string
	}{
		{"unknown family", []Custom{{Family: "claude", Value: "a"}, {Family: "gemini", Value: "b"}}, 1, "family"},
		{"empty family", []Custom{{Value: "b"}}, 0, "family"},
		{"duplicate", []Custom{{Family: "codex", Value: "a"}, {Family: "claude", Value: "a"}, {Family: "codex", Value: "a"}}, 2, "value"},
		{"effort the CLI does not take", []Custom{{Family: "claude", Value: "a", Efforts: []string{"low", "turbo"}}}, 0, "efforts"},
		{"effort twice", []Custom{{Family: "claude", Value: "a", Efforts: []string{"low", "low"}}}, 0, "efforts"},
		{"levels for a provider with none", []Custom{{Family: "pi", Value: "a", Efforts: []string{"low"}}}, 0, "efforts"},
		{"a codex level that is claude's", []Custom{{Family: "codex", Value: "a", Efforts: []string{"max"}}}, 0, "efforts"},
		{"an empty level", []Custom{{Family: "claude", Value: "a", Efforts: []string{""}}}, 0, "efforts"},
	}
	for _, c := range cases {
		_, err := ValidateCustomSet(c.in)
		var ce *CustomError
		require.ErrorAs(t, err, &ce, c.name)
		assert.Equal(t, c.row, ce.Row, c.name)
		assert.Equal(t, c.field, ce.Field, c.name)
		assert.NotEmpty(t, ce.Msg, c.name)
	}
}

func TestValidateCustomSetLimits(t *testing.T) {
	var perFamily []Custom
	for i := 0; i <= MaxCustomPerFamily; i++ {
		perFamily = append(perFamily, Custom{Family: "claude", Value: "m" + strings.Repeat("x", i)})
	}
	_, err := ValidateCustomSet(perFamily[:MaxCustomPerFamily])
	assert.NoError(t, err)
	_, err = ValidateCustomSet(perFamily)
	var ce *CustomError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, MaxCustomPerFamily, ce.Row)

	var all []Custom
	for i := 0; i <= MaxCustomTotal; i++ {
		all = append(all, Custom{Family: CustomFamilies[i%4], Value: "m" + strings.Repeat("y", i)})
	}
	_, err = ValidateCustomSet(all)
	require.Error(t, err)
	_, err = ValidateCustomSet(nil)
	assert.NoError(t, err, "an empty list clears them all")
}

func TestFamilyLevelsAreWhatTheCLIsTake(t *testing.T) {
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, FamilyLevels("claude"))
	assert.Equal(t, []string{"minimal", "low", "medium", "high", "xhigh"}, FamilyLevels("codex"))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, FamilyLevels("agy"))
	assert.Equal(t, []string{}, FamilyLevels("pi"), "Pi is not installed to check: no levels")
	assert.Equal(t, []string{}, FamilyLevels("nonsense"))
}

func TestCleanCustomDropsWhatNoLongerPasses(t *testing.T) {
	got := CleanCustom([]Custom{
		{Family: "claude", Value: "ok", Efforts: []string{"low"}},
		{Family: "claude", Value: "--evil"},
		{Family: "claude", Value: "ok"},
		{Family: "other", Value: "x"},
		{Family: "codex", Value: "fine", Efforts: []string{"bogus"}},
		{Family: "agy", Value: "also-fine"},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "ok", got[0].Value)
	assert.Equal(t, "also-fine", got[1].Value)
}

func TestMergeCustomPutsThemAfterTheCLIsModelsAndKeepsTheInputs(t *testing.T) {
	cli := []Model{{Value: "opus", DisplayName: "Opus", Description: "d"}, {Value: "sonnet", DisplayName: "Sonnet"}}
	custom := []Custom{
		{Family: "claude", Value: "global-opus-5-5", DisplayName: "Global Opus", Efforts: []string{"high"}},
		{Family: "claude", Value: "sonnet", DisplayName: "My Sonnet", Efforts: []string{"low"}},
		{Family: "codex", Value: "gpt-x"},
	}
	got := MergeCustom(cli, "claude", custom)
	require.Len(t, got, 3)
	assert.Equal(t, "opus", got[0].Value)
	assert.False(t, got[0].Custom)
	assert.Equal(t, Model{Value: "sonnet", DisplayName: "My Sonnet", Efforts: []string{"low"}, Custom: true}, got[1], "one the CLI lists keeps its place")
	assert.Equal(t, Model{Value: "global-opus-5-5", DisplayName: "Global Opus", Efforts: []string{"high"}, Custom: true}, got[2])
	assert.Equal(t, "Sonnet", cli[1].DisplayName, "the CLI's list is not changed")
	assert.Equal(t, []Model{}, MergeCustom(nil, "pi", custom), "other providers' models are not added; never null")

	// gemini runs agy: its customs are agy's.
	assert.Equal(t, []string{"a", "b"}, MergeCustomValues([]string{"a"}, "gemini", []Custom{{Family: "agy", Value: "b"}, {Family: "agy", Value: "a"}, {Family: "claude", Value: "c"}}))
	assert.Equal(t, []string{}, MergeCustomValues(nil, "pi", nil))
}
