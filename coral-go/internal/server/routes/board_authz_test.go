package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
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

// #3864: an agent walked message ids under its own board (coral-board read --id N, which is
// GET /api/board/<its board>/messages/all?id=N) and got another team's messages. A message is
// read by id only through its own board; another board's id answers exactly as a missing one
// does, so the walk learns neither the content nor that the id exists.
func TestReadMessageByIDStaysOnItsBoard(t *testing.T) {
	server, bh := setupBoardTestServer(t)
	ctx := context.Background()
	var theirs []int64
	for _, text := range []string{"@Security Reviewer your open review: #3840", "[Task #3857 completed by QA Engineer]"} {
		m, err := bh.bs.PostMessage(ctx, "team-a", "Orchestrator", text, nil)
		require.NoError(t, err)
		theirs = append(theirs, m.ID)
	}
	mine, err := bh.bs.PostMessage(ctx, "team-b", "Orchestrator", "our own post", nil)
	require.NoError(t, err)
	get := func(board string, id int64) (int, string) {
		resp, err := http.Get(server.URL + "/api/board/" + board + "/messages/all?id=" + strconv.FormatInt(id, 10))
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	missingCode, missingBody := get("team-b", mine.ID+1000)
	require.Equal(t, http.StatusNotFound, missingCode)
	for id := theirs[0] - 1; id <= mine.ID+1; id++ { // the walk from the report
		code, body := get("team-b", id)
		if id == mine.ID {
			require.Equal(t, http.StatusOK, code)
			assert.Contains(t, body, "our own post")
			continue
		}
		assert.Equal(t, missingCode, code, "id %d under team-b", id)
		assert.Equal(t, missingBody, body, "id %d: the same answer as a missing id", id)
		assert.NotContains(t, body, "Security Reviewer")
		assert.NotContains(t, body, "#3857")
	}
	// Its own board still reads it.
	code, body := get("team-a", theirs[0])
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "your open review")
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

// #3695: the cap holds for a board request with no session token too (a local curl, the
// dashboard), which the identity check doesn't read but the handler's decoder does. A declared
// length over the cap is refused with 413 before the handler; a chunked body is cut at the cap.
func TestTokenlessBoardBodyIsCapped(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })
	reached, read := 0, 0
	var readErr error
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		b, err := io.ReadAll(r.Body)
		read, readErr = len(b), err
	}))
	do := func(body string, chunked bool) int {
		req := httptest.NewRequest("POST", "/api/board/p/messages", strings.NewReader(body))
		if chunked {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, do(`{"subscriber_id":"Dev","content":"hi"}`, false))
	assert.Equal(t, 1, reached)
	assert.NoError(t, readErr)

	big := `{"subscriber_id":"Dev","content":"` + strings.Repeat("x", maxBoardBody) + `"}`
	assert.Equal(t, http.StatusRequestEntityTooLarge, do(big, false))
	assert.Equal(t, 1, reached, "a declared length over the cap never reaches the handler")

	do(big, true)
	assert.Equal(t, 2, reached)
	var tooBig *http.MaxBytesError
	assert.ErrorAs(t, readErr, &tooBig, "a chunked body over the cap is cut")
	assert.LessOrEqual(t, read, maxBoardBody)
}
