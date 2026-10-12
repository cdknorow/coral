package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type dashboardPage struct {
	Messages []struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
	} `json:"messages"`
	Total       int    `json:"total"`
	Offset      int    `json:"offset"`
	Paging      string `json:"paging"`
	BeforeCount *int   `json:"before_count"`
}

func getDashboardPage(t *testing.T, url string) dashboardPage {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var page dashboardPage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))
	return page
}

func (p dashboardPage) ids() []int64 {
	out := make([]int64, len(p.Messages))
	for i, m := range p.Messages {
		out[i] = m.ID
	}
	return out
}

func TestListAllMessagesKeysetPaging(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := handler.bs.Subscribe(ctx, "keys", "agent-1", "Dev", "", nil, nil, "all")
	require.NoError(t, err)
	var ids []int64
	for i := 0; i < 9; i++ {
		m, err := handler.bs.PostMessage(ctx, "keys", "agent-1", fmt.Sprintf("m%d", i), nil)
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	base := server.URL + "/api/board/keys/messages/all?format=dashboard&limit=4"

	// Latest page (what every dashboard loads first) reports what is older.
	latest := getDashboardPage(t, base)
	require.Equal(t, ids[5:], latest.ids())
	require.Equal(t, 9, latest.Total)
	require.Equal(t, "keyset", latest.Paging)
	require.Equal(t, 5, *latest.BeforeCount)

	// Load earlier by id until nothing remains.
	page := getDashboardPage(t, base+fmt.Sprintf("&before_id=%d", ids[5]))
	require.Equal(t, ids[1:5], page.ids())
	require.Equal(t, 1, *page.BeforeCount)
	page = getDashboardPage(t, base+fmt.Sprintf("&before_id=%d", ids[1]))
	require.Equal(t, ids[:1], page.ids())
	require.Equal(t, 0, *page.BeforeCount)
	page = getDashboardPage(t, base+fmt.Sprintf("&before_id=%d", ids[0]))
	require.Empty(t, page.ids())
	require.Equal(t, 0, *page.BeforeCount)

	// Poll for new messages after the newest loaded one.
	page = getDashboardPage(t, base+fmt.Sprintf("&after_id=%d", ids[8]))
	require.Empty(t, page.ids())
	require.Equal(t, 9, *page.BeforeCount)
	newer, err := handler.bs.PostMessage(ctx, "keys", "agent-1", "new", nil)
	require.NoError(t, err)
	page = getDashboardPage(t, base+fmt.Sprintf("&after_id=%d", ids[8]))
	require.Equal(t, []int64{newer.ID}, page.ids())
	require.Equal(t, 10, page.Total)
	page = getDashboardPage(t, base+"&after_id=0")
	require.Equal(t, ids[:4], page.ids(), "after_id=0 starts at the oldest message")

	// Offset paging is unchanged for older clients and remotes, and says nothing about keyset.
	page = getDashboardPage(t, base+"&offset=2")
	require.Equal(t, ids[2:6], page.ids())
	require.Equal(t, 2, page.Offset)
	require.Empty(t, page.Paging)
	require.Nil(t, page.BeforeCount)

	// The CLI's bare array format is unchanged too.
	resp, err := http.Get(server.URL + "/api/board/keys/messages/all?limit=2")
	require.NoError(t, err)
	defer resp.Body.Close()
	var bare []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&bare))
	require.Len(t, bare, 2)
	require.EqualValues(t, newer.ID, bare[1]["id"])
}
