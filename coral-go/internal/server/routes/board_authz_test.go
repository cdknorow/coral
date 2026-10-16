package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3668 (board authorization audit, finding 1): a message is deleted only through its own board;
// another board's URL with its id deletes nothing.
func TestDeleteMessageStaysOnItsBoard(t *testing.T) {
	server, bh := setupBoardTestServer(t)
	ctx := context.Background()
	msg, err := bh.bs.PostMessage(ctx, "board-a", "Lead", "keep me", nil)
	require.NoError(t, err)
	del := func(board string) int {
		req, _ := http.NewRequest("DELETE", server.URL+"/api/board/"+board+"/messages/"+strconv.FormatInt(msg.ID, 10), nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusNotFound, del("board-b"), "another board's URL")
	n, err := bh.bs.CountMessages(ctx, "board-a")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the message is still there")
	assert.Equal(t, http.StatusOK, del("board-a"), "its own board")
	n, err = bh.bs.CountMessages(ctx, "board-a")
	require.NoError(t, err)
	assert.Zero(t, n)
}

// Finding 2: a pause is stored with the board, so a restart (a new handler on the same store)
// keeps it, and a resume clears it.
func TestBoardPauseSurvivesARestart(t *testing.T) {
	server, bh := setupBoardTestServer(t)
	post := func(path string) {
		resp, err := http.Post(server.URL+path, "application/json", nil)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	post("/api/board/team/pause")
	restarted := NewBoardHandler(bh.bs)
	restarted.LoadPaused(context.Background())
	assert.True(t, restarted.IsPaused("team"), "still paused after a restart")
	assert.False(t, restarted.IsPaused("other"))

	post("/api/board/team/resume")
	again := NewBoardHandler(bh.bs)
	again.LoadPaused(context.Background())
	assert.False(t, again.IsPaused("team"), "resumed: not paused after a restart")
}

// Finding 5: the identity check reads a board request's body to find the identities it names;
// a body over the cap is refused with 413 before it is buffered whole, a normal one goes through.
func TestBoardBodyIsCappedBeforeTheIdentityCheck(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = bh.bs.Subscribe(ctx, "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)
	token, err := db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })
	reached := 0
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	do := func(body string) int {
		req := httptest.NewRequest("POST", "/api/board/p/messages", strings.NewReader(body))
		req.Header.Set(store.SessionTokenHeader, token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, do(`{"subscriber_id":"Dev","content":"hi"}`))
	assert.Equal(t, 1, reached)
	big := `{"subscriber_id":"Dev","content":"` + strings.Repeat("x", maxBoardBody) + `"}`
	assert.Equal(t, http.StatusRequestEntityTooLarge, do(big))
	assert.Equal(t, 1, reached, "never reaches the handler")
}
