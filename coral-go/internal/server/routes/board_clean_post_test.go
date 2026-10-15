package routes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A post's terminal control is removed before it is stored (#3580), and a post that is
// nothing but control is refused like an empty one.
func TestPostMessageStripsTerminalControl(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/cleanboard"
	resp := postJSON(t, base+"/subscribe", map[string]string{"subscriber_id": "Lead", "job_title": "Lead Developer"})
	resp.Body.Close()

	resp = postJSON(t, base+"/messages", map[string]string{"subscriber_id": "Lead",
		"content": "status \x1b[2K\x1b[1Aall green\x1b]0;pwned\x07 \x1b]52;c;cHduZWQ=\x07done\r\nnext\tline"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var posted map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&posted))
	resp.Body.Close()
	assert.Equal(t, "status all green done\nnext\tline", posted["content"])

	got, err := http.Get(base + "/messages/all?limit=10")
	require.NoError(t, err)
	var all []map[string]any
	require.NoError(t, json.NewDecoder(got.Body).Decode(&all))
	got.Body.Close()
	require.Len(t, all, 1)
	assert.Equal(t, "status all green done\nnext\tline", all[0]["content"])
	assert.False(t, strings.ContainsAny(all[0]["content"].(string), "\x1b\x07\r"))

	resp = postJSON(t, base+"/messages", map[string]string{"subscriber_id": "Lead", "content": "\x1b[2J\x1b[H\x07"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "only control: nothing to post")
	resp.Body.Close()
}
