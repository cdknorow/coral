package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomModelsReplaceTheWholeSetInOneTransaction(t *testing.T) {
	ctx := context.Background()
	ss := NewSessionStore(openTestDB(t))
	got, err := ss.ListCustomModels(ctx)
	require.NoError(t, err)
	assert.Equal(t, []CustomModel{}, got)

	first := []CustomModel{
		{Family: "codex", Value: "gpt-x", DisplayName: "GPT X", Efforts: []string{"low", "high"}},
		{Family: "claude", Value: "global-opus-5-5", DisplayName: "Global Opus", Efforts: nil},
	}
	require.NoError(t, ss.ReplaceCustomModels(ctx, first))
	got, err = ss.ListCustomModels(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "gpt-x", got[0].Value, "the order sent")
	assert.Equal(t, []string{"low", "high"}, got[0].Efforts)
	assert.Equal(t, []string{}, got[1].Efforts, "never null")

	// A set the store cannot take (the same primary key twice) changes nothing.
	err = ss.ReplaceCustomModels(ctx, []CustomModel{{Family: "pi", Value: "a"}, {Family: "pi", Value: "a"}})
	require.Error(t, err)
	got, err = ss.ListCustomModels(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2, "the earlier set stands")

	require.NoError(t, ss.ReplaceCustomModels(ctx, nil))
	got, _ = ss.ListCustomModels(ctx)
	assert.Empty(t, got)
}
