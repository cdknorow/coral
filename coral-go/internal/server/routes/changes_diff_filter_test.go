package routes

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/go-chi/chi/v5"
)

const (
	secretEnv = "API_TOKEN=do-not-leak-3555"
	secretKey = "-----BEGIN OPENSSH PRIVATE KEY----- sekrit-3555"
)

// #3555: a stored or tunneled diff keeps ordinary changes and replaces a sensitive path, a
// dot-file, a binary section or an oversized one with a one-line note.
func TestFilterPatch(t *testing.T) {
	section := func(path, body string) string {
		return fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n", path, path, path, body)
	}
	big := strings.Repeat("x", diffSectionMax+10)
	patch := "# a note before\n" +
		section("src/main.go", "fmt.Println(1)") +
		section(".env.local", secretEnv) +
		section("keys/id_rsa", secretKey) +
		"diff --git a/shot.png b/shot.png\nnew file mode 100644\nGIT binary patch\nliteral 12\nzcmZ?wbhEHbWMp7uXlP(yU|@&|\n\n" +
		section("big.txt", big) +
		section("dir with space/ok.md", "fine")
	out := string(filterPatch([]byte(patch)))
	assert.Contains(t, out, "# a note before")
	assert.Contains(t, out, "fmt.Println(1)")
	assert.Contains(t, out, "+fine")
	assert.NotContains(t, out, "do-not-leak")
	assert.NotContains(t, out, "sekrit")
	assert.NotContains(t, out, "zcmZ?w")
	assert.NotContains(t, out, big)
	for _, note := range []string{"changes to .env.local not shown (sensitive path)", "changes to keys/id_rsa not shown (sensitive path)",
		"changes to shot.png not shown (binary file)", "changes to big.txt not shown"} {
		assert.Contains(t, out, note)
	}
	// A quoted path (git's C-style quoting) is read correctly.
	quoted := "diff --git \"a/.env prod\" \"b/.env prod\"\n+X=" + secretEnv + "\n"
	assert.NotContains(t, string(filterPatch([]byte(quoted))), "do-not-leak")

	// The cap cuts at a line end and says so.
	capped := string(capDiff([]byte(strings.Repeat("line\n", 100)), 23))
	assert.True(t, strings.HasPrefix(capped, "line\nline\nline\nline\n#"), capped)
	assert.Contains(t, capped, "diff cut at")
}

func diffRepo(t *testing.T) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return repo
}

// The persisted diff of a checkout: the tracked change and a small untracked text file are
// in it; an untracked .env.local, a key, a big image and a small binary are only noted.
func TestGeneratePersistedDiff(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	repo := diffRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "notes.md"), []byte("a plain new file\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".env.local"), []byte(secretEnv+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "keys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "keys", "id_rsa"), []byte(secretKey+"\n"), 0o644))
	big := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}, 50<<10) // 400 KiB
	require.NoError(t, os.WriteFile(filepath.Join(repo, "shot.png"), big, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tiny.bin"), []byte{1, 0, 2, 0, 3}, 0o644))
	bigText := strings.Repeat("a log line that goes on\n", 12<<10) // ~288 KiB of text: only the size rule applies
	require.NoError(t, os.WriteFile(filepath.Join(repo, "big.log"), []byte(bigText), 0o644))

	diff, err := h.generatePersistedDiff(context.Background(), repo)
	require.NoError(t, err)
	out := string(diff)
	assert.Contains(t, out, "+func main() {}")
	assert.Contains(t, out, "+a plain new file")
	assert.NotContains(t, out, "do-not-leak")
	assert.NotContains(t, out, "sekrit")
	assert.Contains(t, out, "new file .env.local not shown (sensitive path)")
	assert.Contains(t, out, "new file keys/id_rsa not shown (sensitive path)")
	assert.Contains(t, out, fmt.Sprintf("new file shot.png, %d bytes, not stored", len(big)))
	assert.Contains(t, out, "new file tiny.bin, 5 bytes, not stored (binary)")
	assert.Contains(t, out, fmt.Sprintf("new file big.log, %d bytes, not stored", len(bigText)))
	assert.NotContains(t, out, "a log line that goes on")
	assert.Less(t, len(diff), 4096, "a small diff for a big checkout")

	// The live diff on this computer is unchanged (it still has everything).
	live, err := h.generateChangesDiff(context.Background(), repo)
	require.NoError(t, err)
	assert.Contains(t, string(live), "do-not-leak", "the local live view is unchanged")
}

// The cap: tracked changes over 8 MiB are cut, with the note.
func TestGeneratePersistedDiffCap(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	repo := diffRepo(t)
	line := strings.Repeat("y", 99) + "\n"
	for i := 0; i < 45; i++ { // 45 files of 200 KiB: each section under 256 KiB, 9 MiB in all
		require.NoError(t, os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%02d.txt", i)), []byte(strings.Repeat(line, 2048)), 0o644))
	}
	diff, err := h.generatePersistedDiff(context.Background(), repo)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(diff), diffPersistMax+256)
	assert.Contains(t, string(diff), "# Coral: diff cut at 8 MiB")
}

// Identical diffs are stored once: two tasks' changes.diff are the same file on disk.
func TestChangesDiffDedupe(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "artifacts", "tasks", "1", "changes.diff")
	b := filepath.Join(root, "artifacts", "tasks", "2", "changes.diff")
	diff := []byte("diff --git a/x b/x\n+same\n")
	require.NoError(t, writeChangesArtifact(a, diff))
	require.NoError(t, writeChangesArtifact(b, diff))
	fa, err := os.Stat(a)
	require.NoError(t, err)
	fb, err := os.Stat(b)
	require.NoError(t, err)
	assert.True(t, os.SameFile(fa, fb), "stored once, linked twice")
	objs, _ := os.ReadDir(filepath.Join(root, "artifacts", "diff-objects"))
	assert.Len(t, objs, 1)
	got, _ := os.ReadFile(b)
	assert.Equal(t, diff, got)
	// Rewriting one task's diff doesn't change the other's.
	require.NoError(t, writeChangesArtifact(a, []byte("diff --git a/x b/x\n+other\n")))
	got, _ = os.ReadFile(b)
	assert.Equal(t, diff, got)
	if fi, _ := os.Stat(b); fi != nil {
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
}

// The Orchestrator's test: a diff stored BEFORE #3555 (an untracked .env.local and an image
// in it) served through Coral Hub carries none of their bytes; locally it's served whole.
// A tunneled live diff has no X-Coral-Working-Directory.
func TestTunneledDiffsAreFiltered(t *testing.T) {
	bs, err := board.NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	coral := t.TempDir()
	h := NewBoardHandler(bs)
	h.SetTaskArtifactWriter(coral, nil)
	task, err := bs.CreateTask(context.Background(), "team", "t", "", "medium", "Operator")
	require.NoError(t, err)
	old := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/.env.local b/.env.local\nnew file mode 100644\n--- /dev/null\n+++ b/.env.local\n@@ -0,0 +1 @@\n+" + secretEnv + "\n" +
		"diff --git a/shot.png b/shot.png\nnew file mode 100644\nGIT binary patch\nliteral 12\nzcmZ?wbhEHbWMp7uXlP(yU|@&|\n\n"
	path := h.taskArtifactPath(task.ID)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(old), 0o644))

	r := chi.NewRouter()
	r.Get("/api/board/{project}/tasks/{taskID}/changes.diff", h.TaskChangesDiff)
	get := func(tunneled bool) string {
		req := httptest.NewRequest("GET", "/api/board/team/tasks/"+strconv.FormatInt(task.ID, 10)+"/changes.diff", nil)
		if tunneled {
			req = stamp(req)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}
	hub := get(true)
	assert.Contains(t, hub, "+b")
	assert.NotContains(t, hub, "do-not-leak")
	assert.NotContains(t, hub, "zcmZ?w")
	assert.Contains(t, hub, "changes to .env.local not shown (sensitive path)")
	assert.Contains(t, hub, "changes to shot.png not shown (binary file)")
	assert.Equal(t, old, get(false), "locally the stored diff is served whole")

	// The same for a session's stored diff (both routes).
	_, sh, _, _ := setupSessionsTestServer(t)
	sid := "dddddddd-3555-0000-0000-00000000000d"
	spath := sh.sessionChangesArtifactPath(sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(spath), 0o755))
	require.NoError(t, os.WriteFile(spath, []byte(old), 0o644))
	sr := chi.NewRouter()
	sr.Get("/api/sessions/{sessionID}/changes.diff", sh.SessionChangesArtifact)
	sr.Get("/api/sessions/{sessionID}/changes", sh.SessionChanges)
	for _, route := range []string{"/changes.diff", "/changes"} {
		rec := httptest.NewRecorder()
		sr.ServeHTTP(rec, stamp(httptest.NewRequest("GET", "/api/sessions/"+sid+route, nil)))
		require.Equal(t, http.StatusOK, rec.Code, route)
		assert.NotContains(t, rec.Body.String(), "do-not-leak", route)
		assert.NotContains(t, rec.Body.String(), "zcmZ?w", route)
		rec = httptest.NewRecorder()
		sr.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/"+sid+route, nil))
		assert.Contains(t, rec.Body.String(), "do-not-leak", "locally %s is served whole", route)
	}

	// The live diff through the tunnel: filtered, and no local path.
	repo := diffRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".env.local"), []byte(secretEnv+"\n"), 0o644))
	rec := httptest.NewRecorder()
	req := stamp(httptest.NewRequest("GET", "/api/sessions/live/x/changes.diff", nil))
	writeSessionChanges(rec, req, safeDiff([]byte(old)), repo)
	assert.Empty(t, rec.Header().Get("X-Coral-Working-Directory"))
	rec = httptest.NewRecorder()
	writeSessionChanges(rec, httptest.NewRequest("GET", "/x", nil), []byte(old), repo)
	assert.Equal(t, repo, rec.Header().Get("X-Coral-Working-Directory"), "locally unchanged")
	live, err := sh.generatePersistedDiff(context.Background(), repo)
	require.NoError(t, err)
	assert.NotContains(t, string(live), "do-not-leak")
}
