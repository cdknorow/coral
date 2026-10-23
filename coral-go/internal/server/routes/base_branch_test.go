package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3601 (weak site W24): a base_branch that git would read as an option ('-' first) or that
// carries whitespace, a control character or a backslash is refused when a scheduled job is
// created or edited, so it never reaches 'git worktree add'.
func TestScheduledJobRefusesAnOptionAsBaseBranch(t *testing.T) {
	srv, _ := scheduleTestServer(t)
	repo := gitRepo(t)
	job := func(base string) map[string]any {
		return map[string]any{"name": "nightly", "cron_expr": "0 9 * * 1-5", "repo_path": repo, "prompt": "do it", "base_branch": base, "create_worktree": true}
	}
	for _, base := range []string{"--upload-pack=touch /tmp/pwned", "-b", "main other", "a\\b"} {
		code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job(base))
		assert.Equal(t, http.StatusBadRequest, code, base)
		assert.Contains(t, body["error"], "base_branch", base)
	}
	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job("main"))
	require.Equal(t, http.StatusOK, code, body)
	id := strconv.FormatInt(int64(body["id"].(float64)), 10)

	for _, base := range []any{"--orphan", "-x", 5} {
		code, body := sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"base_branch": base})
		assert.Equal(t, http.StatusBadRequest, code, base)
		assert.Contains(t, body["error"], "base_branch", base)
	}
	code, _ = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"base_branch": "origin/main"})
	assert.Equal(t, http.StatusOK, code)
}

func TestWorkflowAndTaskRunRefuseAnOptionAsBaseBranch(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/base_branch.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	wf := NewWorkflowHandler(db, cfg)
	rec := httptest.NewRecorder()
	wf.CreateWorkflow(rec, httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(`{"name":"wf","base_branch":"--upload-pack=x","steps":[{"name":"one","type":"shell","command":"true"}]}`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "base_branch")

	tasks := NewTasksHandler(db, cfg)
	rec = httptest.NewRecorder()
	body := `{"prompt":"x","repo_path":` + strconv.Quote(gitRepo(t)) + `,"base_branch":"-x"}`
	tasks.SubmitTask(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/run", strings.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "base_branch")
}

// Launching a team with a worktree (reachable at Coral Hub's Launch tier) refuses an option as
// its base branch before git runs: no worktree folder appears.
func TestLaunchTeamRefusesAnOptionAsBaseBranch(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	handler := NewSessionsHandler(db, cfg, nil, newMockTerminal(), bs)
	repo := gitRepo(t)
	for _, base := range []string{"--upload-pack=touch /tmp/pwned", "-b", "main other"} {
		b, _ := json.Marshal(map[string]any{"board_name": "w24team", "working_dir": repo, "worktree": true, "base_branch": base,
			"agents": []map[string]any{{"name": "Orchestrator", "prompt": "lead"}}})
		rec := httptest.NewRecorder()
		handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)))
		assert.Equal(t, http.StatusBadRequest, rec.Code, base)
		assert.Contains(t, rec.Body.String(), "base_branch", base)
		assert.NoDirExists(t, filepath.Join(cfg.CoralDir(), "worktrees", "w24team"), base)
	}
}

// Editing a workflow checks base_branch as creating one does (Security's SHOULD on #3601), and a
// refused edit leaves the stored value unchanged.
func TestWorkflowUpdateRefusesAnOptionAsBaseBranch(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "wf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Post("/api/workflows", h.CreateWorkflow)
	r.Put("/api/workflows/{workflowID}", h.UpdateWorkflow)
	r.Get("/api/workflows/{workflowID}", h.GetWorkflow)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workflows", map[string]any{"name": "wf", "base_branch": "main",
		"steps": []map[string]any{{"name": "one", "type": "shell", "command": "true"}}})
	require.Equal(t, http.StatusCreated, code, body)
	id := strconv.FormatInt(int64(body["id"].(float64)), 10)

	code, body = sendJSON(t, http.MethodPut, srv.URL+"/api/workflows/"+id, map[string]any{"base_branch": "--upload-pack=x"})
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body["error"], "base_branch")
	_, got := sendJSON(t, http.MethodGet, srv.URL+"/api/workflows/"+id, nil)
	assert.Equal(t, "main", got["base_branch"], "the stored value is unchanged")
}

// #6701: a job saved with a base branch its repository does not have is a 400 under the field (not a failed first run), with the branches to pick from; none named is the repository's current branch.
func TestScheduledJobBaseBranchIsCheckedWhenSaved(t *testing.T) {
	srv, _ := scheduleTestServer(t)
	repo, _ := repoOn(t, "master", true)
	job := map[string]any{"name": "nightly", "cron_expr": "0 9 * * 1-5", "repo_path": repo, "prompt": "do it", "base_branch": "release", "create_worktree": true}
	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "base_branch_missing", body["code"])
	assert.Equal(t, "base_branch", body["field"])
	assert.NotEmpty(t, body["branches"])
	assert.Contains(t, body["error"], "The base branch release does not exist in ")

	delete(job, "base_branch")
	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/scheduled/jobs", job)
	require.Equal(t, http.StatusOK, code, body)
	assert.Empty(t, body["base_branch"], "nothing named: the repository's own branch, found when it runs")
	id := strconv.FormatInt(int64(body["id"].(float64)), 10)

	code, body = sendJSON(t, http.MethodPut, srv.URL+"/api/scheduled/jobs/"+id, map[string]any{"base_branch": "release"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "base_branch_missing", body["code"])
}

// The folder's current branch comes with is-git, "" before a first commit.
func TestIsGitAnswersTheCurrentBranch(t *testing.T) {
	e := newBoundsEnv(t)
	r := chi.NewRouter()
	r.Get("/api/filesystem/is-git", (&SystemHandler{}).IsGitDir)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	get := func(dir string) map[string]any {
		req, _ := http.NewRequest("GET", srv.URL+"/api/filesystem/is-git?path="+url.QueryEscape(dir), nil)
		req.Header.Set(tunnel.ViaHeader, "1") // the strict bounds: the folder is under the home
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	mk := func(name string, commit bool) string {
		dir := filepath.Join(e.home, "projects", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for _, args := range [][]string{{"init", "-q", "-b", "trunk"}} {
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		if commit {
			out, err := exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x").CombinedOutput()
			require.NoError(t, err, string(out))
		}
		return dir
	}
	assert.Equal(t, map[string]any{"is_git": true, "branch": "trunk"}, get(mk("committed", true)))
	assert.Equal(t, map[string]any{"is_git": true, "branch": ""}, get(mk("empty", false)))
	plain := filepath.Join(e.home, "projects", "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	assert.Equal(t, map[string]any{"is_git": false}, get(plain))
}
