package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boardOf is a fake board of n messages (ids 1..n) answering /messages/all as the server does: at most 500 a page,
// after_id pages forward by id, oldest first. calls counts the requests.
func boardOf(n int, calls *int) func(path string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		*calls++
		u, err := url.Parse(path)
		if err != nil {
			return nil, err
		}
		after, _ := strconv.Atoi(u.Query().Get("after_id"))
		limit, _ := strconv.Atoi(u.Query().Get("limit"))
		limit = min(limit, 500)
		page := []map[string]any{}
		for id := after + 1; id <= n && len(page) < limit; id++ {
			page = append(page, map[string]any{"id": id, "content": fmt.Sprintf("message %d", id)})
		}
		return json.Marshal(page)
	}
}

// stderrOf runs f and returns what it wrote to stderr.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	f()
	os.Stderr = old
	require.NoError(t, w.Close())
	b, _ := io.ReadAll(r)
	return string(b)
}

// #4628: an export reads every message by paging by id, not one request the server cuts at 500.
func TestFetchAllMessagesPagesThroughTheWholeBoard(t *testing.T) {
	for _, tc := range []struct{ n, calls int }{{0, 1}, {1, 1}, {499, 1}, {500, 2}, {501, 2}, {1200, 3}, {1500, 4}} {
		calls := 0
		var got []map[string]any
		var err error
		warn := stderrOf(t, func() { got, err = fetchAllMessages("team a", boardOf(tc.n, &calls)) })
		require.NoError(t, err, tc.n)
		require.Len(t, got, tc.n, "%d messages", tc.n)
		for i, m := range got {
			require.EqualValues(t, i+1, m["id"], "in order, none twice or missing")
		}
		assert.Equal(t, tc.calls, calls, "requests for %d messages", tc.n)
		assert.Empty(t, warn)
	}
}

func TestFetchAllMessagesAsksForPagesOfTheServersSizeByAfterID(t *testing.T) {
	var paths []string
	_, err := fetchAllMessages("team a/b", func(path string) ([]byte, error) {
		paths = append(paths, path)
		return []byte(`[]`), nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/team%20a%2Fb/messages/all?after_id=0&limit=500"}, paths)
}

// A server that ignores after_id answers its newest page every time: the export keeps that page and says it is
// only the newest, never a silent cut presented as the board.
func TestFetchAllMessagesSaysWhenTheServerCannotPage(t *testing.T) {
	calls := 0
	newest := func(path string) ([]byte, error) {
		calls++
		page := []map[string]any{}
		for id := 701; id <= 1200; id++ {
			page = append(page, map[string]any{"id": id})
		}
		return json.Marshal(page)
	}
	var got []map[string]any
	var err error
	warn := stderrOf(t, func() { got, err = fetchAllMessages("b", newest) })
	require.NoError(t, err)
	assert.Len(t, got, 500)
	assert.Contains(t, warn, "cannot page")
	assert.Contains(t, warn, "newest 500")
	assert.LessOrEqual(t, calls, 3, "it did not loop")
}

func TestFetchAllMessagesPassesOnAnErrorAndABadReply(t *testing.T) {
	calls := 0
	_, err := fetchAllMessages("b", func(path string) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("Error: Coral answered 500")
		}
		return boardOf(1200, new(int))(path)
	})
	assert.ErrorContains(t, err, "answered 500", "a failure half way is not an export of the first page")
	_, err = fetchAllMessages("b", func(string) ([]byte, error) { return []byte(`{"error":"x"}`), nil })
	assert.Error(t, err)
}
