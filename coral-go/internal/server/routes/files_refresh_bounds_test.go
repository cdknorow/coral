package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
)

// #3596 W5: the changed-files refresh counts a new file's lines through the working tree held as
// a safefs root. A file reached through a symlinked folder (from git's untracked list or from a
// hook event's file_path) or a symlink itself is never read: its line count would tell a remote
// viewer how long an outside file is.
func TestRefreshFilesCountsOnlyFilesInsideTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	_, handler, terminal, ss := setupSessionsTestServer(t)
	router := chi.NewRouter()
	router.Post("/api/sessions/live/{name}/files/refresh", handler.RefreshFiles)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx := context.Background()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+base)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init", "-q")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644))
	git("add", "-A")
	git("commit", "-qm", "init")
	// 37 lines outside the tree, reached through a symlinked folder and a symlinked file.
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(strings.Repeat("s\n", 37)), 0o600))
	linktest.Symlink(t, outside, filepath.Join(repo, "linkdir"))
	linktest.Symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(repo, "linkfile.txt"))
	// A real new file inside: still counted.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "new.txt"), []byte("a\nb\nc\n"), 0o644))

	const sid = "00000000-0000-0000-0000-00000000f5f5"
	name := "claude-" + sid
	terminal.addSession(name, repo)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: repo}))
	tool := "Write"
	sidCopy := sid
	// The event carries the path the agent wrote, as git reports the tree (symlinks of the temp
	// folder resolved, e.g. macOS's /var -> /private/var).
	realRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	for _, fp := range []string{filepath.Join(realRepo, "linkdir", "secret.txt"), filepath.Join(realRepo, "linkfile.txt")} {
		det, _ := json.Marshal(map[string]string{"file_path": fp})
		ds := string(det)
		_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sidCopy, EventType: "tool_use", ToolName: &tool, DetailJSON: &ds})
		require.NoError(t, err)
	}

	req, err := http.NewRequest("POST", server.URL+"/api/sessions/live/"+name+"/files/refresh", bytes.NewBufferString(`{"session_id":"`+sid+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct {
		Files []store.ChangedFile `json:"files"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	adds := map[string]int{}
	for _, f := range got.Files {
		adds[f.Filepath] = f.Additions
	}
	assert.Equal(t, 3, adds["new.txt"], "a real new file is still counted: %v", adds)
	for f, n := range adds {
		assert.NotEqual(t, 37, n, "%s shows the outside file's line count", f)
	}
	_, listed := adds[filepath.Join("linkdir", "secret.txt")]
	assert.False(t, listed, "a file through a symlinked folder isn't listed: %v", adds)
}
