package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #5019 review: the agent's own display name (and its job title on its board) are taken for ITSELF, but a handler does not resolve either: a
// name in the path is the stored agent name (the folder), the tmux name or an id, and with no session_id it picks the FIRST live session of
// that name. So a display name or a title that equals ANOTHER board's folder name turns a read of that other board's session into "reading
// oneself". By hand this is a real read: after 'coral-board join <own board> --as d' (an agent may re-join its own board with any role) the
// token that got 403 for GET /api/sessions/live/d/events read the other team's events. The name is only itself when a session of that name
// is itself.
func TestAgentOwnDisplayNameDoesNotOpenAnotherBoardsFolderName(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	const me, victim = "aaaaaaaa-5019-4000-8000-0000000000e1", "bbbbbbbb-5019-4000-8000-0000000000e2"
	board := func(ls *store.LiveSession, b string) *store.LiveSession { ls.BoardName = &b; return ls }
	// another board's agent, in a folder named "victim-folder"; registered first, so it is the first live session of that name
	require.NoError(t, ss.RegisterLiveSession(ctx, board(&store.LiveSession{SessionID: victim, AgentType: "claude", AgentName: "victim-folder", WorkingDir: t.TempDir()}, "team-y")))
	// the caller, whose DISPLAY name is that same text (set by a person, or by anything that can name an agent)
	display := "victim-folder"
	require.NoError(t, ss.RegisterLiveSession(ctx, board(&store.LiveSession{SessionID: me, AgentType: "claude", AgentName: "mine", WorkingDir: t.TempDir(), DisplayName: &display}, "team-x")))
	token, err := db.MintSessionToken(ctx, me, "claude-mine")
	require.NoError(t, err)

	o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
	r := chi.NewRouter()
	r.Use(o.Middleware)
	r.Get("/api/sessions/live/{name}/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	get := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(store.SessionTokenHeader, token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusForbidden, get("/api/sessions/live/victim-folder/events"),
		"a display name equal to another board's folder name is not that board's session: the handler would read it")
	assert.Equal(t, http.StatusOK, get("/api/sessions/live/mine/events"), "its own folder name")
	assert.Equal(t, http.StatusOK, get("/api/sessions/live/claude-"+me+"/events"), "its own tmux name")
}
