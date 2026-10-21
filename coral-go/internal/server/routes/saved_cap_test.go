package routes

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// The bookmark cap is a decision: a full list refuses a new message with 409 and stores nothing, but saving a message
// that is already saved still answers with its bookmark.
func TestSavingPastTheBookmarkCapIsRefusedAndNothingIsAdded(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	g := newSavedRig(t)
	ctx := context.Background()
	for i := 0; i < store.MaxSavedMessages; i++ {
		_, err := g.h.ss.SaveMessage(ctx, store.SavedMessage{Kind: "agent", Ref: "sess-1", Key: fmt.Sprintf("%020x", i), Title: "t", Snippet: "s", SaidAt: "2026-10-17T08:00:00Z"})
		require.NoError(t, err)
	}

	code, out := g.do("POST", "/api/saved", savedAsOperator, agentSave("ffffffffffffffffffff", "one too many"))
	assert.Equal(t, http.StatusConflict, code, out)
	_, counts := g.do("GET", "/api/saved?limit=0", savedAsOperator, nil)
	assert.EqualValues(t, store.MaxSavedMessages, counts["open"], "nothing was added past the cap")

	code, out = g.do("POST", "/api/saved", savedAsOperator, agentSave(fmt.Sprintf("%020x", 7), "again"))
	assert.Equal(t, http.StatusOK, code, "a message already saved is still answered at the cap: %v", out)
}
