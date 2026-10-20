package routes

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func headersOf(t *testing.T, url string) http.Header {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	require.Equal(t, 200, resp.StatusCode, url)
	return resp.Header
}

// #5435: the three board reads carry the board's stable id and its creation time as headers, and a rename changes neither.
func TestBoardReadsCarryTheBoardsIdentity(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	created, err := handler.bs.CreateBoard(ctx, "Launch Team", "tester")
	require.NoError(t, err)
	id := created.ID
	base := server.URL + "/api/board/" + id
	subscribeSession(t, handler, id, "Lead", "")
	postJSON(t, base+"/messages", map[string]string{"subscriber_id": "Lead", "content": "hello"}).Body.Close()
	postJSON(t, base+"/tasks", map[string]string{"title": "A task", "created_by": "Lead"}).Body.Close()

	want, err := time.Parse(time.RFC3339, created.CreatedAt)
	require.NoError(t, err)
	for _, url := range []string{base + "/messages", base + "/messages/all", base + "/messages/all?limit=1", base + "/tasks", base + "/tasks?full=1", base + "/tasks?status=all"} {
		h := headersOf(t, url)
		assert.Equal(t, id, h.Get(BoardIDHeader), url)
		got, err := time.Parse(time.RFC3339, h.Get(BoardCreatedAtHeader))
		require.NoError(t, err, url)
		assert.True(t, got.Equal(want), "%s: created %s, want %s", url, got, want)
	}

	// A rename changes the label, not the id nor the creation time; the bare array reply is unchanged in shape.
	require.NoError(t, handler.bs.RenameBoard(ctx, id, "Renamed Team"))
	h := headersOf(t, base+"/messages/all")
	assert.Equal(t, id, h.Get(BoardIDHeader))
	got, _ := time.Parse(time.RFC3339, h.Get(BoardCreatedAtHeader))
	assert.True(t, got.Equal(want))
	resp, err := http.Get(base + "/messages/all")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, byte('['), b[0], "the messages reply is still a JSON array")
}

// A board no row names (a read of a name nothing uses) gets no identity headers and its read is not failed by that.
func TestABoardWithNoRowGetsNoIdentityHeaders(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	h := headersOf(t, server.URL+"/api/board/nothing-here/messages/all")
	assert.Empty(t, h.Get(BoardIDHeader))
	assert.Empty(t, h.Get(BoardCreatedAtHeader))
}

// #5436: only an id that is plain printable text becomes a header value.
func TestOnlyAPlainBoardIdIsAHeaderValue(t *testing.T) {
	for id, ok := range map[string]bool{
		"b-1234abcd": true, "b-AbC123xyz9": true, "Launch Team": true, "my.board_1-2": true,
		"": false, " lead": false, "trail ": false, "a\r\nX-Evil: 1": false, "a\nb": false, "tab\there": false, "del\x7f": false, "caf\u00e9": false, "\x00": false,
		string(make([]byte, 129)): false,
	} {
		assert.Equal(t, ok, headerSafeID(id), "%q", id)
	}
	assert.True(t, headerSafeID(strings.Repeat("a", 128)))
	assert.False(t, headerSafeID(strings.Repeat("a", 129)))
}
