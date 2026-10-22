package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6072: an agent in a folder that is not a git repository has nothing to compare. The changed-files list answers 200 with is_git false (the panel says "Not a git folder"), not the 400
// "Could not determine working directory" that showed as "0 files changed vs HEAD~1"; a folder that is a repository carries no is_git field.
func TestFilesInAFolderThatIsNotAGitRepositorySaysSo(t *testing.T) {
	_, handler, terminal, ss := setupSessionsTestServer(t)
	router := chi.NewRouter()
	router.Get("/api/sessions/live/{name}/files", handler.Files)
	router.Post("/api/sessions/live/{name}/files/refresh", handler.RefreshFiles)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx := context.Background()
	plain := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("hi\n"), 0o644))

	const sid = "00000000-0000-0000-0000-0000000060f2"
	name := "claude-" + sid
	terminal.addSession(name, plain)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: plain}))

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/sessions/live/" + name + "/files?session_id=" + sid},
		{http.MethodPost, "/api/sessions/live/" + name + "/files/refresh"},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, tc.method+" "+tc.path)
		assert.Equal(t, false, body["is_git"], tc.method+": is_git is false")
		assert.Empty(t, body["files"], tc.method+": nothing is listed")
	}
}
