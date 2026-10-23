package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
)

// #6207: the History list carries what the person typed first, for the title to lead with; a row indexed before it was kept says "".
func TestTheHistoryListCarriesTheFirstUserMessage(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ts := "2026-10-23T00:00:00Z"
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: "new", SourceType: "claude", SourceFile: "/h/new.jsonl", FirstTimestamp: &ts, LastTimestamp: &ts, MessageCount: 2, DisplaySummary: "I am in the project folder.", FirstUserMessage: "which folder are you in?", IndexVersion: 2}))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: "old", SourceType: "claude", SourceFile: "/h/old.jsonl", FirstTimestamp: &ts, LastTimestamp: &ts, MessageCount: 2, DisplaySummary: "An old summary."}))
	h := NewHistoryHandler(db, &config.Config{}, nil)
	r := chi.NewRouter()
	r.Get("/api/sessions/history", h.ListSessions)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	resp, err := http.Get(server.URL + "/api/sessions/history?type=agent")
	require.NoError(t, err)
	defer resp.Body.Close()
	var out struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	got := map[string]any{}
	for _, row := range out.Sessions {
		got[row["session_id"].(string)] = row["first_user_message"]
	}
	assert.Equal(t, map[string]any{"new": "which folder are you in?", "old": ""}, got)
}
