package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3718: the history transcript pages (?limit=N&offset=M, the N messages ending M before the
// newest) with its total and the page's first index, so through Coral Hub no reply has to hold a
// whole transcript; without a limit it is the whole transcript, as before.
func TestHistoryDetailPages(t *testing.T) {
	_, h, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()
	dayDir := filepath.Join(codexHome, "sessions", "2026", "10", "16")
	require.NoError(t, os.MkdirAll(dayDir, 0755))
	sid := "f6a7b8c9-d0e1-2345-f012-3456789abcde"
	const n = 250
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-16T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-pages","cwd":"/repo"}}` + "\n")
	fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n", at.CoralSessionMarkerPrefix, sid)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:%02d.000Z","type":"event_msg","payload":{"type":"user_message","message":"message %d","images":[]}}`+"\n", i%60, i)
	}
	rollout := filepath.Join(dayDir, "rollout-pages.jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(b.String()), 0644))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "codex", SourceFile: rollout}))
	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}", h.GetSessionDetail)

	type page struct {
		Messages []map[string]any `json:"messages"`
		Total    int              `json:"total"`
		Start    int              `json:"start"`
	}
	get := func(q string) page {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/history/"+sid+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, q)
		var p page
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
		return p
	}
	content := func(m map[string]any) string { s, _ := m["content"].(string); return s }

	all := get("")
	require.Len(t, all.Messages, n, "no limit: the whole transcript")
	assert.Equal(t, n, all.Total)
	assert.Equal(t, 0, all.Start)

	last := get("?limit=100")
	require.Len(t, last.Messages, 100)
	assert.Equal(t, n, last.Total)
	assert.Equal(t, n-100, last.Start, "the newest page starts 100 before the end")
	assert.Equal(t, content(all.Messages[n-100]), content(last.Messages[0]))
	assert.Equal(t, content(all.Messages[n-1]), content(last.Messages[99]))

	older := get("?limit=100&offset=100")
	require.Len(t, older.Messages, 100)
	assert.Equal(t, n-200, older.Start)
	assert.Equal(t, content(all.Messages[n-200]), content(older.Messages[0]))

	first := get("?limit=100&offset=200")
	require.Len(t, first.Messages, n-200, "the oldest page is what is left")
	assert.Equal(t, 0, first.Start)

	beyond := get("?limit=100&offset=1000")
	assert.Empty(t, beyond.Messages, "past the start: an empty page, not an error")
	assert.Equal(t, n, beyond.Total)
}
