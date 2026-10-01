package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
)

func setupChatSearchTestEnv(t *testing.T) (*store.DB, *store.SessionStore, *board.Store, string, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)

	boardDBPath := filepath.Join(dir, "board.db")
	bs, err := board.NewStore(boardDBPath)
	require.NoError(t, err)

	ss := store.NewSessionStore(db)

	cleanup := func() {
		db.Close()
		bs.Close()
	}
	return db, ss, bs, dir, cleanup
}

func writeClaudeJSONLTranscript(t *testing.T, path string, entries []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		require.NoError(t, err)
		_, err = f.Write(append(data, '\n'))
		require.NoError(t, err)
	}
}

func TestChatSearchVerification_Backend(t *testing.T) {
	db, ss, bs, tempDir, cleanup := setupChatSearchTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// Session A: 2 matching messages ("database migration" and "database migration verified")
	// Timestamp: older
	fileA := filepath.Join(tempDir, "session_a.jsonl")
	writeClaudeJSONLTranscript(t, fileA, []map[string]any{
		{"type": "user", "timestamp": "2026-09-30T10:00:00Z", "message": map[string]any{"content": "Please start the database migration now"}},
		{"type": "assistant", "timestamp": "2026-09-30T10:01:00Z", "message": map[string]any{"content": "Starting the process..."}},
		{"type": "assistant", "timestamp": "2026-09-30T10:02:00Z", "message": map[string]any{"content": "The database migration verified successfully"}},
	})
	tsA := "2026-09-30T10:02:00Z"
	err := ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      "sid-a",
		SourceType:     "claude",
		SourceFile:     fileA,
		LastTimestamp:  &tsA,
		DisplaySummary: "database migration",
	})
	require.NoError(t, err)
	require.NoError(t, ss.UpsertFTS(ctx, "sid-a", "Please start the database migration now Starting the process The database migration verified successfully"))

	// Session B: 1 matching message ("database migration in staging")
	// Timestamp: newest
	fileB := filepath.Join(tempDir, "session_b.jsonl")
	writeClaudeJSONLTranscript(t, fileB, []map[string]any{
		{"type": "user", "timestamp": "2026-09-30T12:00:00Z", "message": map[string]any{"content": "Testing database migration in staging"}},
		{"type": "assistant", "timestamp": "2026-09-30T12:05:00Z", "message": map[string]any{"content": "Staging looks good."}},
	})
	tsB := "2026-09-30T12:05:00Z"
	err = ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      "sid-b",
		SourceType:     "claude",
		SourceFile:     fileB,
		LastTimestamp:  &tsB,
		DisplaySummary: "staging check database migration",
	})
	require.NoError(t, err)
	require.NoError(t, ss.UpsertFTS(ctx, "sid-b", "Testing database migration in staging Staging looks good"))

	// Session C: 1 matching message ("database migration rollback plan")
	// Timestamp: older than B, newer than A
	fileC := filepath.Join(tempDir, "session_c.jsonl")
	writeClaudeJSONLTranscript(t, fileC, []map[string]any{
		{"type": "user", "timestamp": "2026-09-30T11:00:00Z", "message": map[string]any{"content": "What is the database migration rollback plan?"}},
	})
	tsC := "2026-09-30T11:00:00Z"
	err = ss.UpsertSessionIndex(ctx, &store.SessionIndex{
		SessionID:      "sid-c",
		SourceType:     "claude",
		SourceFile:     fileC,
		LastTimestamp:  &tsC,
		DisplaySummary: "rollback database migration",
	})
	require.NoError(t, err)
	require.NoError(t, ss.UpsertFTS(ctx, "sid-c", "What is the database migration rollback plan?"))

	// Add Board messages
	_, err = bs.Subscribe(ctx, "core-infra", "bot1", "Infra Bot", "sid-bot1", nil, nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "core-infra", "bot1", "Announcing database migration schedule", nil)
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "core-infra", "bot1", "database migration post-mortem notes", nil)
	require.NoError(t, err)

	cfg := &config.Config{}
	handler := NewHistoryHandler(db, cfg, bs)

	t.Run("QueryValidationBounds", func(t *testing.T) {
		// Empty query
		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("empty query status = %d, want 400", w.Code)
		}

		// Oversized query (>1000 chars)
		huge := strings.Repeat("x", 1001)
		req = httptest.NewRequest("GET", "/api/sessions/history/search?q="+huge, nil)
		w = httptest.NewRecorder()
		handler.SearchChats(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("huge query status = %d, want 400", w.Code)
		}
	})

	t.Run("Ranking_MatchCountAndRecency", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=agent", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var resp struct {
			Status  string             `json:"status"`
			Total   int                `json:"total"`
			Results []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

		if resp.Status != "complete" {
			t.Errorf("resp.Status = %q, want 'complete'", resp.Status)
		}
		if len(resp.Results) != 3 {
			t.Fatalf("results count = %d, want 3", len(resp.Results))
		}

		// Check ordering:
		if resp.Results[0].SessionID != "sid-a" {
			t.Errorf("1st ranked = %s (score %f), want sid-a (score 2)", resp.Results[0].SessionID, resp.Results[0].Score)
		}
		if resp.Results[1].SessionID != "sid-b" {
			t.Errorf("2nd ranked = %s (ts %s), want sid-b (newer)", resp.Results[1].SessionID, resp.Results[1].LastTimestamp)
		}
		if resp.Results[2].SessionID != "sid-c" {
			t.Errorf("3rd ranked = %s (ts %s), want sid-c (older)", resp.Results[2].SessionID, resp.Results[2].LastTimestamp)
		}

		// Verify locator on sid-a hits
		if len(resp.Results[0].Hits) != 2 {
			t.Fatalf("sid-a hits count = %d, want 2", len(resp.Results[0].Hits))
		}
		hit0 := resp.Results[0].Hits[0]
		if hit0.Locator.SessionID != "sid-a" || hit0.Locator.MessageIndex != 0 {
			t.Errorf("hit 0 locator = %+v, want session_id: sid-a, message_index: 0", hit0.Locator)
		}
		hit1 := resp.Results[0].Hits[1]
		if hit1.Locator.SessionID != "sid-a" || hit1.Locator.MessageIndex != 2 {
			t.Errorf("hit 1 locator = %+v, want session_id: sid-a, message_index: 2", hit1.Locator)
		}
	})

	t.Run("Ranking_GroupBoardAggregation", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=group", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var resp struct {
			Status  string             `json:"status"`
			Results []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

		if len(resp.Results) != 1 {
			t.Fatalf("group results count = %d, want 1 aggregated project", len(resp.Results))
		}
		res := resp.Results[0]
		if res.Type != "group" || res.Score != 2 {
			t.Errorf("group result = %+v, want type: group, score: 2", res)
		}
		if len(res.Hits) != 2 {
			t.Fatalf("hits count = %d, want 2", len(res.Hits))
		}
		if res.Hits[0].Locator.Project != "core-infra" || res.Hits[0].Locator.MessageID == 0 {
			t.Errorf("hit 0 locator = %+v, want project: core-infra with valid message_id", res.Hits[0].Locator)
		}
	})

	t.Run("Status_PartialWhenTranscriptMissing", func(t *testing.T) {
		tsGhost := "2026-09-30T15:00:00Z"
		err := ss.UpsertSessionIndex(ctx, &store.SessionIndex{
			SessionID:      "sid-ghost",
			SourceType:     "claude",
			SourceFile:     filepath.Join(tempDir, "does_not_exist.jsonl"),
			LastTimestamp:  &tsGhost,
			DisplaySummary: "database migration missing",
		})
		require.NoError(t, err)
		require.NoError(t, ss.UpsertFTS(ctx, "sid-ghost", "ghost database migration missing"))

		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=agent", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)

		var resp struct {
			Status        string `json:"status"`
			IndexCoverage struct {
				UnavailableSessions int `json:"unavailable_sessions"`
			} `json:"index_coverage"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

		if resp.Status != "partial" {
			t.Errorf("expected status 'partial', got %q", resp.Status)
		}
		if resp.IndexCoverage.UnavailableSessions < 1 {
			t.Errorf("unavailable_sessions = %d, want >= 1", resp.IndexCoverage.UnavailableSessions)
		}
	})

	t.Run("UTF16Offsets_EmojiAndMultibyteAccuracy", func(t *testing.T) {
		text := "🚀 Rocket ✨ Sparkle needle"
		off := strings.Index(text, "needle")
		offsets := [][]int{{off, off + len("needle")}}
		converted := utf16Offsets(text, offsets)
		if len(converted) != 1 {
			t.Fatalf("expected 1 offset, got %v", converted)
		}
		uStart, uEnd := converted[0][0], converted[0][1]

		expectedStart := 20
		expectedEnd := 26
		if uStart != expectedStart || uEnd != expectedEnd {
			t.Errorf("utf16Offsets = [%d, %d], want [%d, %d]", uStart, uEnd, expectedStart, expectedEnd)
		}
	})

	t.Run("ExactPhraseVsMultiword", func(t *testing.T) {
		// Quoted exact phrase: "database migration verified" should only match sid-a
		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=%22database+migration+verified%22&type=agent", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp struct {
			Total   int                `json:"total"`
			Results []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 1, resp.Total)
		require.Equal(t, "sid-a", resp.Results[0].SessionID)

		// Multiword unquoted: database migration staging should only match sid-b
		req2 := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration+staging&type=agent", nil)
		w2 := httptest.NewRecorder()
		handler.SearchChats(w2, req2)
		require.Equal(t, http.StatusOK, w2.Code)

		var resp2 struct {
			Total   int                `json:"total"`
			Results []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
		require.Equal(t, 1, resp2.Total)
		require.Equal(t, "sid-b", resp2.Results[0].SessionID)
	})

	t.Run("PaginationAndLimits", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=agent&page=1&page_size=1", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp struct {
			Total    int                `json:"total"`
			Page     int                `json:"page"`
			PageSize int                `json:"page_size"`
			HasMore  bool               `json:"has_more"`
			Results  []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 3, resp.Total)
		require.Equal(t, 1, resp.Page)
		require.Equal(t, 1, resp.PageSize)
		require.True(t, resp.HasMore)
		require.Equal(t, 1, len(resp.Results))
		require.Equal(t, "sid-a", resp.Results[0].SessionID)

		// Page 2
		req2 := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=agent&page=2&page_size=1", nil)
		w2 := httptest.NewRecorder()
		handler.SearchChats(w2, req2)
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
		require.Equal(t, 2, resp.Page)
		require.True(t, resp.HasMore)
		require.Equal(t, "sid-b", resp.Results[0].SessionID)

		// Page 3 (last page)
		req3 := httptest.NewRequest("GET", "/api/sessions/history/search?q=database+migration&type=agent&page=3&page_size=1", nil)
		w3 := httptest.NewRecorder()
		handler.SearchChats(w3, req3)
		require.NoError(t, json.Unmarshal(w3.Body.Bytes(), &resp))
		require.Equal(t, 3, resp.Page)
		require.False(t, resp.HasMore)
		require.Equal(t, "sid-c", resp.Results[0].SessionID)
	})

	t.Run("SafePlainExcerptsNoHTMLInjection", func(t *testing.T) {
		fileXSS := filepath.Join(tempDir, "session_xss.jsonl")
		writeClaudeJSONLTranscript(t, fileXSS, []map[string]any{
			{"type": "user", "timestamp": "2026-09-30T16:00:00Z", "message": map[string]any{"content": "<script>alert('xss')</script> vulnerable needle"}},
		})
		tsXSS := "2026-09-30T16:00:00Z"
		require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{
			SessionID:      "sid-xss",
			SourceType:     "claude",
			SourceFile:     fileXSS,
			LastTimestamp:  &tsXSS,
			DisplaySummary: "xss needle",
		}))
		require.NoError(t, ss.UpsertFTS(ctx, "sid-xss", "script alert xss vulnerable needle"))

		req := httptest.NewRequest("GET", "/api/sessions/history/search?q=needle&type=agent", nil)
		w := httptest.NewRecorder()
		handler.SearchChats(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp struct {
			Results []ChatSearchResult `json:"results"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 1, len(resp.Results))
		hit := resp.Results[0].Hits[0]
		// Excerpt contains the literal raw string "<script>alert('xss')</script> vulnerable needle", not escaped yet (escaping happens in UI)
		require.Contains(t, hit.Excerpt, "<script>")
		// The API does NOT inject <mark> tags into excerpt string itself
		require.NotContains(t, hit.Excerpt, "<mark>")
		// Offsets pinpoint "needle"
		start := hit.MatchOffsets[0][0]
		end := hit.MatchOffsets[0][1]
		require.Equal(t, "needle", hit.Excerpt[start:end])
	})
}
