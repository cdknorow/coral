package routes

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A post that mentions nobody on the board is made, and the reply warns, naming the closest member (#4583 rule 5). A post whose
// mentions are all members has no warnings key.
func TestPostWarnsOfAnUnknownMentionButStillPosts(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Digital Ocean Expert", "Cloud", "claude-11111111-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "claude-22222222-0000-0000-0000-000000000000", nil, "all")
	require.NoError(t, err)
	post := func(content string) map[string]any {
		t.Helper()
		resp := postJSON(t, server.URL+"/api/board/team/messages", map[string]string{"subscriber_id": "Orchestrator", "content": content})
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		require.NoError(t, json.Unmarshal(b, &out))
		return out
	}
	out := post("@DO Expert is the droplet up?")
	require.NotNil(t, out["id"], "the post is made")
	assert.Equal(t, []any{"no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?"}, out["warnings"])
	assert.Contains(t, out["content"], "@DO Expert", "the text is stored as written")
	msgs, err := h.bs.ListMessages(ctx, "team", 10, 0, 0)
	require.NoError(t, err)
	assert.Len(t, msgs, 1)

	ok := post("@Digital Ocean Expert the droplet is yours, @all")
	assert.NotContains(t, ok, "warnings")
	assert.NotNil(t, ok["id"])
	none := post("no mention here")
	assert.NotContains(t, none, "warnings")
}
