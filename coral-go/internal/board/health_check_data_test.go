package board

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An "@name" counts only as a whole word: "@Dev" is not "@Developer" (Security's SHOULD (c)).
func TestMentionsOfMatchesWholeWords(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for _, n := range []string{"Dev", "Developer", "Lead"} {
		_, err := s.Subscribe(ctx, "proj", n, "Role "+n, "sess-"+n, nil, "all")
		require.NoError(t, err)
	}
	post := func(text string) int64 {
		m, err := s.PostMessage(ctx, "proj", "Lead", text, nil)
		require.NoError(t, err)
		return m.ID
	}
	notIt := post("@Developer please look")
	notIt2 := post("mail dev@Dev.example or @Devs")
	yes1 := post("@Dev please look")
	yes2 := post("thanks, @dev.")
	yes3 := post("(@Dev)")

	got, err := s.MentionsOf(ctx, "proj", "Dev", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	var ids []int64
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []int64{yes1, yes2, yes3}, ids)
	assert.NotContains(t, ids, notIt)
	assert.NotContains(t, ids, notIt2)
}
