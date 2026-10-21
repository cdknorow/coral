package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5802: the Saved list asks which of a board's message ids still exist in one query; an id that was deleted, never existed or is another board's is not there.
func TestExistingMessageIDsIsOneBoardsMessagesThatAreStillThere(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, p := range []string{"alpha", "beta"} {
		_, err := s.Subscribe(ctx, p, "Dev", "Dev", "dev-"+p, nil, "all")
		require.NoError(t, err)
	}
	post := func(project, text string) int64 {
		m, err := s.PostMessage(ctx, project, "Dev", text, nil)
		require.NoError(t, err)
		return m.ID
	}
	a1, a2, b1 := post("alpha", "one"), post("alpha", "two"), post("beta", "other board")
	ok, err := s.DeleteMessage(ctx, "alpha", a2)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := s.ExistingMessageIDs(ctx, "alpha", []int64{a1, a2, b1, 999999})
	require.NoError(t, err)
	assert.Equal(t, map[int64]bool{a1: true}, got)
	got, err = s.ExistingMessageIDs(ctx, "alpha", nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	// more ids than one query takes are read in chunks
	many := make([]int64, 1200)
	for i := range many {
		many[i] = int64(i + 1000000)
	}
	many[1199] = a1
	got, err = s.ExistingMessageIDs(ctx, "alpha", many)
	require.NoError(t, err)
	assert.Equal(t, map[int64]bool{a1: true}, got)
}
